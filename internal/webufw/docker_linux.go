//go:build linux

package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Only the root agent can access this socket. It exposes no generic Docker
// proxy to the web process; these three read-only paths are fixed here.
var dockerClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", "/var/run/docker.sock")
		},
		MaxIdleConns: 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("Docker redirects are not supported") },
}

func dockerJSON(ctx context.Context, path string, result any) error {
	switch path {
	case "/info", "/containers/json?all=1", "/networks":
	default:
		return errors.New("unsupported Docker query")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker"+path, nil)
	if err != nil {
		return err
	}
	response, err := dockerClient.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Docker HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("Docker response exceeds 2 MiB")
	}
	return json.Unmarshal(data, result)
}

func inspectDocker(ctx context.Context) ([]Container, bool, []string) {
	containers, writable, warnings := inspectDockerWith(ctx, dockerJSON)
	if containers == nil {
		return containers, writable, warnings
	}
	if config, err := readBounded("/etc/docker/daemon.json"); err == nil {
		var cfg map[string]any
		if json.Unmarshal(config, &cfg) != nil || cfg["firewall-backend"] == "nftables" || cfg["iptables"] == false {
			writable = false
			warnings = append(warnings, "Docker daemon 設定不支援寫入，請檢查 firewall-backend / iptables。")
		}
	} else if !os.IsNotExist(err) {
		writable = false
		warnings = append(warnings, "無法檢查 Docker daemon 設定，僅供檢視。")
	}
	if _, err := os.Stat("/usr/bin/docker"); err != nil {
		writable = false
		warnings = append(warnings, "ufw-docker 寫入需要 /usr/bin/docker CLI。")
	}
	return containers, writable, warnings
}

func inspectDockerWith(ctx context.Context, query func(context.Context, string, any) error) ([]Container, bool, []string) {
	warnings := []string{}
	var info struct {
		OSType, OperatingSystem string
		FirewallBackend         json.RawMessage
		SecurityOptions         []string
		Swarm                   struct{ LocalNodeState string }
	}
	if err := query(ctx, "/info", &info); err != nil {
		return nil, false, []string{"Docker Engine 無法存取；主機 UFW 功能仍可使用。"}
	}
	backend, known := dockerFirewallBackend(info.FirewallBackend)
	writable := known && info.OSType == "linux" && !strings.Contains(strings.ToLower(info.OperatingSystem), "docker desktop") && info.Swarm.LocalNodeState != "active" && backend != "nftables"
	for _, option := range info.SecurityOptions {
		if strings.Contains(option, "rootless") {
			writable = false
		}
	}
	if !writable {
		warnings = append(warnings, "目前 Docker 模式不支援寫入（Swarm、rootless、Desktop 或原生 nftables）。")
	}
	var raw []struct {
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
	if err := query(ctx, "/containers/json?all=1", &raw); err != nil {
		return nil, false, []string{"無法讀取 Docker 容器資料"}
	}
	if len(raw) > 200 {
		raw = raw[:200]
		writable = false
		warnings = append(warnings, "容器數超過 200，本次僅檢視前 200 個。")
	}
	type networkDetails struct {
		ID, Driver string
		Internal   bool
		Options    map[string]string
	}
	nets := map[string]networkDetails{}
	if len(raw) > 0 {
		var all []networkDetails
		if err := query(ctx, "/networks", &all); err != nil {
			return nil, false, []string{"無法檢查 Docker 網路"}
		}
		for _, n := range all {
			nets[n.ID] = n
		}
	}
	result := []Container{}
	for _, x := range raw {
		c := Container{ID: x.ID, Image: x.Image, Running: x.State == "running", Networks: []Network{}, Ports: []Port{}}
		if len(x.Names) > 0 {
			c.Name = strings.TrimPrefix(x.Names[0], "/")
		}
		if !namePattern.MatchString(c.Name) {
			c.Reason = "無法辨識容器名稱"
		}
		if x.HostConfig.NetworkMode == "host" || strings.HasPrefix(x.HostConfig.NetworkMode, "container:") || x.Labels["com.docker.swarm.service.id"] != "" {
			c.Reason = "不支援此網路模式"
		}
		for name, n := range x.NetworkSettings.Networks {
			nd, ok := nets[n.NetworkID]
			supported := ok && nd.Driver == "bridge" && !nd.Internal
			for _, key := range []string{"com.docker.network.bridge.gateway_mode_ipv4", "com.docker.network.bridge.gateway_mode_ipv6"} {
				if mode := nd.Options[key]; mode != "" && mode != "nat" {
					supported = false
				}
			}
			c.Networks = append(c.Networks, Network{Name: name, IPv4: n.IPAddress, IPv6: n.GlobalIPv6Address, Driver: nd.Driver, Supported: supported})
		}
		ports := map[string]*Port{}
		for _, binding := range x.Ports {
			if binding.PublicPort == 0 || binding.PrivatePort == 0 || (binding.Type != "tcp" && binding.Type != "udp") {
				continue
			}
			port := strconv.Itoa(int(binding.PrivatePort))
			key := port + "/" + binding.Type
			if ports[key] == nil {
				ports[key] = &Port{Port: port, Protocol: binding.Type, Bindings: []string{}}
			}
			ports[key].Bindings = append(ports[key].Bindings, net.JoinHostPort(binding.IP, strconv.Itoa(int(binding.PublicPort)))+" → "+key)
		}
		for _, p := range ports {
			sort.Strings(p.Bindings)
			c.Ports = append(c.Ports, *p)
		}
		sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Name < c.Networks[j].Name })
		sort.Slice(c.Ports, func(i, j int) bool {
			return c.Ports[i].Port+"/"+c.Ports[i].Protocol < c.Ports[j].Port+"/"+c.Ports[j].Protocol
		})
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, writable, warnings
}
func dockerFirewallBackend(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", true
	}
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name, name == "" || name == "iptables" || name == "nftables"
	}
	var v struct{ Driver string }
	if json.Unmarshal(raw, &v) == nil {
		return v.Driver, v.Driver == "iptables" || v.Driver == "nftables"
	}
	return "", false
}
