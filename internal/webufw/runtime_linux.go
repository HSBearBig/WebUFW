//go:build linux

package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"
)

const socketPath = "/run/webufw/agent.sock"

type RPCRequest struct {
	Operation string          `json:"operation"`
	Data      json.RawMessage `json:"data"`
}
type RPCResponse struct {
	Data     json.RawMessage `json:"data"`
	Error    string          `json:"error,omitempty"`
	Conflict bool            `json:"conflict,omitempty"`
}
type Remote struct{ client *http.Client }

func NewRemote(path string) *Remote {
	return &Remote{client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}, MaxConnsPerHost: 8}, Timeout: 50 * time.Second}}
}
func (r *Remote) Call(ctx context.Context, op string, data json.RawMessage) (any, error) {
	b, _ := json.Marshal(RPCRequest{op, data})
	req, e := http.NewRequestWithContext(ctx, "POST", "http://unix/rpc", bytesReader(b))
	if e != nil {
		return nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := r.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer res.Body.Close()
	var reply RPCResponse
	if e = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&reply); e != nil {
		return nil, e
	}
	if reply.Error != "" {
		if reply.Conflict {
			return nil, fmt.Errorf("%w：%s", ErrConflict, reply.Error)
		}
		return nil, errors.New(reply.Error)
	}
	var out any
	e = json.Unmarshal(reply.Data, &out)
	return out, e
}
func bytesReader(b []byte) io.Reader { return &byteReader{b: b} }

type byteReader struct{ b []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
func rpcHandler(s *Service) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		b, e := io.ReadAll(r.Body)
		var req RPCRequest
		if e == nil {
			e = decodeStrict(b, &req)
		}
		var v any
		if e == nil {
			v, e = s.Call(r.Context(), req.Operation, req.Data)
		}
		reply := RPCResponse{}
		if e != nil {
			reply.Error = e.Error()
			reply.Conflict = errors.Is(e, ErrConflict)
		} else {
			reply.Data, _ = json.Marshal(v)
		}
		writeJSON(w, 200, reply)
	})
}
func server(handler http.Handler) *http.Server {
	return &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 120 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
}
func contextSignals() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func timer(ctx context.Context, m *Manager) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			m.Tick(tick)
			cancel()
		}
	}
}
func Run() error {
	if e := ensureRoot(); e != nil {
		return e
	}
	account, e := user.Lookup("webufw")
	if e != nil {
		account, e = user.Lookup("nobody")
	}
	if e != nil {
		return fmt.Errorf("找不到可用的一般使用者帳號：%w", e)
	}
	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)
	if uid == 0 || gid == 0 {
		return errors.New("webufw 服務帳號不可為 root")
	}
	if e = os.MkdirAll("/run/webufw", 0750); e != nil {
		return e
	}
	if e = os.Chown("/run/webufw", 0, gid); e != nil {
		return e
	}
	if e = os.Chmod("/run/webufw", 0750); e != nil {
		return e
	}
	instance, e := os.OpenFile("/run/webufw/instance.lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer instance.Close()
	if e = syscall.Flock(int(instance.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("WebUFW 已在執行")
	}
	c, password, e := initializeConfig(configLocation())
	if e != nil {
		return e
	}
	if password != "" {
		log.Printf("WebUFW 初始密碼：admin / %s（登入後可於設定頁修改密碼）", password)
	}
	_ = os.Remove(socketPath)
	ln, e := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if e != nil {
		return e
	}
	defer ln.Close()
	if e = os.Chown(socketPath, 0, gid); e != nil {
		return e
	}
	if e = os.Chmod(socketPath, 0660); e != nil {
		return e
	}
	defer os.Remove(socketPath)
	b, e := NewRealBackend()
	if e != nil {
		return e
	}
	m, e := NewManager(b, "/var/lib/webufw")
	if e != nil {
		return e
	}
	recoverCtx, cancelRecover := context.WithTimeout(context.Background(), 90*time.Second)
	if e = m.Recover(recoverCtx); e != nil {
		log.Printf("recovery: %v", e)
	}
	cancelRecover()
	s := NewService(m, c, configLocation())
	srv := server(rpcHandler(s))
	peerListener := &checkedListener{UnixListener: ln, uid: uint32(uid)}
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(peerListener) }()
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	child := exec.Command(exe)
	child.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GOMEMLIMIT=24MiB", "GOGC=80", "WEBUFW_PROCESS=web", "WEBUFW_LISTEN=" + c.Listen}
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}, Pdeathsig: syscall.SIGTERM}
	if e = child.Start(); e != nil {
		srv.Close()
		return e
	}
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	ctx, cancel := contextSignals()
	defer cancel()
	go timer(ctx, m)
	log.Printf("WebUFW: root agent PID %d, web PID %d (uid %d), http://%s", os.Getpid(), child.Process.Pid, uid, c.Listen)
	childExited := false
	select {
	case <-ctx.Done():
	case e = <-childDone:
		childExited = true
		if e == nil {
			e = errors.New("web 子程序已結束")
		}
	case e = <-serveDone:
	}
	cancel()
	stop, stopCancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer stopCancel()
	if !childExited {
		_ = child.Process.Signal(syscall.SIGTERM)
		select {
		case <-childDone:
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
		}
	}
	_ = srv.Shutdown(stop)
	if err := m.Recover(stop); err != nil {
		log.Printf("shutdown recovery: %v", err)
	}
	if errors.Is(e, http.ErrServerClosed) {
		e = nil
	}
	return e
}

type checkedListener struct {
	*net.UnixListener
	uid uint32
}

func (l *checkedListener) Accept() (net.Conn, error) {
	for {
		c, e := l.AcceptUnix()
		if e != nil {
			return nil, e
		}
		raw, e := c.SyscallConn()
		allowed := false
		if e == nil {
			_ = raw.Control(func(fd uintptr) {
				cred, err := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
				allowed = err == nil && (cred.Uid == l.uid || cred.Uid == 0)
			})
		}
		if allowed {
			return c, nil
		}
		c.Close()
	}
}
func ServeChild() error {
	if os.Geteuid() == 0 {
		return errors.New("web 子程序不得使用 root")
	}
	c := Config{Listen: os.Getenv("WEBUFW_LISTEN")}
	if e := c.Validate(); e != nil {
		return e
	}
	w := NewWeb(NewRemote(socketPath), c)
	srv := server(w)
	ln, e := net.Listen("tcp", c.Listen)
	if e != nil {
		return e
	}
	defer ln.Close()
	log.Printf("WebUFW ready: %s；監聽 %s", Version, c.Listen)
	ctx, cancel := contextSignals()
	defer cancel()
	go func() {
		<-ctx.Done()
		stop, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		srv.Shutdown(stop)
	}()
	e = srv.Serve(ln)
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
