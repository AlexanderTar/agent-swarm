package lan

import (
	"context"
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

func TestPrimaryIPv4Linux(t *testing.T) {
	const cmd = "ip -4 route get 1.1.1.1"
	for _, tc := range []struct {
		name string
		res  execx.Result
		want string // "" = error
	}{
		{"wifi", execx.Result{Out: "1.1.1.1 via 10.10.10.1 dev wlp59s0 src 10.10.9.75 uid 1000 \n    cache \n"}, "10.10.9.75"},
		{"direct", execx.Result{Out: "1.1.1.1 dev eth0 src 192.168.1.42 uid 1000\n"}, "192.168.1.42"},
		{"offline", execx.Result{Err: errors.New("RTNETLINK answers: Network is unreachable")}, ""},
		{"no src", execx.Result{Out: "1.1.1.1 via 10.0.0.1 dev tun0\n"}, ""},
		{"loopback", execx.Result{Out: "1.1.1.1 dev lo src 127.0.0.1\n"}, ""},
	} {
		run := (&execx.Fake{Responses: map[string]execx.Result{cmd: tc.res}}).Runner()
		ip, err := PrimaryIPv4Linux(context.Background(), run)
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: ip = %v, want an error", tc.name, ip)
			}
			continue
		}
		if err != nil || ip.String() != tc.want {
			t.Errorf("%s: ip = %v %v, want %s", tc.name, ip, err, tc.want)
		}
	}
}
