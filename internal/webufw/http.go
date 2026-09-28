package webufw

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS

type session struct {
	CSRF        string
	Expires     time.Time
	AuthVersion int
}
type Web struct {
	api       Caller
	config    Config
	mu        sync.Mutex
	authMu    sync.Mutex
	sessions  map[string]session
	loginGate chan struct{}
	handler   http.Handler
}

func NewWeb(api Caller, c Config) *Web {
	w := &Web{api: api, config: c, sessions: map[string]session{}, loginGate: make(chan struct{}, 2)}
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", w.page)
	m.HandleFunc("GET /assets/", w.asset)
	m.HandleFunc("POST /api/v1/login", w.login)
	m.HandleFunc("GET /api/v1/session", w.session)
	m.HandleFunc("/api/v1/", w.apiRequest)
	w.handler = w.security(m)
	return w
}
func (w *Web) ServeHTTP(rw http.ResponseWriter, r *http.Request) { w.handler.ServeHTTP(rw, r) }
func (w *Web) origin(r *http.Request) string {
	return "http://" + r.Host
}
func (w *Web) allowedHost(host string) bool {
	listenHost, listenPort, err := net.SplitHostPort(w.config.Listen)
	if err != nil {
		return false
	}
	requestHost, requestPort, err := net.SplitHostPort(host)
	if err != nil || requestPort != listenPort {
		return false
	}
	listenIP := net.ParseIP(listenHost)
	if listenIP == nil {
		return false
	}
	if listenIP.IsUnspecified() {
		return requestHost == "localhost" || net.ParseIP(requestHost) != nil
	}
	if requestHost == listenHost {
		return true
	}
	return listenIP.IsLoopback() && requestHost == "localhost"
}
func (w *Web) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("Cache-Control", "no-store")
		if !w.allowedHost(r.Host) {
			writeError(rw, http.StatusForbidden, errors.New("不允許的 Host"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			origin := r.Header.Get("Origin")
			expected := w.origin(r)
			if origin != expected {
				writeError(rw, 403, errors.New("Origin 驗證失敗"))
				return
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				writeError(rw, 415, errors.New("需要 application/json"))
				return
			}
		}
		r.Body = http.MaxBytesReader(rw, r.Body, 16384)
		next.ServeHTTP(rw, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, e error) {
	writeJSON(w, status, map[string]string{"error": e.Error()})
}
func (w *Web) page(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(rw, r)
		return
	}
	t, e := template.ParseFS(assets, "assets/index.html")
	if e != nil {
		http.Error(rw, "template unavailable", 500)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = t.Execute(rw, map[string]string{"Version": Version})
}
func (w *Web) asset(rw http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name != "assets/app.css" && name != "assets/app.js" && name != "assets/theme.js" {
		http.NotFound(rw, r)
		return
	}
	b, e := assets.ReadFile(name)
	if e != nil {
		http.NotFound(rw, r)
		return
	}
	if strings.HasSuffix(name, "css") {
		rw.Header().Set("Content-Type", "text/css; charset=utf-8")
	} else {
		rw.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	}
	rw.Write(b)
}
func (w *Web) getSession(r *http.Request) (string, session, bool) {
	c, e := r.Cookie("webufw_session")
	if e != nil {
		return "", session{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.sessions[c.Value]
	if !ok || time.Now().After(s.Expires) {
		delete(w.sessions, c.Value)
		return "", session{}, false
	}
	return c.Value, s, true
}
func (w *Web) secureCookie(r *http.Request) bool {
	return r.TLS != nil
}
func (w *Web) login(rw http.ResponseWriter, r *http.Request) {
	select {
	case w.loginGate <- struct{}{}:
		defer func() { <-w.loginGate }()
	default:
		writeError(rw, 429, errors.New("請稍後再試"))
		return
	}
	b, e := io.ReadAll(r.Body)
	if e != nil {
		writeError(rw, 400, e)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if e = decodeStrict(b, &in); e != nil {
		writeError(rw, 400, e)
		return
	}
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	data, _ := json.Marshal(map[string]string{"username": in.Username, "password": in.Password, "remote": host})
	// Serialize successful login/session creation with password changes so a
	// delayed login response cannot recreate a session after invalidation.
	w.authMu.Lock()
	defer w.authMu.Unlock()
	v, e := w.api.Call(r.Context(), "login", data)
	if e != nil {
		writeError(rw, 401, e)
		return
	}
	var version struct {
		AuthVersion int `json:"auth_version"`
	}
	vb, _ := json.Marshal(v)
	json.Unmarshal(vb, &version)
	id, csrf := token(), token()
	w.mu.Lock()
	for k, s := range w.sessions {
		if time.Now().After(s.Expires) {
			delete(w.sessions, k)
		}
	}
	if len(w.sessions) >= 20 {
		w.sessions = map[string]session{}
	}
	if old, err := r.Cookie("webufw_session"); err == nil {
		delete(w.sessions, old.Value)
	}
	w.sessions[id] = session{csrf, time.Now().Add(8 * time.Hour), version.AuthVersion}
	w.mu.Unlock()
	http.SetCookie(rw, &http.Cookie{Name: "webufw_session", Value: id, Path: "/", HttpOnly: true, Secure: w.secureCookie(r), SameSite: http.SameSiteStrictMode, MaxAge: 8 * 3600})
	writeJSON(rw, 200, map[string]any{"csrf": csrf})
}
func (w *Web) session(rw http.ResponseWriter, r *http.Request) {
	_, s, ok := w.getSession(r)
	if !ok {
		writeError(rw, 401, errors.New("請登入"))
		return
	}
	writeJSON(rw, 200, map[string]any{"csrf": s.CSRF})
}
func (w *Web) apiRequest(rw http.ResponseWriter, r *http.Request) {
	id, s, ok := w.getSession(r)
	if !ok {
		writeError(rw, 401, errors.New("請登入"))
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	methods := map[string]string{"changes": "GET", "status": "GET", "rules": "GET", "containers": "GET", "logs": "GET", "settings": "GET", "sources": "GET", "preview": "POST", "apply": "POST", "confirm": "POST", "rollback": "POST", "logout": "POST", "password": "POST", "settings.update": "POST", "source.prepare": "POST", "source.install": "POST"}
	method, ok := methods[path]
	if !ok {
		http.NotFound(rw, r)
		return
	}
	if r.Method != method {
		writeError(rw, 405, errors.New("不支援的方法"))
		return
	}
	if method == "POST" && r.Header.Get("X-CSRF-Token") != s.CSRF {
		writeError(rw, 403, errors.New("CSRF 驗證失敗"))
		return
	}
	if path == "logout" {
		w.mu.Lock()
		delete(w.sessions, id)
		w.mu.Unlock()
		http.SetCookie(rw, &http.Cookie{Name: "webufw_session", Value: "", Path: "/", HttpOnly: true, Secure: w.secureCookie(r), SameSite: http.SameSiteStrictMode, MaxAge: -1})
		writeJSON(rw, 200, map[string]bool{"ok": true})
		return
	}
	b := []byte("{}")
	var e error
	if method == "POST" {
		b, e = io.ReadAll(r.Body)
		if e != nil {
			writeError(rw, 400, e)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	if path == "password" {
		w.authMu.Lock()
		defer w.authMu.Unlock()
		if _, _, valid := w.getSession(r); !valid {
			writeError(rw, 401, errors.New("請重新登入"))
			return
		}
	}
	v, e := w.api.Call(ctx, path, b)
	if e != nil {
		code := 400
		if errors.Is(e, ErrConflict) {
			code = 409
		}
		writeError(rw, code, e)
		return
	}
	if path == "password" {
		w.mu.Lock()
		w.sessions = map[string]session{}
		w.mu.Unlock()
	}
	writeJSON(rw, 200, v)
}
