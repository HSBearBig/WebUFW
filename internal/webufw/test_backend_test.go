package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type testBackend struct {
	mu    sync.Mutex
	state Snapshot
	logs  []string
}

func newTestBackend() *testBackend {
	d := &testBackend{state: Snapshot{Enabled: true, IPv6: true, Rules: []Rule{}, Containers: []Container{
		{ID: "test-nginx", Name: "nginx", Image: "nginx:stable", Running: true, Networks: []Network{{Name: "frontend", IPv4: "172.18.0.4", IPv6: "fd00:18::4", Driver: "bridge", Supported: true}}, Ports: []Port{{Port: "80", Protocol: "tcp", Bindings: []string{"0.0.0.0:8080 → 80/tcp", "[::]:8080 → 80/tcp"}}}},
		{ID: "test-api", Name: "api", Image: "webufw/example-api:1", Running: true, Networks: []Network{{Name: "backend", IPv4: "172.19.0.3", Driver: "bridge", Supported: true}}, Ports: []Port{{Port: "8080", Protocol: "tcp", Bindings: []string{"0.0.0.0:3000 → 8080/tcp"}}}},
		{ID: "test-dns", Name: "dns", Image: "webufw/example-dns:1", Running: true, Networks: []Network{{Name: "frontend", IPv4: "172.18.0.5", Driver: "bridge", Supported: true}}, Ports: []Port{{Port: "53", Protocol: "udp", Bindings: []string{"0.0.0.0:5353 → 53/udp"}}}},
	}, Environment: Environment{UFW: true, Docker: true, Integration: true, DockerWritable: true, Warnings: []string{"ufw-docker 信任網段及其他廣泛放行可能讓額外來源通過。"}, Listen: "127.0.0.1:8088"}}}
	h, _ := hostRules(HostInput{Action: "allow", Direction: "in", Protocol: "tcp", Source: "192.168.1.0/24", Destination: "any", Port: "22", Comment: "區網 SSH"}, true)
	d.state.Rules = append(d.state.Rules, h...)
	h, _ = hostRules(HostInput{Action: "allow", Direction: "in", Protocol: "tcp", Source: "any", Destination: "any", Port: "443", Comment: "HTTPS"}, true)
	d.state.Rules = append(d.state.Rules, h...)
	for _, x := range []struct{ c, n, p, proto, src, dst string }{{"nginx", "frontend", "80", "tcp", "203.0.113.10", "172.18.0.2"}, {"api", "backend", "8080", "tcp", "0.0.0.0/0", "172.19.0.3"}, {"dns", "frontend", "53", "udp", "192.168.1.0/24", "172.18.0.5"}} {
		source := x.src
		if anyIP(source) {
			source = "any"
		}
		comment := fmt.Sprintf("allow %s %s/%s %s", x.c, x.p, x.proto, x.n)
		if source != "any" {
			comment += " from " + source
		}
		d.state.Rules = append(d.state.Rules, finalizeRule(Rule{Family: 4, Kind: "docker", Action: "allow", Direction: "in", Protocol: x.proto, Source: x.src, Destination: x.dst, Port: x.p, Comment: comment, Container: x.c, Network: x.n}))
	}
	d.logs = []string{"[UFW BLOCK] IN=eth0 SRC=198.51.100.42 DST=192.0.2.10 PROTO=TCP DPT=22", "[UFW DOCKER BLOCK] IN=eth0 OUT=br-frontend SRC=198.51.100.42 DST=172.18.0.4 PROTO=TCP DPT=80"}
	return d
}
func (d *testBackend) Lock(ctx context.Context) (func(), error) { d.mu.Lock(); return d.mu.Unlock, nil }
func (d *testBackend) Snapshot(context.Context) (Snapshot, error) {
	b, _ := json.Marshal(d.state)
	var s Snapshot
	json.Unmarshal(b, &s)
	enrich(&s)
	s.Revision = digest([]any{s.Enabled, s.IPv6, s.Rules, s.Containers})
	return s, nil
}
func (d *testBackend) Execute(_ context.Context, step Step) error {
	if step.Tool == "docker" {
		return d.executeDocker(step.Args)
	}
	a := append([]string{}, step.Args...)
	if len(a) > 0 && a[0] == "--force" {
		a = a[1:]
	}
	if len(a) == 1 && (a[0] == "enable" || a[0] == "disable") {
		d.state.Enabled = a[0] == "enable"
		return nil
	}
	route := false
	if a[0] == "route" {
		route = true
		a = a[1:]
	}
	del := false
	position := 0
	if a[0] == "delete" {
		del = true
		a = a[1:]
	} else if a[0] == "insert" {
		fmt.Sscanf(a[1], "%d", &position)
		a = a[2:]
	}
	h := HostInput{Action: a[0], Direction: "in", Protocol: "any", Port: "any"}
	a = a[1:]
	if len(a) > 0 && (a[0] == "in" || a[0] == "out") {
		h.Direction = a[0]
		a = a[1:]
	}
	for i := 0; i+1 < len(a); i += 2 {
		switch a[i] {
		case "proto":
			h.Protocol = a[i+1]
		case "from":
			h.Source = a[i+1]
		case "to":
			h.Destination = a[i+1]
		case "port":
			h.Port = a[i+1]
		case "comment":
			h.Comment = a[i+1]
		default:
			return fmt.Errorf("unexpected test argument %s", a[i])
		}
	}
	rr, e := hostRules(h, d.state.IPv6)
	if e != nil {
		return e
	}
	for _, r := range rr {
		if route {
			r.Kind = "docker"
			m := dockerComment.FindStringSubmatch(r.Comment)
			if m == nil {
				return errors.New("invalid docker comment")
			}
			r.Container = m[1]
			r.Network = m[5]
			r = finalizeRule(r)
		}
		at := -1
		for i, x := range d.state.Rules {
			if x.ID == r.ID {
				at = i
			}
		}
		if del {
			if at >= 0 {
				d.state.Rules = append(d.state.Rules[:at], d.state.Rules[at+1:]...)
			}
			continue
		}
		if at >= 0 {
			continue
		}
		if position > 0 {
			idx := len(d.state.Rules)
			count := 0
			for i, x := range d.state.Rules {
				count++
				if count == position {
					idx = i
					break
				}
				_ = x
			}
			d.state.Rules = append(d.state.Rules, Rule{})
			copy(d.state.Rules[idx+1:], d.state.Rules[idx:])
			d.state.Rules[idx] = r
		} else {
			d.state.Rules = append(d.state.Rules, r)
		}
	}
	return nil
}
func (d *testBackend) executeDocker(a []string) error {
	del := a[0] == "delete"
	if del {
		a = a[1:]
	}
	in := DockerInput{Container: a[1], Port: strings.Split(a[2], "/")[0], Protocol: strings.Split(a[2], "/")[1], Network: a[3], Source: "any"}
	if len(a) > 4 {
		in.Source = a[5]
	}
	rules, e := dockerRules(d.state, in)
	if e != nil {
		return e
	}
	keep := []Rule{}
	for _, r := range d.state.Rules {
		same := r.Kind == "docker" && r.Container == in.Container && r.Network == in.Network && r.Port == in.Port && r.Protocol == in.Protocol && dockerSourceText(r) == in.Source
		if !same {
			keep = append(keep, r)
		}
	}
	if !del {
		keep = append(keep, rules...)
	}
	d.state.Rules = keep
	return nil
}
func (d *testBackend) Logs(context.Context, int) ([]string, error) { return d.logs, nil }
