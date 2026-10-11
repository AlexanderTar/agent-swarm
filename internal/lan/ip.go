// Package lan serves the board on the Mac's LAN address and advertises it as
// swarm.local over Bonjour.
package lan

import (
	"context"
	"errors"
	"net"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
)

// PrimaryIPv4 is the address of the interface the default route leaves by
// (`route -n get default`, then `ipconfig getifaddr <if>`). It fails when the
// Mac has no usable IPv4 LAN address, e.g. while offline.
func PrimaryIPv4(ctx context.Context, run execx.Runner) (net.IP, error) {
	out, err := run(ctx, "route", "-n", "get", "default")
	if err != nil {
		return nil, err
	}
	var iface string
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), ":"); ok && k == "interface" {
			iface = strings.TrimSpace(v)
		}
	}
	if iface == "" {
		return nil, errors.New("no default route")
	}
	out, err = run(ctx, "ipconfig", "getifaddr", iface)
	if err != nil {
		return nil, err
	}
	return usable(net.ParseIP(strings.TrimSpace(string(out))))
}

// usable accepts only a real unicast IPv4 address, so the listener can never
// end up on a wildcard or loopback address.
func usable(ip net.IP) (net.IP, error) {
	if ip4 := ip.To4(); ip4 != nil && !ip4.IsUnspecified() && !ip4.IsLoopback() {
		return ip4, nil
	}
	return nil, errors.New("no IPv4 address on the default network interface")
}
