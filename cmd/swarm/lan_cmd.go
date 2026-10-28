package main

import (
	"context"
	"fmt"
	"io"

	"github.com/AlexanderTar/swarmrun/internal/install"
)

const lanUsage = "usage: swarm lan enable | disable | status\n"

// lanStatus mirrors the daemon's GET /api/lan answer.
type lanStatus struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"`
	IP        string `json:"ip"`
	Hostname  string `json:"hostname"`
	URL       string `json:"url"`
	LastError string `json:"last_error"`
}

// cmdLAN switches LAN access through the daemon, which owns the switch, the
// listener and the swarm.local registration.
func cmdLAN(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "enable" && args[0] != "disable" && args[0] != "status") {
		fmt.Fprint(stderr, lanUsage)
		return 2
	}
	sub := args[0]
	c, _, code, done := connect("lan "+sub, args[1:], stderr, nil)
	if done {
		return code
	}
	var st lanStatus
	switch sub {
	case "enable", "disable":
		if err := c.do("PUT", "/api/lan", map[string]bool{"enabled": sub == "enable"}, &st); err != nil {
			return fail(stderr, err)
		}
		if sub == "disable" {
			fmt.Fprintln(stdout, "LAN access off.")
			return 0
		}
		fmt.Fprintln(stdout, "LAN access on.")
	case "status":
		if err := c.do("GET", "/api/lan", nil, &st); err != nil {
			return fail(stderr, err)
		}
	}
	printLAN(stdout, st)
	if sub == "enable" {
		fmt.Fprint(stdout, lanWarnings)
	}
	return 0
}

const lanWarnings = `
Before you use it:
  - This is plain HTTP. Anyone on the same network can read what's sent,
    including your password. Use it on a network you trust, like home Wi-Fi.
  - macOS may ask whether swarm can accept incoming connections. Choose Allow,
    or other devices can't connect. If it doesn't ask, check the firewall in
    System Settings > Network > Firewall.
`

func printLAN(w io.Writer, st lanStatus) {
	fmt.Fprintf(w, "LAN: %s\n", st.State)
	if st.URL != "" {
		fmt.Fprintf(w, "Open: %s\n", st.URL)
	}
	if st.IP != "" {
		fmt.Fprintf(w, "Address: %s\n", st.IP)
	}
	if st.LastError != "" {
		fmt.Fprintf(w, "Last error: %s\n", st.LastError)
	}
	if st.State == "disabled" {
		fmt.Fprintln(w, "Turn it on with swarm lan enable")
	}
}

// lanProbe is the doctor's view of LAN access: the daemon's own status.
func lanProbe(home, daemonURL string) func(context.Context) (install.LANInfo, error) {
	return func(context.Context) (install.LANInfo, error) {
		c, err := newClient(home, daemonURL)
		if err != nil {
			return install.LANInfo{}, err
		}
		var st lanStatus
		if err := c.do("GET", "/api/lan", nil, &st); err != nil {
			return install.LANInfo{}, err
		}
		return install.LANInfo{State: st.State, URL: st.URL, LastError: st.LastError}, nil
	}
}
