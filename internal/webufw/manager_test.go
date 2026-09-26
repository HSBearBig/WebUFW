package webufw

import (
	"context"
	"errors"
	"testing"
	"time"
)

type staleDisplayBackend struct {
	*testBackend
	display Snapshot
}

func (b *staleDisplayBackend) ReadSnapshot(context.Context) (Snapshot, error) {
	return b.display, nil
}

func TestCachedDisplayCannotAuthorizeStaleWrite(t *testing.T) {
	d := newTestBackend()
	before, _ := d.Snapshot(t.Context())
	b := &staleDisplayBackend{d, before}
	m := mustNewManager(t, b)
	p, err := m.Preview(t.Context(), Change{Kind: "host.delete", Revision: before.Revision, RuleID: before.Rules[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Execute(t.Context(), Step{Tool: "ufw", Args: []string{"--force", "disable"}}); err != nil {
		t.Fatal(err)
	}
	shown, _ := m.State(t.Context())
	if shown.Revision != before.Revision {
		t.Fatal("fixture did not return stale display state")
	}
	if _, err = m.Apply(t.Context(), p.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("stale apply accepted", err)
	}
	if _, err = m.Preview(t.Context(), Change{Kind: "host.delete", Revision: shown.Revision, RuleID: before.Rules[0].ID}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale preview accepted", err)
	}
}

func setupManager(t *testing.T) (*Manager, *testBackend) {
	t.Helper()
	d := newTestBackend()
	m, e := NewManager(d, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	m.ttl = time.Millisecond
	return m, d
}
func planChange(t *testing.T, m *Manager, c Change) Preview {
	t.Helper()
	s, e := m.State(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	c.Revision = s.Revision
	p, e := m.Preview(t.Context(), c)
	if e != nil {
		t.Fatal(e)
	}
	return p
}
func TestExpiryRollbackAndRestart(t *testing.T) {
	m, d := setupManager(t)
	before, _ := m.State(t.Context())
	r := before.Rules[0]
	p := planChange(t, m, Change{Kind: "host.delete", RuleID: r.ID})
	s, e := m.Apply(t.Context(), p.ID)
	if e != nil || s.Pending == nil {
		t.Fatal(s, e)
	}
	if len(s.Rules) != len(before.Rules)-1 {
		t.Fatal("rule not removed")
	}
	restarted, e := NewManager(d, m.dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	after, _ := restarted.State(t.Context())
	if after.Pending != nil || !sameRuleOrder(before, after) {
		t.Fatalf("bad recovery %+v", after)
	}
	p = planChange(t, mustNewManager(t, d), Change{Kind: "host.delete", RuleID: r.ID})
	_ = p
	m2 := mustNewManager(t, d)
	p = planChange(t, m2, Change{Kind: "ufw.toggle", Enabled: false})
	s, e = m2.Apply(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	m2.mu.Lock()
	m2.current.Pending.Deadline = time.Now().Add(-time.Second)
	m2.mu.Unlock()
	m2.Tick(t.Context())
	after, _ = m2.State(t.Context())
	if !after.Enabled || after.Pending != nil {
		t.Fatal("timeout did not restore")
	}
}
func mustNewManager(t *testing.T, d Backend) *Manager {
	t.Helper()
	m, e := NewManager(d, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func TestCLIConflict(t *testing.T) {
	m, d := setupManager(t)
	s, _ := m.State(t.Context())
	p := planChange(t, m, Change{Kind: "host.delete", RuleID: s.Rules[0].ID})
	d.state.Enabled = false
	if _, e := m.Apply(t.Context(), p.ID); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	d.state.Enabled = true
	p = planChange(t, m, Change{Kind: "host.delete", RuleID: s.Rules[0].ID})
	_, e := m.Apply(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	d.state.Enabled = false
	if e = m.Recover(t.Context()); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	if d.state.Enabled {
		t.Fatal("overwrote CLI change")
	}
}
func TestDockerSyncAndNarrowRollback(t *testing.T) {
	m, _ := setupManager(t)
	before, _ := m.State(t.Context())
	var r Rule
	for _, x := range before.Rules {
		if x.Container == "nginx" {
			r = x
		}
	}
	p := planChange(t, m, Change{Kind: "docker.sync", RuleID: r.ID})
	after, e := m.Apply(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range after.Rules {
		if x.Container == "nginx" && (x.Destination != "172.18.0.4" || x.Source != "203.0.113.10") {
			t.Fatal(x)
		}
	}
	if e = m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	restored, _ := m.State(t.Context())
	if !sameRuleOrder(before, restored) {
		t.Fatal("sync rollback changed rule order")
	}
	p = planChange(t, m, Change{Kind: "docker.narrow", Docker: DockerInput{Container: "api", Network: "backend", Port: "8080", Protocol: "tcp", Source: "203.0.113.20"}})
	_, e = m.Apply(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	restored, _ = m.State(t.Context())
	if !sameRuleOrder(before, restored) {
		t.Fatal("narrow rollback changed rules")
	}
}

type failingBackend struct {
	*testBackend
	calls int
	fail  int
}

func (b *failingBackend) Execute(ctx context.Context, s Step) error {
	b.calls++
	if b.calls == b.fail {
		return errors.New("injected command failure")
	}
	return b.testBackend.Execute(ctx, s)
}
func TestPartialFailureRollback(t *testing.T) {
	d := newTestBackend()
	b := &failingBackend{testBackend: d, fail: 2}
	m := mustNewManager(t, b)
	before, _ := m.State(t.Context())
	p := planChange(t, m, Change{Kind: "host.add", Host: HostInput{Action: "deny", Direction: "in", Protocol: "tcp", Source: "any", Destination: "any", Port: "1234"}})
	_, e := m.Apply(t.Context(), p.ID)
	if e == nil {
		t.Fatal("expected failure")
	}
	after, _ := m.State(t.Context())
	if after.Pending != nil || !sameRuleOrder(before, after) {
		t.Fatal("partial write not rolled back")
	}
}
func TestConfirmedRulesSurviveStop(t *testing.T) {
	m, _ := setupManager(t)
	m.ttl = time.Minute
	before, _ := m.State(t.Context())
	p := planChange(t, m, Change{Kind: "host.delete", RuleID: before.Rules[0].ID})
	s, e := m.Apply(t.Context(), p.ID)
	if e != nil {
		t.Fatal(e)
	}
	s, e = m.Confirm(t.Context(), s.Pending.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Recover(t.Context()); e != nil {
		t.Fatal(e)
	}
	if len(s.Rules) != len(before.Rules)-1 || s.Pending != nil {
		t.Fatal("confirmed state reverted")
	}
}
func TestInFlightRestartRequiresReview(t *testing.T) {
	m, d := setupManager(t)
	s, _ := m.State(t.Context())
	m.current = &journal{Pending: Pending{ID: token(), State: "applying"}, Before: s, After: s, InFlight: true}
	if e := m.save(); e != nil {
		t.Fatal(e)
	}
	restarted, e := NewManager(d, m.dir)
	if e != nil {
		t.Fatal(e)
	}
	if e = restarted.Recover(t.Context()); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
	after, _ := restarted.State(t.Context())
	if after.Pending.State != "conflict" || !sameRuleOrder(s, after) {
		t.Fatal(after)
	}
}
