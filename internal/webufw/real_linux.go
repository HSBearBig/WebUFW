//go:build linux

package webufw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type RealBackend struct {
	mu         sync.Mutex
	lock       *os.File
	executable string
	runtime    runtimeCheck
}

// Cached only for read-only dashboard requests. Mutations always call Snapshot.
type runtimeCheck struct {
	key                          string
	at                           time.Time
	enabled, ufw, dockerWritable bool
	warnings                     []string
}

func NewRealBackend() (*RealBackend, error) {
	exe, e := os.Executable()
	return &RealBackend{executable: exe}, e
}
func (b *RealBackend) Lock(ctx context.Context) (func(), error) {
	b.mu.Lock()
	f, e := os.OpenFile("/run/ufw.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		b.mu.Unlock()
		return nil, e
	}
	fl := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	for {
		e = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl)
		if e == nil {
			break
		}
		if e != syscall.EAGAIN && e != syscall.EACCES {
			f.Close()
			b.mu.Unlock()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			b.mu.Unlock()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	b.lock = f
	return func() {
		b.lock = nil
		fl.Type = syscall.F_UNLCK
		_ = syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &fl)
		f.Close()
		b.mu.Unlock()
	}, nil
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		w.Buffer.Write(p)
	}
	return n, nil
}
func runCommand(ctx context.Context, path string, args []string, env []string, files []*os.File) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append([]string{"PATH=/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C", "HOME=/root", "DOCKER_HOST=unix:///var/run/docker.sock"}, env...)
	cmd.ExtraFiles = files
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = time.Second
	out := &limitedOutput{limit: 2 << 20}
	errout := &limitedOutput{limit: 8192}
	cmd.Stdout = out
	cmd.Stderr = errout
	e := cmd.Run()
	if e != nil {
		return out.Bytes(), fmt.Errorf("%s: %w: %s", filepath.Base(path), e, strings.TrimSpace(errout.String()))
	}
	return out.Bytes(), nil
}
func (b *RealBackend) ufw(ctx context.Context, args []string) ([]byte, error) {
	if b.lock == nil {
		return nil, errors.New("UFW lock is not held")
	}
	return runCommand(ctx, b.executable, append([]string{"_ufw"}, args...), []string{"WEBUFW_LOCK_PID=" + strconv.Itoa(os.Getpid())}, []*os.File{b.lock})
}
func readBounded(path string) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 4<<20+1))
	if e == nil && len(b) > 4<<20 {
		return nil, errors.New("設定檔超過 4 MiB 上限")
	}
	return b, e
}
func (b *RealBackend) Snapshot(ctx context.Context) (Snapshot, error) {
	return b.snapshot(ctx, false)
}
func (b *RealBackend) ReadSnapshot(ctx context.Context) (Snapshot, error) {
	return b.snapshot(ctx, true)
}

// ReadHint is a refresh hint, never a rule revision or authorization token.
// Avoid parsing/serializing hundreds of unchanged rules on every browser poll.
func (b *RealBackend) ReadHint(ctx context.Context) (string, bool, error) {
	files := map[string]any{}
	for _, path := range []string{"/etc/ufw/user.rules", "/etc/ufw/user6.rules", "/etc/ufw/ufw.conf", "/etc/default/ufw", "/etc/ufw/before.rules", "/etc/ufw/before6.rules", "/etc/ufw/after.rules", "/etc/ufw/after6.rules", "/etc/docker/daemon.json", scriptPath, sourcePath, systemScriptPath, systemScriptFallback, "/var/run/docker.sock"} {
		info, err := os.Stat(path)
		if err != nil {
			files[path] = err.Error()
			continue
		}
		var inode uint64
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			inode = stat.Ino
		}
		files[path] = []any{info.ModTime().UnixNano(), info.Size(), info.Mode(), inode}
	}
	// Exclude Docker's human-readable Status ("Up N seconds"), which changes
	// despite no configuration change. All fields used for rules are retained.
	var containers []struct {
		ID, Image, State string
		Names            []string
		Labels           map[string]string
		HostConfig       struct{ NetworkMode string }
		NetworkSettings  struct {
			Networks map[string]struct{ IPAddress, GlobalIPv6Address, NetworkID string }
		}
		Ports []struct {
			IP, Type                string
			PrivatePort, PublicPort uint16
		}
	}
	err := dockerJSON(ctx, "/containers/json?all=1", &containers)
	errorText := ""
	if err != nil {
		errorText = err.Error()
	}
	// Docker may emit map-backed collections in a different order per request.
	// Such ordering changes must not trigger a full dashboard refresh.
	for i := range containers {
		c := &containers[i]
		sort.Strings(c.Names)
		sort.Slice(c.Ports, func(i, j int) bool {
			a, z := c.Ports[i], c.Ports[j]
			if a.PrivatePort != z.PrivatePort {
				return a.PrivatePort < z.PrivatePort
			}
			if a.PublicPort != z.PublicPort {
				return a.PublicPort < z.PublicPort
			}
			if a.Type != z.Type {
				return a.Type < z.Type
			}
			return a.IP < z.IP
		})
	}
	sort.Slice(containers, func(i, j int) bool { return containers[i].ID < containers[j].ID })
	return digest([]any{files, containers, errorText}), time.Since(b.runtime.at) >= 30*time.Second, nil
}
func (b *RealBackend) snapshot(ctx context.Context, allowCachedRuntime bool) (Snapshot, error) {
	s := Snapshot{Rules: []Rule{}, Containers: []Container{}, Environment: Environment{Warnings: []string{}}}
	contents := map[string]string{}
	for _, p := range []string{"/etc/ufw/user.rules", "/etc/ufw/user6.rules", "/etc/ufw/ufw.conf", "/etc/default/ufw", "/etc/ufw/before.rules", "/etc/ufw/before6.rules", "/etc/ufw/after.rules", "/etc/ufw/after6.rules"} {
		data, e := readBounded(p)
		if os.IsNotExist(e) {
			contents[p] = ""
			continue
		}
		if e != nil {
			return s, fmt.Errorf("讀取 %s: %w", p, e)
		}
		contents[p] = string(data)
	}
	s.Enabled = strings.Contains(contents["/etc/ufw/ufw.conf"], "ENABLED=yes")
	s.IPv6 = strings.Contains(contents["/etc/default/ufw"], "IPV6=yes")
	s.Rules = append(s.Rules, parseRules([]byte(contents["/etc/ufw/user.rules"]), 4)...)
	if s.IPv6 {
		s.Rules = append(s.Rules, parseRules([]byte(contents["/etc/ufw/user6.rules"]), 6)...)
	}
	_, e := os.Stat("/usr/sbin/ufw")
	s.Environment.UFW = e == nil
	if !s.Environment.UFW {
		s.Environment.Warnings = append(s.Environment.Warnings, "尚未安裝 UFW；請在環境檢查頁查看安裝方式。")
	}
	after := contents["/etc/ufw/after.rules"]
	s.Environment.Integration = strings.Contains(after, "# BEGIN UFW AND DOCKER") && strings.Contains(after, "-A DOCKER-USER -j ufw-user-forward")
	for _, line := range strings.Split(after+"\n"+contents["/etc/ufw/after6.rules"], "\n") {
		if strings.HasPrefix(line, "-A DOCKER-USER") && strings.Contains(line, " -j RETURN -s ") {
			s.Environment.Warnings = append(s.Environment.Warnings, "整合預設信任："+strings.TrimSpace(strings.Split(line, " -s ")[1]))
		}
	}
	source, installed, compatible := installedSource()
	script, _ := readBounded(source.Path)
	s.Environment.ScriptInstalled = installed
	s.Environment.ScriptSource = source.ID
	s.Environment.ScriptPath = source.Path
	containers, writable, warnings := inspectDocker(ctx)
	s.Containers = containers
	s.Environment.Docker = containers != nil
	s.Environment.Warnings = append(s.Environment.Warnings, warnings...)
	if s.Environment.Docker {
		if !s.Environment.Integration {
			s.Environment.Warnings = append(s.Environment.Warnings, "尚未設定 DOCKER-USER 整合；請在維護時段依部署文件設定。")
		}
		if !installed {
			s.Environment.Warnings = append(s.Environment.Warnings, "尚未偵測到 ufw-docker 腳本；若不使用 Docker，可略過。")
		}
		if installed && !compatible {
			s.Environment.Warnings = append(s.Environment.Warnings, "已偵測到 ufw-docker，但此版本尚未驗證相容性，Docker 規則僅供檢視。")
		}
	}
	s.Environment.DockerWritable = s.Environment.UFW && s.Environment.Docker && writable && compatible && s.Environment.Integration
	// Rule files and Docker containers are always read fresh. Expensive kernel
	// consistency checks can be reused briefly for display only; previews,
	// writes, confirmation and recovery always verify the kernel again.
	v6Container := false
	for _, c := range s.Containers {
		for _, n := range c.Networks {
			v6Container = v6Container || n.IPv6 != ""
		}
	}
	var socketIdentity any
	if info, err := os.Stat("/var/run/docker.sock"); err == nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			socketIdentity = []uint64{uint64(st.Dev), st.Ino}
		}
	}
	key := digest([]any{s.Enabled, s.IPv6, s.Environment.UFW, s.Environment.DockerWritable, v6Container, socketIdentity, contents["/etc/ufw/ufw.conf"], contents["/etc/default/ufw"], contents["/etc/ufw/before.rules"], contents["/etc/ufw/before6.rules"], contents["/etc/ufw/after.rules"], contents["/etc/ufw/after6.rules"]})
	if allowCachedRuntime && b.runtime.key == key && time.Since(b.runtime.at) < 30*time.Second {
		s.Enabled = b.runtime.enabled
		s.Environment.UFW = b.runtime.ufw
		s.Environment.DockerWritable = b.runtime.dockerWritable
		s.Environment.Warnings = append(s.Environment.Warnings, b.runtime.warnings...)
	} else {
		startWarnings := len(s.Environment.Warnings)
		b.checkRuntime(ctx, &s, contents)
		b.runtime = runtimeCheck{key: key, at: time.Now(), enabled: s.Enabled, ufw: s.Environment.UFW, dockerWritable: s.Environment.DockerWritable, warnings: append([]string{}, s.Environment.Warnings[startWarnings:]...)}
	}
	if s.Containers == nil {
		s.Containers = []Container{}
	}
	enrich(&s)
	seen := map[string]int{}
	for _, r := range s.Rules {
		seen[r.ID]++
	}
	for i := range s.Rules {
		if seen[s.Rules[i].ID] > 1 {
			s.Rules[i].ReadOnly = true
			s.Rules[i].Reason = "重複規則，請先以 CLI 整理"
		}
	}
	s.Revision = digest([]any{contents, s.Enabled, s.Environment.UFW, s.Containers, s.Environment.DockerWritable, digest(script)})
	return s, nil
}
func (b *RealBackend) checkRuntime(ctx context.Context, s *Snapshot, contents map[string]string) {
	if s.Environment.UFW {
		status, statusErr := b.ufw(ctx, []string{"status"})
		if statusErr != nil {
			s.Environment.UFW = false
			s.Environment.Warnings = append(s.Environment.Warnings, "無法讀取執行中的 UFW 狀態："+statusErr.Error())
		} else {
			running := bytes.Contains(status, []byte("Status: active"))
			if running != s.Enabled {
				s.Environment.UFW = false
				s.Environment.Warnings = append(s.Environment.Warnings, "UFW 開機設定與執行狀態不同，請先以 CLI 檢查並重新載入。")
			}
			s.Enabled = running
		}
	}
	if s.Environment.DockerWritable {
		if rules, err := runCommand(ctx, "/usr/sbin/iptables", []string{"-S", "DOCKER-USER"}, nil, nil); err != nil || !bytes.Contains(rules, []byte("-A DOCKER-USER -j ufw-user-forward")) {
			s.Environment.DockerWritable = false
			s.Environment.Warnings = append(s.Environment.Warnings, "執行中的 DOCKER-USER 未連接 ufw-user-forward，需先載入整合規則。")
		}
		if s.IPv6 {
			after6 := contents["/etc/ufw/after6.rules"]
			v6Container := false
			for _, c := range s.Containers {
				for _, n := range c.Networks {
					v6Container = v6Container || n.IPv6 != ""
				}
			}
			if v6Container {
				rules6, err := runCommand(ctx, "/usr/sbin/ip6tables", []string{"-S", "DOCKER-USER"}, nil, nil)
				if err != nil || !bytes.Contains(rules6, []byte("-A DOCKER-USER -j ufw6-user-forward")) || !strings.Contains(after6, "-A DOCKER-USER -j ufw6-user-forward") {
					s.Environment.DockerWritable = false
					s.Environment.Warnings = append(s.Environment.Warnings, "雙棧容器缺少 IPv6 DOCKER-USER 整合，Docker 寫入已停用。")
				}
			}
		}
	}
}

func (b *RealBackend) Execute(ctx context.Context, s Step) error {
	if b.lock == nil {
		return errors.New("missing UFW lock")
	}
	switch s.Tool {
	case "ufw":
		_, e := b.ufw(ctx, s.Args)
		return e
	case "docker":
		script, e := readBounded(scriptPath)
		if e != nil || digestBytes(script) != legacyVerifiedSHA256 {
			return errors.New("ufw-docker 版本已變更")
		}
		_, e = runCommand(ctx, scriptPath, s.Args, []string{"WEBUFW_LOCK_PID=" + strconv.Itoa(os.Getpid()), "WEBUFW_EXECUTABLE=" + b.executable}, []*os.File{b.lock})
		return e
	default:
		return errors.New("unsupported command")
	}
}
func (b *RealBackend) Logs(ctx context.Context, n int) ([]string, error) {
	out, e := runCommand(ctx, "/usr/bin/journalctl", []string{"--kernel", "--no-pager", "--output=short-iso", "--grep=UFW", "--lines=" + strconv.Itoa(n)}, nil, nil)
	if e != nil {
		return nil, e
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

// LockedUFW is only a subprocess of the root agent. UFW uses POSIX record locks,
// which belong to a PID; the child verifies that the parent still owns the lock
// before skipping UFW's otherwise recursive acquisition. No shell is involved.
func LockedUFW(args []string) error {
	if os.Geteuid() != 0 {
		return errors.New("root required")
	}
	expected, e := strconv.Atoi(os.Getenv("WEBUFW_LOCK_PID"))
	if e != nil || expected <= 1 {
		return errors.New("missing lock owner")
	}
	var fdStat, pathStat syscall.Stat_t
	if e = syscall.Fstat(3, &fdStat); e != nil {
		return e
	}
	if e = syscall.Stat("/run/ufw.lock", &pathStat); e != nil {
		return e
	}
	if fdStat.Ino != pathStat.Ino || fdStat.Dev != pathStat.Dev {
		return errors.New("invalid lock descriptor")
	}
	fl := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if e = syscall.FcntlFlock(3, syscall.F_GETLK, &fl); e != nil {
		return e
	}
	if fl.Type != syscall.F_WRLCK || int(fl.Pid) != expected {
		return errors.New("agent does not own UFW lock")
	}
	const code = `import runpy,sys,ufw.util
ufw.util.create_lock = lambda *a, **kw: None
ufw.util.release_lock = lambda *a, **kw: None
sys.argv = ['/usr/sbin/ufw'] + sys.argv[1:]
runpy.run_path('/usr/sbin/ufw', run_name='__main__')`
	return syscall.Exec("/usr/bin/python3", append([]string{"python3", "-I", "-c", code}, args...), []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "LANG=C", "HOME=/root"})
}
