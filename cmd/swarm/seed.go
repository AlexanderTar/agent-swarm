package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// refuseProduction keeps dev-seed away from the real daemon. It is fixture
// data, and ~/.swarm / :7777 are the live one's home and port.
//
// filepath.EvalSymlinks needs the path to exist; a --home that hasn't been
// created yet (as in the CLI-level refusal test, which never creates
// filepath.Join(home, ".swarm")) would make it fail and fall through to
// comparing "" — never matching, and so never refusing. Falling back to the
// cleaned, unresolved path keeps the check working whether or not ~/.swarm
// happens to exist on this machine, which the brief's own snippet did not
// need to handle only because it assumed a machine where it already did.
func refuseProduction(home, url string) error {
	userHome, _ := os.UserHomeDir()
	want := filepath.Join(userHome, ".swarm")
	real, err := filepath.EvalSymlinks(home)
	if err != nil {
		real = filepath.Clean(home)
	}
	if real == want || strings.Contains(url, ":7777") {
		return errors.New("dev-seed refuses to write to the live daemon. Use --home ~/.swarm-dev --url http://127.0.0.1:17777.")
	}
	return nil
}

func cmdDevSeed(args []string, stdout, stderr io.Writer) int {
	fs, home, base := flags("dev-seed", stderr, true)
	if code, done := parse(fs, args); done {
		return code
	}
	if err := refuseProduction(*home, *base); err != nil {
		return fail(stderr, err)
	}
	c, err := newClient(*home, *base)
	if err != nil {
		return fail(stderr, err)
	}
	var out struct {
		Items    int `json:"items"`
		Requests int `json:"requests"`
	}
	if err := c.do("POST", "/api/dev/seed", nil, &out); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "Seeded %d item(s) and %d request(s) at %s.\n", out.Items, out.Requests, c.base)
	return 0
}
