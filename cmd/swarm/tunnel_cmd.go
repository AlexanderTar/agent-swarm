package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/AlexanderTar/swarmrun/internal/install"
)

const tunnelUsage = "usage: swarm tunnel enable [--url wss://RELAY] | disable | status\n"

// tunnelStatus mirrors the daemon's GET /api/tunnel answer.
type tunnelStatus struct {
	Enabled        bool       `json:"enabled"`
	State          string     `json:"state"`
	URL            string     `json:"url"`
	PublicURL      string     `json:"public_url"`
	ConnectedSince *time.Time `json:"connected_since"`
	Reconnects     int        `json:"reconnects"`
	LastError      string     `json:"last_error"`
}

// cmdTunnel switches the relay tunnel through the daemon, which owns the
// switch, the secret and the connection.
func cmdTunnel(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, tunnelUsage)
		return 2
	}
	sub := args[0]
	if sub != "enable" && sub != "disable" && sub != "status" {
		fmt.Fprint(stderr, tunnelUsage)
		return 2
	}
	// --url is the relay here, so the daemon's address has its own flag
	fs := flag.NewFlagSet("swarm tunnel "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	home := fs.String("home", defaultHome(), "swarm data folder")
	daemon := fs.String("daemon", defaultURL(), "daemon address")
	relay := new(string)
	if sub == "enable" {
		relay = fs.String("url", "", "relay address, e.g. wss://your-relay.up.railway.app (needed the first time)")
	}
	if code, done := parse(fs, args[1:]); done {
		return code
	}
	c, err := newClient(*home, *daemon)
	if err != nil {
		return fail(stderr, err)
	}
	var st tunnelStatus
	switch sub {
	case "enable", "disable":
		body := map[string]any{"enabled": sub == "enable"}
		if *relay != "" {
			body["url"] = *relay
		}
		if err := c.do("PUT", "/api/tunnel", body, &st); err != nil {
			return fail(stderr, err)
		}
		if sub == "disable" {
			fmt.Fprintln(stdout, "Tunnel off.")
			return 0
		}
		fmt.Fprintln(stdout, "Tunnel on.")
	case "status":
		if err := c.do("GET", "/api/tunnel", nil, &st); err != nil {
			return fail(stderr, err)
		}
	}
	printTunnel(stdout, st)
	return 0
}

func printTunnel(w io.Writer, st tunnelStatus) {
	fmt.Fprintf(w, "Tunnel: %s\n", st.State)
	if st.PublicURL != "" {
		fmt.Fprintf(w, "URL: %s\n", st.PublicURL)
	}
	if st.ConnectedSince != nil && st.State == "connected" {
		fmt.Fprintf(w, "Connected since: %s\n", st.ConnectedSince.Local().Format("2006-01-02 15:04:05"))
	}
	if st.State != "disabled" {
		fmt.Fprintf(w, "Reconnects: %d\n", st.Reconnects)
	}
	if st.LastError != "" {
		fmt.Fprintf(w, "Last error: %s\n", st.LastError)
	}
	if st.State == "disabled" {
		fmt.Fprintln(w, "Turn it on with swarm tunnel enable --url wss://your-relay.up.railway.app")
	}
}

// tunnelProbe is the doctor's view of the tunnel: the daemon's own status.
func tunnelProbe(home, daemonURL string) func(context.Context) (install.TunnelInfo, error) {
	return func(context.Context) (install.TunnelInfo, error) {
		c, err := newClient(home, daemonURL)
		if err != nil {
			return install.TunnelInfo{}, err
		}
		var st tunnelStatus
		if err := c.do("GET", "/api/tunnel", nil, &st); err != nil {
			return install.TunnelInfo{}, err
		}
		return install.TunnelInfo{State: st.State, PublicURL: st.PublicURL, LastError: st.LastError}, nil
	}
}
