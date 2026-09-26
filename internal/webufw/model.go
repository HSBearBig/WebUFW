package webufw

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var ErrConflict = errors.New("規則或容器已變更，請重新預覽")
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
var dockerComment = regexp.MustCompile(`^allow ([a-zA-Z0-9][a-zA-Z0-9_.-]*)(/v6)? ([0-9]+)/(tcp|udp)(?: ([a-zA-Z0-9][a-zA-Z0-9_.-]*))?(?: from ([^ ]+))?$`)

type Rule struct {
	ID          string `json:"id"`
	Family      int    `json:"family"`
	Position    int    `json:"position"`
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Direction   string `json:"direction"`
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Port        string `json:"port"`
	Comment     string `json:"comment"`
	Container   string `json:"container,omitempty"`
	Network     string `json:"network,omitempty"`
	ReadOnly    bool   `json:"read_only"`
	Raw         string `json:"raw,omitempty"`
	PendingSync bool   `json:"pending_sync"`
	Reason      string `json:"reason,omitempty"`
}
type Network struct {
	Name      string `json:"name"`
	IPv4      string `json:"ipv4"`
	IPv6      string `json:"ipv6"`
	Driver    string `json:"driver"`
	Supported bool   `json:"supported"`
}
type Port struct {
	Port     string   `json:"port"`
	Protocol string   `json:"protocol"`
	Bindings []string `json:"bindings"`
}
type Container struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Image    string    `json:"image"`
	Running  bool      `json:"running"`
	Networks []Network `json:"networks"`
	Ports    []Port    `json:"ports"`
	Reason   string    `json:"reason,omitempty"`
}
type Environment struct {
	UFW             bool     `json:"ufw"`
	Docker          bool     `json:"docker"`
	Integration     bool     `json:"integration"`
	DockerWritable  bool     `json:"docker_writable"`
	ScriptInstalled bool     `json:"script_installed"`
	ScriptSource    string   `json:"script_source,omitempty"`
	Warnings        []string `json:"warnings"`
	Listen          string   `json:"listen"`
}
type Snapshot struct {
	Revision    string      `json:"revision"`
	Enabled     bool        `json:"enabled"`
	IPv6        bool        `json:"ipv6"`
	Rules       []Rule      `json:"rules"`
	Containers  []Container `json:"containers"`
	Environment Environment `json:"environment"`
	Pending     *Pending    `json:"pending,omitempty"`
	CheckedAt   time.Time   `json:"checked_at"`
}
type HostInput struct {
	Action      string `json:"action"`
	Direction   string `json:"direction"`
	Protocol    string `json:"protocol"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Port        string `json:"port"`
	Comment     string `json:"comment"`
}
type DockerInput struct {
	Container string `json:"container"`
	Network   string `json:"network"`
	Port      string `json:"port"`
	Protocol  string `json:"protocol"`
	Source    string `json:"source"`
}
type Change struct {
	Kind     string      `json:"kind"`
	Revision string      `json:"revision"`
	RuleID   string      `json:"rule_id,omitempty"`
	Host     HostInput   `json:"host"`
	Docker   DockerInput `json:"docker"`
	Enabled  bool        `json:"enabled"`
}
type Step struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}
type Preview struct {
	ID        string    `json:"id"`
	Revision  string    `json:"revision"`
	Change    Change    `json:"change"`
	Added     []Rule    `json:"added"`
	Removed   []Rule    `json:"removed"`
	Commands  []Step    `json:"commands"`
	Warnings  []string  `json:"warnings"`
	Dangerous bool      `json:"dangerous"`
	ExpiresAt time.Time `json:"expires_at"`
}
type Pending struct {
	ID       string    `json:"id"`
	Deadline time.Time `json:"deadline"`
	State    string    `json:"state"`
	Message  string    `json:"message,omitempty"`
}
type Audit struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Detail string    `json:"detail"`
}

func token() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func digestBytes(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func digest(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}
func canonicalIP(s string) (string, error) {
	if s == "" || s == "any" {
		return "any", nil
	}
	if p, err := netip.ParsePrefix(s); err == nil && !p.Addr().Is4In6() {
		p = p.Masked()
		if p.Bits() == p.Addr().BitLen() {
			return p.Addr().String(), nil
		}
		return p.String(), nil
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" && !a.Is4In6() {
		return a.String(), nil
	}
	return "", fmt.Errorf("無效 IP/CIDR：%s", s)
}
func family(s string) int {
	if s == "any" || s == "" {
		return 0
	}
	if strings.Contains(s, ":") {
		return 6
	}
	return 4
}
func allIP(f int) string {
	if f == 6 {
		return "::/0"
	}
	return "0.0.0.0/0"
}
func anyIP(s string) bool { return s == "any" || s == "0.0.0.0/0" || s == "::/0" }
func validatePort(s string, ranges bool) error {
	if s == "any" && ranges {
		return nil
	}
	parts := strings.Split(s, ":")
	if len(parts) > 2 || (!ranges && len(parts) != 1) {
		return errors.New("連接埠格式不正確")
	}
	last := 0
	for _, p := range parts {
		if p == "" || strings.Trim(p, "0123456789") != "" || (len(p) > 1 && p[0] == '0') {
			return errors.New("連接埠必須為 1–65535")
		}
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 || n < last {
			return errors.New("連接埠範圍不正確")
		}
		last = n
	}
	return nil
}
func finalizeRule(r Rule) Rule {
	r.ID = digest([]any{r.Family, r.Kind, r.Action, r.Direction, r.Protocol, r.Source, r.Destination, r.Port, r.Comment, r.Raw})
	return r
}
func ruleArgs(r Rule) []string {
	a := []string{}
	if r.Kind == "docker" || r.Kind == "forward" {
		a = append(a, "route")
	}
	a = append(a, r.Action)
	if r.Kind == "host" {
		a = append(a, r.Direction)
	}
	if r.Protocol != "any" {
		a = append(a, "proto", r.Protocol)
	}
	a = append(a, "from", r.Source, "to", r.Destination)
	if r.Port != "any" {
		a = append(a, "port", r.Port)
	}
	if r.Comment != "" {
		a = append(a, "comment", r.Comment)
	}
	return a
}
func deleteArgs(r Rule) []string {
	a := ruleArgs(r)
	if a[0] == "route" {
		return append([]string{"--force", "route", "delete"}, a[1:]...)
	}
	return append([]string{"--force", "delete"}, a...)
}
func insertArgs(r Rule, position int) []string {
	a := ruleArgs(r)
	if a[0] == "route" {
		return append([]string{"route", "insert", strconv.Itoa(position)}, a[1:]...)
	}
	return append([]string{"insert", strconv.Itoa(position)}, a...)
}
func parseRules(data []byte, f int) []Rule {
	rules := []Rule{}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "### tuple ### ") {
			continue
		}
		r := Rule{Family: f, Position: len(rules) + 1, Kind: "host", ReadOnly: true, Raw: line, Reason: "複雜或無法辨識的既有規則"}
		body, comment, has := strings.Cut(strings.TrimPrefix(line, "### tuple ### "), " comment=")
		if has {
			b, e := hex.DecodeString(strings.TrimSpace(comment))
			if e != nil {
				rules = append(rules, finalizeRule(r))
				continue
			}
			r.Comment = string(b)
		}
		t := strings.Fields(body)
		if len(t) != 7 {
			rules = append(rules, finalizeRule(r))
			continue
		}
		r.Action = t[0]
		if strings.HasPrefix(r.Action, "route:") {
			r.Kind = "forward"
			r.Action = strings.TrimPrefix(r.Action, "route:")
		}
		r.Protocol = t[1]
		r.Port = t[2]
		r.Destination = t[3]
		r.Source = t[5]
		r.Direction = t[6]
		src, se := canonicalIP(r.Source)
		dst, de := canonicalIP(r.Destination)
		if se == nil && de == nil && t[4] == "any" && (r.Action == "allow" || r.Action == "deny" || r.Action == "reject") && (r.Protocol == "tcp" || r.Protocol == "udp" || r.Protocol == "any") && (r.Direction == "in" || r.Direction == "out") && validatePort(r.Port, true) == nil && (r.Protocol != "any" || r.Port == "any") {
			r.Source = src
			r.Destination = dst
			r.Raw = ""
			r.ReadOnly = r.Kind == "forward"
			r.Reason = ""
			if r.ReadOnly {
				r.Reason = "未辨識的轉送規則"
			}
			if m := dockerComment.FindStringSubmatch(r.Comment); m != nil && r.Kind == "forward" && r.Action == "allow" {
				r.Container = m[1]
				r.Network = m[5]
				r.Kind = "docker"
				source := m[6]
				if source == "" {
					source = "any"
				}
				source, e := canonicalIP(source)
				if e == nil && m[3] == r.Port && m[4] == r.Protocol && (m[2] != "") == (f == 6) && (source == r.Source || anyIP(source) && anyIP(r.Source)) && r.Network != "" {
					r.ReadOnly = false
					r.Reason = ""
				} else {
					r.ReadOnly = true
					r.Reason = "註解與實際規則不一致，或未指定網路"
				}
			}
		}
		rules = append(rules, finalizeRule(r))
	}
	return rules
}
func hostRules(h HostInput, ipv6 bool) ([]Rule, error) {
	if h.Action != "allow" && h.Action != "deny" && h.Action != "reject" {
		return nil, errors.New("無效動作")
	}
	if h.Direction != "in" && h.Direction != "out" {
		return nil, errors.New("無效方向")
	}
	if h.Protocol != "tcp" && h.Protocol != "udp" && h.Protocol != "any" {
		return nil, errors.New("無效協定")
	}
	if h.Port == "" {
		h.Port = "any"
	}
	if err := validatePort(h.Port, true); err != nil {
		return nil, err
	}
	if h.Port != "any" && h.Protocol == "any" {
		return nil, errors.New("指定連接埠時請選 TCP 或 UDP")
	}
	if len([]byte(h.Comment)) > 128 || strings.ContainsAny(h.Comment, "\n\r\x00") {
		return nil, errors.New("註解最多 128 位元組，且不可換行")
	}
	s, e := canonicalIP(h.Source)
	if e != nil {
		return nil, e
	}
	d, e := canonicalIP(h.Destination)
	if e != nil {
		return nil, e
	}
	if family(s) != 0 && family(d) != 0 && family(s) != family(d) {
		return nil, errors.New("來源與目的 IP 版本不一致")
	}
	fs := []int{4}
	if ipv6 {
		fs = append(fs, 6)
	}
	f := family(s)
	if f == 0 {
		f = family(d)
	}
	if f != 0 {
		if f == 6 && !ipv6 {
			return nil, errors.New("UFW 未啟用 IPv6")
		}
		fs = []int{f}
	}
	out := []Rule{}
	for _, f := range fs {
		src, dst := s, d
		if src == "any" {
			src = allIP(f)
		}
		if dst == "any" {
			dst = allIP(f)
		}
		out = append(out, finalizeRule(Rule{Family: f, Kind: "host", Action: h.Action, Direction: h.Direction, Protocol: h.Protocol, Source: src, Destination: dst, Port: h.Port, Comment: h.Comment}))
	}
	return out, nil
}
