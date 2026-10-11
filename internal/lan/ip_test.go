package lan

import (
	"errors"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

const routeOut = `   route to: default
destination: default
       mask: default
    gateway: 192.168.1.1
  interface: en0
      flags: <UP,GATEWAY,DONE,STATIC,PRCLONING,GLOBAL>
`

func TestPrimaryIPv4UsesTheDefaultRouteInterface(t *testing.T) {
	f := &execx.Fake{Responses: map[string]execx.Result{
		"route -n get default":   {Out: routeOut},
		"ipconfig getifaddr en0": {Out: "192.168.1.20\n"},
	}}
	ip, err := PrimaryIPv4(bg, f.Runner())
	if err != nil || ip.String() != "192.168.1.20" {
		t.Fatalf("ip = %v, err = %v", ip, err)
	}
}

func TestPrimaryIPv4FailsWhenOffline(t *testing.T) {
	for name, res := range map[string]map[string]execx.Result{
		"no default route":  {"route -n get default": {Err: errors.New("route: writing to routing socket: not in table")}},
		"no interface line": {"route -n get default": {Out: "   route to: default\n"}},
		"interface has no address": {
			"route -n get default":   {Out: routeOut},
			"ipconfig getifaddr en0": {Err: errors.New("exit status 1")},
		},
		"not an IPv4 address": {
			"route -n get default":   {Out: routeOut},
			"ipconfig getifaddr en0": {Out: "fe80::1\n"},
		},
		"unspecified": {
			"route -n get default":   {Out: routeOut},
			"ipconfig getifaddr en0": {Out: "0.0.0.0\n"},
		},
	} {
		f := &execx.Fake{Responses: res}
		if ip, err := PrimaryIPv4(bg, f.Runner()); err == nil {
			t.Errorf("%s: got %v, want an error", name, ip)
		}
	}
}
