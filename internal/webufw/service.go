package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type Config struct {
	Listen       string `json:"listen"`
	PasswordHash string `json:"password_hash"`
}

func (c Config) Validate() error {
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(host) == nil {
		return errors.New("監聽位址需為 IP:連接埠，例如 0.0.0.0:8088")
	}
	if e = validatePort(port, false); e != nil {
		return e
	}
	return nil
}
func readConfig(path string) (Config, error) {
	var c Config
	b, e := readBounded(path)
	if e != nil {
		return c, e
	}
	if e = json.Unmarshal(b, &c); e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	if _, e = bcrypt.Cost([]byte(c.PasswordHash)); e != nil {
		return c, errors.New("管理者密碼尚未初始化")
	}
	return c, nil
}

// initializeConfig is called under the instance lock. Only the hash is persisted;
// the initial password is returned once for the service journal.
func initializeConfig(path string) (Config, string, error) {
	c, err := readConfig(path)
	if !os.IsNotExist(err) {
		return c, "", err
	}
	c = Config{Listen: "127.0.0.1:8088"}
	password := token()[:24]
	c.PasswordHash, err = passwordHash(password)
	if err != nil {
		return Config{}, "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Config{}, "", err
	}
	if err = atomicJSON(path, c, 0600); err != nil {
		return Config{}, "", err
	}
	return c, password, nil
}

func passwordHash(s string) (string, error) {
	if len([]byte(s)) < 12 || len([]byte(s)) > 72 {
		return "", errors.New("密碼長度需為 12–72 位元組")
	}
	b, e := bcrypt.GenerateFromPassword([]byte(s), 12)
	return string(b), e
}

type Caller interface {
	Call(context.Context, string, json.RawMessage) (any, error)
}
type Service struct {
	manager       *Manager
	mu            sync.Mutex
	config        Config
	activeListen  string
	configPath    string
	loginFailures map[string][]time.Time
	authVersion   int
	sourceStage   *sourceStage
}

func NewService(m *Manager, c Config, path string) *Service {
	return &Service{manager: m, config: c, activeListen: c.Listen, configPath: path, loginFailures: map[string][]time.Time{}}
}
func decodeStrict(data []byte, v any) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	d := json.NewDecoder(strings.NewReader(string(data)))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return fmt.Errorf("請求格式錯誤：%w", e)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("請求含多餘內容")
	}
	return nil
}
func (s *Service) Call(ctx context.Context, op string, data json.RawMessage) (any, error) {
	switch op {
	case "changes":
		return s.manager.Changes(ctx)
	case "status", "rules", "containers":
		v, e := s.manager.State(ctx)
		s.mu.Lock()
		v.Environment.Listen = s.activeListen
		s.mu.Unlock()
		return v, e
	case "preview":
		var c Change
		if e := decodeStrict(data, &c); e != nil {
			return nil, e
		}
		return s.manager.Preview(ctx, c)
	case "apply", "confirm", "rollback":
		var in struct {
			ID string `json:"id"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		switch op {
		case "apply":
			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 120*time.Second)
			defer cancel()
			return s.manager.Apply(writeCtx, in.ID)
		case "confirm":
			return s.manager.Confirm(ctx, in.ID)
		default:
			return s.manager.Rollback(ctx, in.ID)
		}
	case "logs":
		var in struct {
			Limit int `json:"limit"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		return s.manager.Logs(ctx, in.Limit)
	case "settings":
		s.mu.Lock()
		defer s.mu.Unlock()
		return map[string]any{"listen": s.config.Listen, "active_listen": s.activeListen, "restart_required": s.config.Listen != s.activeListen, "username": "admin", "auth_version": s.authVersion}, nil
	case "sources":
		st, installed, compatible := installedSource()
		path := st.Path
		if path == "" {
			path = scriptPath
		}
		return map[string]any{"options": sourceOptions(), "installed": installed, "source": st, "compatible": compatible, "path": path}, nil
	case "source.prepare":
		var in struct {
			ID string `json:"id"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		stage, e := fetchSource(ctx, in.ID)
		if e != nil {
			return nil, e
		}
		s.mu.Lock()
		s.sourceStage = stage
		s.mu.Unlock()
		return map[string]any{"id": stage.ID, "commit": stage.Commit, "sha256": stage.SHA256, "url": stage.URL, "size": len(stage.Bytes), "compatible": compatibleSource(stage.sourceState)}, nil
	case "source.install":
		var in struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		s.mu.Lock()
		stage := s.sourceStage
		if stage == nil || stage.ID != in.ID || stage.SHA256 != in.SHA256 {
			s.mu.Unlock()
			return nil, errors.New("來源預覽已變更，請重新下載")
		}
		s.manager.mu.Lock()
		if s.manager.current != nil {
			s.manager.mu.Unlock()
			s.mu.Unlock()
			return nil, fmt.Errorf("%w：請先處理待確認的規則變更", ErrConflict)
		}
		backup, e := installSource(stage)
		s.manager.mu.Unlock()
		if e == nil {
			s.sourceStage = nil
		}
		s.mu.Unlock()
		if e != nil {
			return nil, e
		}
		_ = s.manager.Record("source.install", in.ID+" "+in.SHA256)
		return map[string]any{"ok": true, "backup": backup, "path": scriptPath}, nil
	case "login":
		var in struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Remote   string `json:"remote"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now()
		for key, attempts := range s.loginFailures {
			keep := []time.Time{}
			for _, t := range attempts {
				if now.Sub(t) < 5*time.Minute {
					keep = append(keep, t)
				}
			}
			if len(keep) == 0 {
				delete(s.loginFailures, key)
			} else {
				s.loginFailures[key] = keep
			}
		}
		attempts := s.loginFailures[in.Remote]
		global := s.loginFailures["*global*"]
		if len(attempts) >= 5 || len(global) >= 30 {
			return nil, errors.New("登入嘗試過多，請於 5 分鐘後再試")
		}
		if len(in.Password) > 72 {
			in.Password = "invalid"
		}
		err := bcrypt.CompareHashAndPassword([]byte(s.config.PasswordHash), []byte(in.Password))
		if in.Username != "admin" || err != nil {
			s.loginFailures[in.Remote] = append(attempts, now)
			s.loginFailures["*global*"] = append(global, now)
			return nil, errors.New("帳號或密碼不正確")
		}
		delete(s.loginFailures, in.Remote)
		return map[string]int{"auth_version": s.authVersion}, nil
	case "password":
		var in struct {
			Current  string `json:"current"`
			Password string `json:"password"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if bcrypt.CompareHashAndPassword([]byte(s.config.PasswordHash), []byte(in.Current)) != nil {
			return nil, errors.New("目前密碼不正確")
		}
		h, e := passwordHash(in.Password)
		if e != nil {
			return nil, e
		}
		c := s.config
		c.PasswordHash = h
		if e = atomicJSON(s.configPath, c, 0600); e != nil {
			return nil, e
		}
		s.config = c
		s.authVersion++
		_ = s.manager.Record("password.update", "admin")
		return map[string]bool{"ok": true}, nil
	case "settings.update":
		var in struct {
			Listen string `json:"listen"`
		}
		if e := decodeStrict(data, &in); e != nil {
			return nil, e
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		c := s.config
		c.Listen = in.Listen
		if e := c.Validate(); e != nil {
			return nil, e
		}
		if e := atomicJSON(s.configPath, c, 0600); e != nil {
			return nil, e
		}
		s.config = c
		_ = s.manager.Record("settings.update", c.Listen)
		return map[string]any{"ok": true, "message": "設定已儲存；執行 sudo systemctl restart webufw 後生效。"}, nil
	default:
		return nil, errors.New("不支援的 Agent 操作")
	}
}
func configLocation() string { return filepath.Join("/etc/webufw", "config.json") }
func ensureRoot() error {
	if os.Geteuid() != 0 {
		return errors.New("WebUFW 服務需要 root 權限")
	}
	return nil
}
