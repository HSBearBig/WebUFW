//go:build linux

package webufw

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const scriptPath = "/var/lib/webufw/ufw-docker"
const sourcePath = "/var/lib/webufw/source.json"

// Keep existing installations usable, including pending-change recovery.
// New downloads are not granted write access until their CLI behavior is tested.
const legacyVerifiedSHA256 = "7422e36db2212423b65580e3586cc34a17256030c388ac667370a09bc1ea085a"

type sourceState struct {
	ID     string `json:"id"`
	Commit string `json:"commit"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
}
type sourceStage struct {
	sourceState
	Bytes   []byte
	Expires time.Time
}

var sourceNames = map[string]string{
	"hsbearbig": "HSBearBig/ufw-docker 最新版本",
	"chaifeng":  "chaifeng/ufw-docker 最新版本",
}

func sourceOptions() []map[string]string {
	return []map[string]string{
		{"id": "hsbearbig", "name": sourceNames["hsbearbig"], "url": "https://github.com/HSBearBig/ufw-docker", "support": "可獨立使用 CLI；WebUFW Docker 規則暫為唯讀"},
		{"id": "chaifeng", "name": sourceNames["chaifeng"], "url": "https://github.com/chaifeng/ufw-docker", "support": "原版沒有來源 IP 功能；WebUFW 規則寫入唯讀"},
	}
}

func installedSource() (sourceState, bool, bool) {
	return installedSourceAt(scriptPath, sourcePath)
}
func installedSourceAt(scriptPath, sourcePath string) (sourceState, bool, bool) {
	var state sourceState
	script, err := readBounded(scriptPath)
	if err != nil {
		return state, false, false
	}
	if digestBytes(script) == legacyVerifiedSHA256 {
		return sourceState{ID: "legacy-webufw", Commit: "既有已驗證版本", SHA256: digestBytes(script)}, true, true
	}
	if data, err := readBounded(sourcePath); err == nil && json.Unmarshal(data, &state) == nil && state.SHA256 == digestBytes(script) && sourceNames[state.ID] != "" {
		return state, true, false
	}
	return sourceState{}, true, false
}

var sourceHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if len(via) > 2 || r.URL.Scheme != "https" || (r.URL.Host != "api.github.com" && r.URL.Host != "raw.githubusercontent.com") {
		return errors.New("來源轉址不受支援")
	}
	return nil
}}

func fetchLimited(ctx context.Context, url string, limit int) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "WebUFW/0.1")
	response, err := sourceHTTP.Do(r)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("來源回應 HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit+1)))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, errors.New("下載內容超過大小限制")
	}
	return data, nil
}

func fetchSource(ctx context.Context, id string) (*sourceStage, error) {
	if sourceNames[id] == "" {
		return nil, errors.New("不支援的 ufw-docker 來源")
	}
	stage := &sourceStage{Expires: time.Now().Add(10 * time.Minute)}
	stage.ID = id
	owner := map[string]string{"hsbearbig": "HSBearBig", "chaifeng": "chaifeng"}[id]
	data, err := fetchLimited(ctx, "https://api.github.com/repos/"+owner+"/ufw-docker/commits?per_page=1", 1<<20)
	if err != nil {
		return nil, err
	}
	var commits []struct {
		SHA string `json:"sha"`
	}
	if err = json.Unmarshal(data, &commits); err != nil || len(commits) != 1 || len(commits[0].SHA) != 40 {
		return nil, errors.New("無法確認來源 commit")
	}
	for _, c := range commits[0].SHA {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return nil, errors.New("來源 commit 格式不正確")
		}
	}
	stage.Commit = commits[0].SHA
	stage.URL = "https://raw.githubusercontent.com/" + owner + "/ufw-docker/" + stage.Commit + "/ufw-docker"
	stage.Bytes, err = fetchLimited(ctx, stage.URL, 256<<10)
	if err != nil {
		return nil, err
	}
	if len(stage.Bytes) < 1000 || bytes.IndexByte(stage.Bytes, 0) >= 0 || !bytes.Contains(stage.Bytes, []byte("ufw-docker--allow")) {
		return nil, errors.New("下載內容不是預期的 ufw-docker 腳本")
	}
	stage.SHA256 = digestBytes(stage.Bytes)
	return stage, nil
}

func installSource(stage *sourceStage) (string, error) {
	return installSourceAt(scriptPath, sourcePath, stage)
}
func installSourceAt(scriptPath, sourcePath string, stage *sourceStage) (string, error) {
	if stage == nil || time.Now().After(stage.Expires) || stage.SHA256 != digestBytes(stage.Bytes) {
		return "", errors.New("下載預覽已過期，請重新取得")
	}
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0700); err != nil {
		return "", err
	}
	old, err := os.ReadFile(scriptPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	backup := ""
	if err == nil && !bytes.Equal(old, stage.Bytes) {
		backup = scriptPath + ".backup-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err = installFile(backup, old, 0600); err != nil {
			return "", err
		}
	}
	if err = installFile(scriptPath, stage.Bytes, 0755); err != nil {
		return "", err
	}
	if err = atomicJSON(sourcePath, stage.sourceState, 0600); err != nil {
		return "", err
	}
	return backup, nil
}
