package webufw

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func dockerRules(s Snapshot, in DockerInput) ([]Rule, error) {
	if !s.Environment.DockerWritable {
		return nil, errors.New("目前 Docker 環境或 ufw-docker 整合未通過檢查，僅供檢視")
	}
	if !s.Enabled {
		return nil, errors.New("請先啟用 UFW，再操作 Docker 規則")
	}
	if !namePattern.MatchString(in.Container) || !namePattern.MatchString(in.Network) {
		return nil, errors.New("容器或網路名稱不正確")
	}
	if in.Protocol != "tcp" && in.Protocol != "udp" {
		return nil, errors.New("無效協定")
	}
	if e := validatePort(in.Port, false); e != nil {
		return nil, e
	}
	source, e := canonicalIP(in.Source)
	if e != nil {
		return nil, e
	}
	var c *Container
	for i := range s.Containers {
		if s.Containers[i].Name == in.Container {
			c = &s.Containers[i]
		}
	}
	if c == nil || !c.Running || c.Reason != "" {
		return nil, errors.New("容器未執行或使用不支援的模式")
	}
	published := false
	for _, p := range c.Ports {
		if p.Port == in.Port && p.Protocol == in.Protocol && len(p.Bindings) > 0 {
			published = true
		}
	}
	if !published {
		return nil, errors.New("請選擇已發布的容器服務埠")
	}
	var n *Network
	for i := range c.Networks {
		if c.Networks[i].Name == in.Network {
			n = &c.Networks[i]
		}
	}
	if n == nil || !n.Supported {
		return nil, errors.New("目前只支援一般 Docker bridge 網路")
	}
	out := []Rule{}
	for _, dst := range []string{n.IPv4, n.IPv6} {
		if dst == "" {
			continue
		}
		f := family(dst)
		if f == 6 && !s.IPv6 {
			continue
		}
		if family(source) != 0 && family(source) != f {
			continue
		}
		src := source
		if src == "any" {
			src = allIP(f)
		}
		name := c.Name
		if f == 6 {
			name += "/v6"
		}
		comment := fmt.Sprintf("allow %s %s/%s %s", name, in.Port, in.Protocol, in.Network)
		if source != "any" {
			comment += " from " + in.Source
		}
		out = append(out, finalizeRule(Rule{Family: f, Kind: "docker", Action: "allow", Direction: "in", Protocol: in.Protocol, Source: src, Destination: dst, Port: in.Port, Comment: comment, Container: c.Name, Network: in.Network}))
	}
	if len(out) == 0 {
		return nil, errors.New("找不到與來源 IP 版本相同的容器目的 IP")
	}
	return out, nil
}
func sameDocker(r Rule, in DockerInput) bool {
	return r.Kind == "docker" && r.Container == in.Container && r.Network == in.Network && r.Port == in.Port && r.Protocol == in.Protocol
}
func sourceMatch(r Rule, src string) bool { return r.Source == src || anyIP(r.Source) && anyIP(src) }
func dockerStep(in DockerInput, del bool) Step {
	src := in.Source
	if src == "" {
		src = "any"
	}
	a := []string{}
	if del {
		a = append(a, "delete")
	}
	a = append(a, "allow", in.Container, in.Port+"/"+in.Protocol, in.Network, "--source", src)
	return Step{Tool: "docker", Args: a}
}
func dockerSourceText(r Rule) string {
	if m := dockerComment.FindStringSubmatch(r.Comment); m != nil && m[6] != "" {
		return m[6]
	}
	return "any"
}
func inputFromRule(r Rule) DockerInput {
	return DockerInput{Container: r.Container, Network: r.Network, Port: r.Port, Protocol: r.Protocol, Source: dockerSourceText(r)}
}

func Plan(s Snapshot, c Change) (Preview, error) {
	p := Preview{ID: token(), Revision: s.Revision, Change: c, Added: []Rule{}, Removed: []Rule{}, Commands: []Step{}, Warnings: []string{}, ExpiresAt: time.Now().Add(2 * time.Minute)}
	if c.Revision != s.Revision {
		return p, ErrConflict
	}
	if !s.Environment.UFW {
		return p, errors.New("UFW 無法使用")
	}
	find := func() (Rule, error) {
		for _, r := range s.Rules {
			if r.ID == c.RuleID {
				if r.ReadOnly {
					return r, errors.New("這條既有規則僅供檢視")
				}
				return r, nil
			}
		}
		return Rule{}, ErrConflict
	}
	switch c.Kind {
	case "host.add", "host.edit":
		if c.Kind == "host.edit" {
			r, e := find()
			if e != nil {
				return p, e
			}
			if r.Kind != "host" {
				return p, errors.New("不是主機規則")
			}
			p.Removed = append(p.Removed, r)
			p.Commands = append(p.Commands, Step{"ufw", deleteArgs(r)})
			p.Dangerous = true
		}
		rr, e := hostRules(c.Host, s.IPv6)
		if e != nil {
			return p, e
		}
		if c.Kind == "host.edit" {
			for _, r := range rr {
				if r.Family != p.Removed[0].Family {
					return p, errors.New("編輯時請指定與原規則同版本的來源或目的 IP")
				}
			}
		}
		for _, r := range rr {
			p.Added = append(p.Added, r)
			args := ruleArgs(r)
			if c.Kind == "host.edit" {
				old := p.Removed[0]
				r.Position = old.Position
				current := s
				current.Rules = []Rule{}
				for _, existing := range s.Rules {
					if existing.ID != old.ID {
						current.Rules = append(current.Rules, existing)
					}
				}
				args = restoreArgs(current, r)
			}
			p.Commands = append(p.Commands, Step{"ufw", args})
		}
		p.Dangerous = p.Dangerous || c.Host.Action != "allow"
	case "host.delete":
		r, e := find()
		if e != nil {
			return p, e
		}
		if r.Kind != "host" {
			return p, errors.New("不是主機規則")
		}
		p.Removed = []Rule{r}
		p.Commands = []Step{{"ufw", deleteArgs(r)}}
		p.Dangerous = true
	case "ufw.toggle":
		if c.Enabled == s.Enabled {
			return p, errors.New("UFW 已處於指定狀態")
		}
		cmd := "disable"
		if c.Enabled {
			cmd = "enable"
		}
		p.Commands = []Step{{"ufw", []string{"--force", cmd}}}
		p.Dangerous = true
	case "docker.add", "docker.narrow", "docker.sync", "docker.delete":
		in := c.Docker
		if c.Kind == "docker.sync" || c.Kind == "docker.delete" {
			r, e := find()
			if e != nil {
				return p, e
			}
			if r.Kind != "docker" {
				return p, errors.New("不是 Docker 規則")
			}
			in = inputFromRule(r)
			p.Change.Docker = in
		}
		source, e := canonicalIP(in.Source)
		if e != nil {
			return p, e
		}
		if in.Source == "" {
			in.Source = "any"
		}
		p.Change.Docker = in
		// Deleting an orphan is supported, without requiring the container to exist.
		if c.Kind == "docker.delete" {
			if !s.Environment.DockerWritable || !s.Enabled {
				return p, errors.New("Docker 整合尚未可寫入")
			}
			for _, r := range s.Rules {
				if sameDocker(r, in) && dockerSourceText(r) == in.Source {
					if r.ReadOnly {
						return p, errors.New("同一 CLI 識別下包含唯讀規則")
					}
					p.Removed = append(p.Removed, r)
				}
			}
			p.Commands = append(p.Commands, dockerStep(in, true))
			p.Dangerous = true
		} else {
			rr, e := dockerRules(s, in)
			if e != nil {
				return p, e
			}
			if c.Kind == "docker.narrow" && source == "any" {
				return p, errors.New("縮限來源時請指定 IP/CIDR")
			}
			for _, r := range s.Rules {
				if !sameDocker(r, in) {
					continue
				}
				remove := dockerSourceText(r) == in.Source || (c.Kind == "docker.narrow" && anyIP(r.Source))
				if remove {
					if r.ReadOnly {
						return p, errors.New("同一 CLI 識別下包含唯讀規則")
					}
					p.Removed = append(p.Removed, r)
				} else {
					p.Warnings = append(p.Warnings, "保留其他來源放行："+r.Source+" → "+r.Destination)
				}
			}
			if c.Kind == "docker.narrow" {
				seen := map[string]bool{}
				for _, r := range p.Removed {
					if !anyIP(r.Source) {
						continue
					}
					broad := inputFromRule(r)
					if !seen[broad.Source] {
						p.Commands = append(p.Commands, dockerStep(broad, true))
						seen[broad.Source] = true
					}
				}
				p.Dangerous = true
			}
			p.Added = rr
			p.Commands = append(p.Commands, dockerStep(in, false))
			if len(p.Removed) > 0 {
				p.Dangerous = true
			}
		}
		p.Warnings = append(p.Warnings, "來源放行只新增允許條件；其他 UFW 轉送規則、既有連線及 ufw-docker 信任網段仍可能允許其他來源。")
		for _, container := range s.Containers {
			if container.Name == in.Container && len(container.Networks) > 1 {
				p.Warnings = append(p.Warnings, "容器連接多個網路；此規則只適用於所選網路的目的 IP。Docker 發布埠可能轉送至另一個網路，請核對實際 DNAT 目的。")
			}
		}
		for _, r := range s.Rules {
			if (r.Kind == "forward" || r.Kind == "docker") && r.Action == "allow" && !sameDocker(r, in) {
				p.Warnings = append(p.Warnings, "另有轉送放行，請檢查是否重疊："+r.Source+" → "+r.Destination+" port "+r.Port)
			}
		}
	default:
		return p, errors.New("不支援的變更種類")
	}
	// A UFW rule's comment is metadata, not part of its packet-match identity.
	// Reject overlapping identities to avoid UFW silently editing another comment.
	removed := map[string]bool{}
	for _, r := range p.Removed {
		removed[r.ID] = true
	}
	for _, a := range p.Added {
		for _, r := range s.Rules {
			if removed[r.ID] {
				continue
			}
			if packetIdentity(a) == packetIdentity(r) {
				return p, errors.New("相同封包條件已存在，請編輯既有規則")
			}
		}
	}
	for _, r := range append(append([]Rule{}, p.Added...), p.Removed...) {
		if strings.ContainsAny(r.Comment, "\x00\n\r") {
			return p, errors.New("註解格式不支援")
		}
	}
	return p, nil
}
func packetIdentity(r Rule) string {
	return digest([]any{r.Family, r.Action, r.Kind == "host", r.Direction, r.Protocol, r.Source, r.Destination, r.Port})
}
func globalPosition(s Snapshot, r Rule) int {
	n := r.Position
	if r.Family == 6 {
		for _, x := range s.Rules {
			if x.Family == 4 {
				n++
			}
		}
	}
	return n
}

// UFW insert accepts existing positions only. Restoring a family's last rule
// must append instead of inserting count+1, including when IPv6 rules follow it.
func restoreArgs(current Snapshot, r Rule) []string {
	count := 0
	for _, x := range current.Rules {
		if x.Family == r.Family {
			count++
		}
	}
	if r.Position > count {
		return ruleArgs(r)
	}
	return insertArgs(r, globalPosition(current, r))
}
