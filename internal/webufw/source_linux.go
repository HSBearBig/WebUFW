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
const systemScriptPath = "/usr/local/bin/ufw-docker"
const systemScriptFallback = "/usr/bin/ufw-docker"

// Keep existing installations usable, including pending-change recovery.
// Unknown downloads remain read-only until their comment format is reviewed.
const legacyVerifiedSHA256 = "7422e36db2212423b65580e3586cc34a17256030c388ac667370a09bc1ea085a"

// The HSBearBig release and the locally installed LC_ALL export variant were
// inspected for the CLI comment format. WebUFW writes through locked UFW, not
// through the script, so its broad regex-based delete path is never invoked.
const verifiedHSBearSHA256 = "8089879e50bad72c850b9c4ac16ef58fe4d5df18d314998d65ab836e58b6b1e5"
const verifiedHSBearLocalSHA256 = "60a47fb76501cc5e848c45127e1517b63448a6fa68a50901b87eaad268f8aeae"

type sourceState struct {
	ID     string `json:"id"`
	Path   string `json:"path,omitempty"`
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
		{"id": "hsbearbig", "name": sourceNames["hsbearbig"], "url": "https://github.com/HSBearBig/ufw-docker", "support": "已驗證版本可由 WebUFW 管理 Docker 規則；其他版本僅供 CLI 使用"},
		{"id": "chaifeng", "name": sourceNames["chaifeng"], "url": "https://github.com/chaifeng/ufw-docker", "support": "原版沒有來源 IP 功能；WebUFW 規則寫入唯讀"},
	}
}

func compatibleSource(state sourceState) bool {
	return state.SHA256 == legacyVerifiedSHA256 ||
		(state.ID == "hsbearbig" && (state.SHA256 == verifiedHSBearSHA256 || state.SHA256 == verifiedHSBearLocalSHA256))
}

func identifySource(script []byte, path string) sourceState {
	hash := digestBytes(script)
	state := sourceState{ID: "unknown", Path: path, SHA256: hash, Commit: "版本未知"}
	switch hash {
	case legacyVerifiedSHA256:
		state.ID, state.Commit = "legacy-webufw", "既有已驗證版本"
	case verifiedHSBearSHA256, verifiedHSBearLocalSHA256:
		state.ID, state.Commit = "hsbearbig", "20ede95187ec8b989a29c6cc79134f960739250d"
	}
	return state
}
func installedSource() (sourceState, bool, bool) {
	return installedSourcePaths(scriptPath, sourcePath, []string{systemScriptPath, systemScriptFallback})
}
func installedSourceAt(path, metadata string) (sourceState, bool, bool) {
	return installedSourcePaths(path, metadata, nil)
}
func installedSourcePaths(path, metadata string, fallbacks []string) (sourceState, bool, bool) {
	script, err := readBounded(path)
	usedFallback := false
	if os.IsNotExist(err) {
		for _, candidate := range fallbacks {
			script, err = readBounded(candidate)
			if err == nil {
				path = candidate
				usedFallback = true
				break
			}
			if !os.IsNotExist(err) {
				break
			}
		}
	}
	if err != nil {
		return sourceState{}, false, false
	}
	state := identifySource(script, path)
	if usedFallback {
		// No private metadata applies to a system-wide installation.
		return state, true, compatibleSource(state)
	}
	var saved sourceState
	if data, e := readBounded(metadata); e == nil && json.Unmarshal(data, &saved) == nil && saved.SHA256 == state.SHA256 && sourceNames[saved.ID] != "" {
		state.ID, state.Commit, state.URL = saved.ID, saved.Commit, saved.URL
	}
	return state, true, compatibleSource(state)
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
