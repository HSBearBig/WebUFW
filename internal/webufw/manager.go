package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type journal struct {
	Pending  Pending  `json:"pending"`
	Before   Snapshot `json:"before"`
	After    Snapshot `json:"after"`
	Preview  Preview  `json:"preview"`
	InFlight bool     `json:"in_flight"`
}
type Manager struct {
	mu       sync.Mutex
	backend  Backend
	dir      string
	previews map[string]Preview
	current  *journal
	audit    []Audit
	ttl      time.Duration
}

func NewManager(b Backend, dir string) (*Manager, error) {
	m := &Manager{backend: b, dir: dir, previews: map[string]Preview{}, audit: []Audit{}, ttl: 60 * time.Second}
	if dir != "" {
		if e := os.MkdirAll(dir, 0700); e != nil {
			return nil, e
		}
		data, e := os.ReadFile(filepath.Join(dir, "pending.json"))
		if e == nil {
			var j journal
			if e = json.Unmarshal(data, &j); e != nil {
				return nil, e
			}
			m.current = &j
		} else if !os.IsNotExist(e) {
			return nil, e
		}
		data, e = os.ReadFile(filepath.Join(dir, "audit.json"))
		if e == nil {
			if e = json.Unmarshal(data, &m.audit); e != nil {
				return nil, e
			}
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	return m, nil
}
func (m *Manager) path(name string) string {
	if m.dir == "" {
		return ""
	}
	return filepath.Join(m.dir, name)
}
func (m *Manager) record(action, detail string) error {
	m.audit = append(m.audit, Audit{time.Now().UTC(), action, detail})
	if len(m.audit) > 1000 {
		m.audit = append([]Audit{}, m.audit[len(m.audit)-1000:]...)
	}
	return atomicJSON(m.path("audit.json"), m.audit, 0600)
}
func (m *Manager) save() error {
	if m.current == nil {
		if m.dir != "" {
			err := os.Remove(m.path("pending.json"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			d, e := os.Open(m.dir)
			if e != nil {
				return e
			}
			defer d.Close()
			return d.Sync()
		}
		return nil
	}
	return atomicJSON(m.path("pending.json"), m.current, 0600)
}
func (m *Manager) snapshot(ctx context.Context) (Snapshot, error) {
	s, e := m.backend.Snapshot(ctx)
	if e == nil && m.current != nil {
		p := m.current.Pending
		s.Pending = &p
	}
	return s, e
}
func (m *Manager) State(ctx context.Context) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer unlock()
	if reader, ok := m.backend.(interface {
		ReadSnapshot(context.Context) (Snapshot, error)
	}); ok {
		s, err := reader.ReadSnapshot(ctx)
		if err == nil && m.current != nil {
			p := m.current.Pending
			s.Pending = &p
		}
		return s, err
	}
	return m.snapshot(ctx)
}

func (m *Manager) Changes(ctx context.Context) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.backend.Lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	var hint string
	refresh := false
	if reader, ok := m.backend.(interface {
		ReadHint(context.Context) (string, bool, error)
	}); ok {
		hint, refresh, err = reader.ReadHint(ctx)
	} else {
		var state Snapshot
		state, err = m.backend.Snapshot(ctx)
		hint = state.Revision
	}
	if err != nil {
		return nil, err
	}
	var pending *Pending
	if m.current != nil {
		p := m.current.Pending
		pending = &p
	}
	return map[string]any{"version": digest([]any{hint, pending}), "refresh": refresh}, nil
}
func (m *Manager) Preview(ctx context.Context, c Change) (Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		return Preview{}, errors.New("請先確認或回復目前變更")
	}
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return Preview{}, e
	}
	defer unlock()
	s, e := m.snapshot(ctx)
	if e != nil {
		return Preview{}, e
	}
	p, e := Plan(s, c)
	if e == nil {
		for k, x := range m.previews {
			if time.Now().After(x.ExpiresAt) {
				delete(m.previews, k)
			}
		}
		if len(m.previews) >= 32 {
			return Preview{}, errors.New("預覽過多，請稍後再試")
		}
		m.previews[p.ID] = p
	}
	return p, e
}
func (m *Manager) Apply(ctx context.Context, id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		return Snapshot{}, errors.New("已有待處理變更")
	}
	p, ok := m.previews[id]
	if !ok || time.Now().After(p.ExpiresAt) {
		return Snapshot{}, errors.New("預覽已過期，請重新預覽")
	}
	delete(m.previews, id)
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer unlock()
	s, e := m.snapshot(ctx)
	if e != nil {
		return s, e
	}
	if s.Revision != p.Revision {
		return s, ErrConflict
	}
	m.current = &journal{Pending: Pending{ID: token(), Deadline: time.Now().Add(m.ttl), State: "applying"}, Before: s, After: s, Preview: p}
	if e = m.save(); e != nil {
		m.current = nil
		return s, e
	}
	if e = m.record("apply.start", p.Change.Kind); e != nil {
		m.current = nil
		_ = m.save()
		return s, e
	}
	for _, step := range p.Commands {
		m.current.InFlight = true
		if e = m.save(); e != nil {
			return s, e
		}
		cmdErr := m.backend.Execute(ctx, step)
		after, readErr := m.backend.Snapshot(ctx)
		if readErr != nil {
			m.current.Pending.State = "conflict"
			m.current.Pending.Message = "指令執行後無法讀取規則；保留回復紀錄，需人工核對"
			_ = m.save()
			return s, fmt.Errorf("%v; %w", cmdErr, readErr)
		}
		m.current.After = after
		m.current.InFlight = false
		if e = m.save(); e != nil {
			return after, e
		}
		if cmdErr != nil {
			_ = m.record("apply.failed", cmdErr.Error())
			rb := m.rollbackLocked(ctx)
			if rb != nil {
				return after, fmt.Errorf("%v；回復失敗：%w", cmdErr, rb)
			}
			return Snapshot{}, cmdErr
		}
	}
	after := m.current.After
	if !matchesPlan(s, after, p) {
		m.current.Pending.State = "conflict"
		m.current.Pending.Message = "實際變更超出預覽範圍，已停止後續寫入；請以 CLI 人工檢查"
		_ = m.save()
		_ = m.record("apply.unexpected", p.Change.Kind)
		return after, errors.New(m.current.Pending.Message)
	}
	if p.Dangerous {
		m.current.Pending.State = "pending"
		m.current.Pending.Deadline = time.Now().Add(m.ttl)
		if e = m.save(); e != nil {
			return after, e
		}
	} else {
		m.current = nil
		if e = m.save(); e != nil {
			return after, e
		}
	}
	_ = m.record("apply.done", p.Change.Kind)
	return m.snapshot(ctx)
}
func matchesPlan(before, after Snapshot, p Preview) bool {
	want := map[string]int{}
	for _, r := range before.Rules {
		want[r.ID]++
	}
	for _, r := range p.Removed {
		want[r.ID]--
	}
	for _, r := range p.Added {
		want[r.ID]++
	}
	for _, r := range after.Rules {
		want[r.ID]--
	}
	for _, n := range want {
		if n != 0 {
			return false
		}
	}
	enabled := before.Enabled
	if p.Change.Kind == "ufw.toggle" {
		enabled = p.Change.Enabled
	}
	return enabled == after.Enabled
}
func (m *Manager) Confirm(ctx context.Context, id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.Pending.ID != id {
		return Snapshot{}, errors.New("找不到待確認變更")
	}
	if m.current.Pending.State != "pending" {
		return Snapshot{}, errors.New("目前狀態需要人工處理")
	}
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer unlock()
	if time.Now().After(m.current.Pending.Deadline) {
		e = m.rollbackLocked(ctx)
		if e != nil {
			return Snapshot{}, e
		}
		return Snapshot{}, errors.New("確認已逾時，變更已回復")
	}
	s, e := m.backend.Snapshot(ctx)
	if e != nil {
		return s, e
	}
	if s.Revision != m.current.After.Revision {
		return s, m.conflict("CLI 或容器已修改，無法確認原預覽")
	}
	if e = m.record("confirm", id); e != nil {
		return s, e
	}
	m.current = nil
	if e = m.save(); e != nil {
		return s, e
	}
	return s, nil
}
func (m *Manager) conflict(msg string) error {
	m.current.Pending.State = "conflict"
	m.current.Pending.Message = msg
	_ = m.save()
	_ = m.record("rollback.conflict", msg)
	return fmt.Errorf("%w：%s", ErrConflict, msg)
}
func (m *Manager) Rollback(ctx context.Context, id string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.Pending.ID != id {
		return Snapshot{}, errors.New("找不到待處理變更")
	}
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return Snapshot{}, e
	}
	defer unlock()
	if e = m.rollbackLocked(ctx); e != nil {
		return Snapshot{}, e
	}
	return m.snapshot(ctx)
}
func (m *Manager) rollbackLocked(ctx context.Context) error {
	j := m.current
	if j == nil {
		return nil
	}
	if j.Pending.State == "conflict" {
		return errors.New(j.Pending.Message)
	}
	if j.InFlight {
		return m.conflict("前次程序在指令執行期間中止，需人工核對；不會覆蓋既有設定")
	}
	s, e := m.backend.Snapshot(ctx)
	if e != nil {
		return e
	}
	if s.Revision != j.After.Revision {
		return m.conflict("外部狀態已變更，已停止自動回復以保留 CLI 操作")
	}
	beforeIDs := map[string]bool{}
	afterIDs := map[string]bool{}
	for _, r := range j.Before.Rules {
		beforeIDs[r.ID] = true
	}
	for _, r := range s.Rules {
		afterIDs[r.ID] = true
	}
	steps := []Step{}
	for _, r := range s.Rules {
		if !beforeIDs[r.ID] {
			if r.ReadOnly {
				return m.conflict("新增了無法安全回復的規則")
			}
			steps = append(steps, Step{"ufw", deleteArgs(r)})
		}
	}
	// Restore deleted rules in their original global positions. The lock excludes
	// UFW CLI writes for the entire inverse operation.
	virtual := s
	virtual.Rules = []Rule{}
	for _, r := range s.Rules {
		if beforeIDs[r.ID] {
			virtual.Rules = append(virtual.Rules, r)
		}
	}
	for _, r := range sortedRules(j.Before.Rules) {
		if !afterIDs[r.ID] {
			if r.ReadOnly {
				return m.conflict("刪除了無法安全回復的規則")
			}
			steps = append(steps, Step{"ufw", restoreArgs(virtual, r)})
			virtual.Rules = append(virtual.Rules, r)
		}
	}
	if s.Enabled != j.Before.Enabled {
		cmd := "disable"
		if j.Before.Enabled {
			cmd = "enable"
		}
		steps = append(steps, Step{"ufw", []string{"--force", cmd}})
	}
	for _, step := range steps {
		j.InFlight = true
		if e = m.save(); e != nil {
			return e
		}
		err := m.backend.Execute(ctx, step)
		state, se := m.backend.Snapshot(ctx)
		if se != nil {
			return m.conflict("回復後無法讀取規則")
		}
		j.After = state
		j.InFlight = false
		if e = m.save(); e != nil {
			return e
		}
		if err != nil {
			return m.conflict("回復指令失敗：" + err.Error())
		}
	}
	restored, e := m.backend.Snapshot(ctx)
	if e != nil {
		return e
	}
	if !sameRuleOrder(j.Before, restored) {
		return m.conflict("回復後規則順序與原先不同，需人工核對")
	}
	if e = m.record("rollback.done", j.Pending.ID); e != nil {
		return e
	}
	m.current = nil
	return m.save()
}
func sameRuleOrder(a, b Snapshot) bool {
	if a.Enabled != b.Enabled || len(a.Rules) != len(b.Rules) {
		return false
	}
	aa, bb := sortedRules(a.Rules), sortedRules(b.Rules)
	for i := range aa {
		if aa[i].ID != bb[i].ID {
			return false
		}
	}
	return true
}
func (m *Manager) Recover(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil {
		return nil
	}
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return e
	}
	defer unlock()
	return m.rollbackLocked(ctx)
}
func (m *Manager) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current == nil || m.current.Pending.State != "pending" || time.Now().Before(m.current.Pending.Deadline) {
		return
	}
	unlock, e := m.backend.Lock(ctx)
	if e != nil {
		return
	}
	defer unlock()
	_ = m.rollbackLocked(ctx)
}
func (m *Manager) Logs(ctx context.Context, n int) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n < 1 || n > 200 {
		n = 100
	}
	lines, e := m.backend.Logs(ctx, n)
	a := m.audit
	if len(a) > n {
		a = a[len(a)-n:]
	}
	return map[string]any{"firewall": lines, "audit": append([]Audit{}, a...)}, e
}

func (m *Manager) Record(action, detail string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.record(action, detail)
}
