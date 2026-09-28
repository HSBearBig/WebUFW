package webufw

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestInitialPasswordPersistsAcrossRestartAndSettingsChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "etc", "config.json")
	c, password, err := initializeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8088" || len(password) < 24 || bcrypt.CompareHashAndPassword([]byte(c.PasswordHash), []byte(password)) != nil {
		t.Fatal("invalid initial credentials or default listener")
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(b), password) {
		t.Fatal("initial password must only be stored as a hash", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("config must be private", err)
	}
	m, _ := NewManager(newTestBackend(), "")
	s := NewService(m, c, path)
	w := NewWeb(s, c)
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	login := request(w, "POST", "/api/v1/login", string(body), nil, "", "http://127.0.0.1:8088", "127.0.0.1:8088")
	if login.Code != 200 {
		t.Fatal("initial password cannot log in", login.Code)
	}
	if _, err = s.Call(t.Context(), "settings.update", json.RawMessage(`{"listen":"0.0.0.0:8088"}`)); err != nil {
		t.Fatal(err)
	}
	restarted, newPassword, err := initializeConfig(path)
	if err != nil || newPassword != "" || restarted.PasswordHash != c.PasswordHash || restarted.Listen != "0.0.0.0:8088" {
		t.Fatal("restart must preserve the password and saved listener", err)
	}
	w = NewWeb(NewService(m, restarted, path), restarted)
	login = request(w, "POST", "/api/v1/login", string(body), nil, "", "http://192.168.1.10:8088", "192.168.1.10:8088")
	if login.Code != 200 {
		t.Fatal("initial password cannot log in through a host IP", login.Code)
	}
	changed, _ := json.Marshal(map[string]string{"current": password, "password": "changed-password-123"})
	if _, err = w.api.Call(t.Context(), "password", changed); err != nil {
		t.Fatal(err)
	}
	restarted, newPassword, err = initializeConfig(path)
	if err != nil || newPassword != "" || bcrypt.CompareHashAndPassword([]byte(restarted.PasswordHash), []byte("changed-password-123")) != nil {
		t.Fatal("restart must preserve a changed password", err)
	}
}

func TestInitializationDoesNotOverwriteInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	original := []byte(`{"listen":"0.0.0.0:8088","password_hash":"invalid"}`)
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, password, err := initializeConfig(path); err == nil || password != "" {
		t.Fatal("invalid existing config must not silently reset credentials")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(original) {
		t.Fatal("existing config changed", err)
	}
}
