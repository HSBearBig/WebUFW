//go:build linux

package webufw

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func dockerFixture(info string, fail string) func(context.Context, string, any) error {
	return func(_ context.Context, path string, dst any) error {
		if path == fail {
			return errors.New("daemon unavailable")
		}
		responses := map[string]string{
			"/info":                  info,
			"/containers/json?all=1": `[{"Id":"abc","Names":["/web"],"Image":"test:local","State":"running","Labels":{},"HostConfig":{"NetworkMode":"frontend"},"NetworkSettings":{"Networks":{"frontend":{"NetworkID":"net1","IPAddress":"172.28.0.10","GlobalIPv6Address":"fd00:28::10"},"backend":{"NetworkID":"net2","IPAddress":"172.29.0.10"}}},"Ports":[{"IP":"0.0.0.0","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"IP":"::","PrivatePort":80,"PublicPort":8080,"Type":"tcp"},{"IP":"0.0.0.0","PrivatePort":53,"PublicPort":5353,"Type":"udp"},{"PrivatePort":9000,"Type":"tcp"}]}]`,
			"/networks":              `[{"Id":"net1","Driver":"bridge","Internal":false,"Options":{}},{"Id":"net2","Driver":"bridge","Internal":false,"Options":{"com.docker.network.bridge.gateway_mode_ipv4":"routed"}}]`,
		}
		return json.Unmarshal([]byte(responses[path]), dst)
	}
}

func TestDockerAPIMapsPublishedPortsAndNetworks(t *testing.T) {
	query := dockerFixture(`{"OSType":"linux","OperatingSystem":"Ubuntu","FirewallBackend":{"Driver":"iptables"},"Swarm":{"LocalNodeState":"inactive"}}`, "")
	containers, writable, warnings := inspectDockerWith(t.Context(), query)
	if !writable || len(warnings) != 0 || len(containers) != 1 {
		t.Fatal(containers, writable, warnings)
	}
	c := containers[0]
	if c.Name != "web" || !c.Running || c.Networks[0].Supported || !c.Networks[1].Supported || c.Networks[1].IPv6 != "fd00:28::10" {
		t.Fatal(c)
	}
	want := []Port{
		{Port: "53", Protocol: "udp", Bindings: []string{"0.0.0.0:5353 → 53/udp"}},
		{Port: "80", Protocol: "tcp", Bindings: []string{"0.0.0.0:8080 → 80/tcp", "[::]:8080 → 80/tcp"}},
	}
	if !reflect.DeepEqual(c.Ports, want) {
		t.Fatal(c.Ports)
	}
	// An API failure must never turn into an empty but writable Docker state.
	for _, path := range []string{"/info", "/containers/json?all=1", "/networks"} {
		_, writable, warnings = inspectDockerWith(t.Context(), dockerFixture(`{"OSType":"linux"}`, path))
		if writable || len(warnings) == 0 {
			t.Fatal(path, writable, warnings)
		}
	}
}

func TestDockerAPIUnsupportedEnvironmentsAreReadOnly(t *testing.T) {
	for _, info := range []string{
		`{"OSType":"linux","OperatingSystem":"Docker Desktop"}`,
		`{"OSType":"linux","SecurityOptions":["name=rootless"]}`,
		`{"OSType":"linux","Swarm":{"LocalNodeState":"active"}}`,
		`{"OSType":"linux","FirewallBackend":{"Driver":"nftables"}}`,
		`{"OSType":"windows"}`,
	} {
		containers, writable, warnings := inspectDockerWith(t.Context(), dockerFixture(info, ""))
		if len(containers) != 1 || writable || len(warnings) == 0 {
			t.Fatal(info, writable, warnings)
		}
	}
}
