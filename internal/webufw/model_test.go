package webufw

import (
	"encoding/hex"
	"strings"
	"testing"
)

func tuple(body, comment string) string {
	return "### tuple ### " + body + " comment=" + hex.EncodeToString([]byte(comment)) + "\n"
}
func TestParseRules(t *testing.T) {
	data := tuple("route:allow tcp 80 172.18.0.2 any 203.0.113.10 in", "allow web 80/tcp frontend from 203.0.113.10") + tuple("allow tcp 22 0.0.0.0/0 any 192.168.1.0/24 in", "SSH 管理") + tuple("allow tcp 443 0.0.0.0/0 123 0.0.0.0/0 in_eth0", "complex")
	rr := parseRules([]byte(data), 4)
	if len(rr) != 3 {
		t.Fatal(rr)
	}
	r := rr[0]
	if r.ReadOnly || r.Kind != "docker" || r.Port != "80" || r.Destination != "172.18.0.2" || r.Source != "203.0.113.10" {
		t.Fatalf("bad docker rule %+v", r)
	}
	if rr[1].Comment != "SSH 管理" || rr[1].ReadOnly {
		t.Fatal(rr[1])
	}
	if !rr[2].ReadOnly {
		t.Fatal("complex rule must be readonly")
	}
	bad := parseRules([]byte(tuple("route:allow tcp 80 172.18.0.2 any 0.0.0.0/0 in", "allow web 80/tcp frontend from 203.0.113.10")), 4)
	if !bad[0].ReadOnly {
		t.Fatal("mismatched comment is writable")
	}
	r6 := parseRules([]byte(tuple("route:allow udp 53 fd00::2 any 2001:db8::1 in", "allow dns/v6 53/udp frontend from 2001:db8::1")), 6)
	if r6[0].ReadOnly || r6[0].Family != 6 {
		t.Fatal(r6)
	}
}
func TestInputValidation(t *testing.T) {
	for _, in := range []string{"127.0.0.1;id", "--force", "1.2.3.999", "1.2.3.4/33", "fe80::1%eth0", "::ffff:192.0.2.1"} {
		if _, e := canonicalIP(in); e == nil {
			t.Errorf("accepted %q", in)
		}
	}
	for _, p := range []string{"0", "65536", "-1", "80;reboot", "90:80", "01x", "080", "80:090", "80:90:100"} {
		if validatePort(p, true) == nil {
			t.Errorf("accepted port %q", p)
		}
	}
	h := HostInput{Action: "allow", Direction: "in", Protocol: "tcp", Port: "80", Source: "192.0.2.1", Destination: "::1"}
	if _, e := hostRules(h, true); e == nil {
		t.Fatal("mixed families accepted")
	}
	h.Source = "any"
	h.Destination = "any"
	rr, e := hostRules(h, true)
	if e != nil || len(rr) != 2 {
		t.Fatal(rr, e)
	}
	for _, r := range rr {
		args := strings.Join(deleteArgs(r), " ")
		if !strings.Contains(args, "from ") || strings.Contains(args, "delete 1") {
			t.Fatal(args)
		}
	}
}
func TestDockerPlanning(t *testing.T) {
	d := newTestBackend()
	s, _ := d.Snapshot(t.Context())
	in := DockerInput{Container: "nginx", Network: "frontend", Port: "80", Protocol: "tcp", Source: "203.0.113.9"}
	rr, e := dockerRules(s, in)
	if e != nil || len(rr) != 1 || rr[0].Destination != "172.18.0.4" {
		t.Fatal(rr, e)
	}
	in.Source = "2001:db8::1"
	rr, e = dockerRules(s, in)
	if e != nil || len(rr) != 1 || rr[0].Family != 6 {
		t.Fatal(rr, e)
	}
	in = DockerInput{Container: "api", Network: "backend", Port: "8080", Protocol: "tcp", Source: "203.0.113.10"}
	p, e := Plan(s, Change{Kind: "docker.add", Revision: s.Revision, Docker: in})
	if e != nil || len(p.Removed) != 0 {
		t.Fatal(p, e)
	}
	if len(p.Commands) != 1 || p.Commands[0].Tool != "ufw" || p.Commands[0].Args[0] != "route" {
		t.Fatal("Docker add must use locked UFW", p.Commands)
	}
	p, e = Plan(s, Change{Kind: "docker.narrow", Revision: s.Revision, Docker: in})
	if e != nil || len(p.Removed) != 1 || !anyIP(p.Removed[0].Source) || !p.Dangerous {
		t.Fatal(p, e)
	}
	if len(p.Commands) != 2 || p.Commands[0].Tool != "ufw" || p.Commands[1].Tool != "ufw" || p.Commands[0].Args[1] != "route" {
		t.Fatal("Docker narrow must delete broad rule then add scoped rule", p.Commands)
	}
	in.Port = "3000"
	if _, e = dockerRules(s, in); e == nil {
		t.Fatal("accepted host published port as container port")
	}
}

func TestDockerFirewallBackendVersions(t *testing.T) {
	for _, c := range []struct {
		raw, want string
		ok        bool
	}{{"", "", true}, {`null`, "", true}, {`"iptables"`, "iptables", true}, {`{"Driver":"iptables"}`, "iptables", true}, {`{"Driver":"nftables"}`, "nftables", true}, {`{"Driver":"unknown"}`, "unknown", false}, {`[]`, "", false}} {
		name, ok := dockerFirewallBackend([]byte(c.raw))
		if name != c.want || ok != c.ok {
			t.Errorf("%s: %q %v", c.raw, name, ok)
		}
	}
}

func TestRestoreLastFamilyRuleAppends(t *testing.T) {
	s := Snapshot{Rules: []Rule{{Family: 4, Position: 1}, {Family: 6, Position: 1}}}
	for _, family := range []int{4, 6} {
		r := Rule{Family: family, Position: 2, Kind: "host", Action: "allow", Direction: "in", Protocol: "tcp", Source: allIP(family), Destination: allIP(family), Port: "80"}
		if got := restoreArgs(s, r); got[0] == "insert" {
			t.Fatal("last rule should append", got)
		}
		r.Position = 1
		got := restoreArgs(s, r)
		if got[0] != "insert" {
			t.Fatal(got)
		}
		want := "1"
		if family == 6 {
			want = "2"
		}
		if got[1] != want {
			t.Fatal(got)
		}
	}
}
