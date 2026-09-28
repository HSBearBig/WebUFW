package webufw

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type delayedLogin struct {
	Caller
	verified       chan struct{}
	release        chan struct{}
	passwordCalled chan struct{}
}

func (d delayedLogin) Call(ctx context.Context, op string, data json.RawMessage) (any, error) {
	if op == "password" {
		close(d.passwordCalled)
	}
	v, err := d.Caller.Call(ctx, op, data)
	if op == "login" {
		close(d.verified)
		<-d.release
	}
	return v, err
}

func TestConcurrentLoginCannotSurvivePasswordChange(t *testing.T) {
	w := newTestWeb(t)
	host := "127.0.0.1:8088"
	origin := "http://" + host
	loginBody := `{"username":"admin","password":"test-password-123"}`
	r := request(w, "POST", "/api/v1/login", loginBody, nil, "", origin, host)
	cookie := r.Result().Cookies()[0]
	var auth struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &auth)
	d := delayedLogin{w.api, make(chan struct{}), make(chan struct{}), make(chan struct{})}
	w.api = d
	loginDone := make(chan *httptest.ResponseRecorder, 1)
	passwordDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { loginDone <- request(w, "POST", "/api/v1/login", loginBody, nil, "", origin, host) }()
	<-d.verified
	go func() {
		passwordDone <- request(w, "POST", "/api/v1/password", `{"current":"test-password-123","password":"new-password-123"}`, cookie, auth.CSRF, origin, host)
	}()
	var changed *httptest.ResponseRecorder
	select {
	case <-d.passwordCalled:
		// If password changes can overtake login, release the old response only
		// after invalidation. It must not produce a surviving session.
		changed = <-passwordDone
	case <-time.After(50 * time.Millisecond):
	}
	close(d.release)
	lateLogin := <-loginDone
	if changed == nil {
		changed = <-passwordDone
	}
	if changed.Code != 200 {
		t.Fatal(changed.Body.String())
	}
	if got := request(w, "GET", "/api/v1/status", "", lateLogin.Result().Cookies()[0], "", "", host); got.Code != 401 {
		t.Fatal("old login survived a concurrent password change")
	}
}

func newTestWeb(t *testing.T) *Web {
	h, e := bcrypt.GenerateFromPassword([]byte("test-password-123"), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	m, _ := NewManager(newTestBackend(), "")
	c := Config{Listen: "127.0.0.1:8088", PasswordHash: string(h)}
	return NewWeb(NewService(m, c, ""), c)
}
func TestIPListenAndWildcardHostValidation(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:8088", "0.0.0.0:8088", "192.168.1.10:8088", "[::]:8088"} {
		if err := (Config{Listen: listen}).Validate(); err != nil {
			t.Fatalf("%s: %v", listen, err)
		}
	}
	for _, listen := range []string{"localhost:8088", "0.0.0.0:0", "0.0.0.0:65536", ":8088"} {
		if err := (Config{Listen: listen}).Validate(); err == nil {
			t.Fatalf("invalid listen accepted: %s", listen)
		}
	}
	w := newTestWeb(t)
	w.config.Listen = "0.0.0.0:8088"
	for _, host := range []string{"127.0.0.1:8088", "192.168.1.10:8088", "localhost:8088", "[::1]:8088"} {
		if r := request(w, "GET", "/", "", nil, "", "", host); r.Code != 200 {
			t.Fatalf("%s: %d", host, r.Code)
		}
	}
	for _, host := range []string{"evil.example:8088", "192.168.1.10:9999"} {
		if r := request(w, "GET", "/", "", nil, "", "", host); r.Code != 403 {
			t.Fatalf("%s: %d", host, r.Code)
		}
	}
	if r := request(w, "POST", "/api/v1/login", `{}`, nil, "", "http://evil.example:8088", "192.168.1.10:8088"); r.Code != 403 {
		t.Fatal("cross-origin login accepted", r.Code)
	}
}
func TestOptionalSourcesVisibleWithoutInstall(t *testing.T) {
	w := newTestWeb(t)
	host, origin := "127.0.0.1:8088", "http://127.0.0.1:8088"
	page := request(w, "GET", "/", "", nil, "", "", host)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "source-select") || !strings.Contains(page.Body.String(), "dependency-check") {
		t.Fatal("missing dependency UI")
	}
	login := request(w, "POST", "/api/v1/login", `{"username":"admin","password":"test-password-123"}`, nil, "", origin, host)
	if login.Code != 200 {
		t.Fatal(login.Body.String())
	}
	cookie := login.Result().Cookies()[0]
	r := request(w, "GET", "/api/v1/sources", "", cookie, "", "", host)
	if r.Code != 200 || !strings.Contains(r.Body.String(), "chaifeng") || !strings.Contains(r.Body.String(), "hsbearbig") {
		t.Fatal(r.Code, r.Body.String())
	}
}
func request(w *Web, method, path, body string, cookie *http.Cookie, csrf, origin, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+host+path, strings.NewReader(body))
	r.Host = host
	r.RemoteAddr = "127.0.0.1:9000"
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", csrf)
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	rw := httptest.NewRecorder()
	w.ServeHTTP(rw, r)
	return rw
}
func TestWebAuthCSRFAndXSS(t *testing.T) {
	w := newTestWeb(t)
	host := "127.0.0.1:8088"
	origin := "http://" + host
	if r := request(w, "GET", "/api/v1/status", "", nil, "", "", host); r.Code != 401 {
		t.Fatal(r.Code)
	}
	if r := request(w, "POST", "/api/v1/login", `{"username":"admin","password":"test-password-123"}`, nil, "", "https://evil.test", host); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r := request(w, "GET", "/", "", nil, "", "", "evil.test"); r.Code != 403 {
		t.Fatal(r.Code)
	}
	r := request(w, "POST", "/api/v1/login", `{"username":"admin","password":"test-password-123"}`, nil, "", origin, host)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	cookie := r.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
	var auth map[string]any
	json.Unmarshal(r.Body.Bytes(), &auth)
	csrf := auth["csrf"].(string)
	if r = request(w, "POST", "/api/v1/preview", `{}`, cookie, "bad", origin, host); r.Code != 403 {
		t.Fatal(r.Code)
	}
	if r = request(w, "GET", "/api/v1/status", "", cookie, csrf, "", host); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = request(w, "POST", "/api/v1/password", `{"current":"test-password-123","password":"new-password-123"}`, cookie, csrf, origin, host); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r = request(w, "GET", "/api/v1/status", "", cookie, csrf, "", host); r.Code != 401 {
		t.Fatal("session survived password change")
	}
}
func TestLoginRateLimit(t *testing.T) {
	w := newTestWeb(t)
	for i := 0; i < 5; i++ {
		r := request(w, "POST", "/api/v1/login", `{"username":"admin","password":"wrong"}`, nil, "", "http://127.0.0.1:8088", "127.0.0.1:8088")
		if r.Code != 401 {
			t.Fatal(r.Code)
		}
	}
	r := request(w, "POST", "/api/v1/login", `{"username":"admin","password":"test-password-123"}`, nil, "", "http://127.0.0.1:8088", "127.0.0.1:8088")
	if r.Code != 401 || !strings.Contains(r.Body.String(), "5 分鐘") {
		t.Fatal(r.Body.String())
	}
}
func TestUnknownFieldsAndCommandInjection(t *testing.T) {
	var c Change
	if decodeStrict([]byte(`{"kind":"ufw.toggle","shell":"rm -rf /"}`), &c) == nil {
		t.Fatal("unknown field accepted")
	}
	s, _ := newTestBackend().Snapshot(t.Context())
	_, e := Plan(s, Change{Kind: "docker.add", Revision: s.Revision, Docker: DockerInput{Container: "--privileged", Network: "bridge", Port: "80", Protocol: "tcp", Source: "any"}})
	if e == nil {
		t.Fatal("option injection accepted")
	}
}
