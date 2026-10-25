package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
)

// cmdSettings prints the settings, or changes one (PATCH /api/settings, so
// every other setting stays as stored). The menu bar's Settings window is the
// other editor; Linux has only this.
func cmdSettings(args []string, stdout, stderr io.Writer) int {
	c, rest, code, done := connect("settings", args, stderr, nil)
	if done {
		return code
	}
	switch {
	case len(rest) == 0:
		var cur json.RawMessage
		if err := c.do("GET", "/api/settings", nil, &cur); err != nil {
			return fail(stderr, err)
		}
		var b bytes.Buffer
		json.Indent(&b, cur, "", "  ")
		fmt.Fprintln(stdout, b.String())
		return 0
	case len(rest) == 3 && rest[0] == "set":
		key, val := rest[1], json.RawMessage(rest[2])
		if !json.Valid(val) {
			val, _ = json.Marshal(rest[2]) // a bare word is a string: swarm settings set ... claude
		}
		var saved map[string]json.RawMessage
		if err := c.do("PATCH", "/api/settings", map[string]json.RawMessage{key: val}, &saved); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "%s = %s\n", key, saved[key])
		return 0
	}
	fmt.Fprint(stderr, "Usage: swarm settings | swarm settings set KEY VALUE (VALUE is JSON; a bare word is a string)\n")
	return 2
}

// cmdNotifications lists the notifications the menu bar would show, newest
// first, or marks them all read.
func cmdNotifications(args []string, stdout, stderr io.Writer) int {
	var unread, readAll *bool
	c, rest, code, done := connect("notifications", args, stderr, func(fs *flag.FlagSet) {
		unread = fs.Bool("unread", false, "only unread notifications")
		readAll = fs.Bool("read-all", false, "mark every notification read")
	})
	if done {
		return code
	}
	if len(rest) != 0 {
		fmt.Fprint(stderr, "Usage: swarm notifications [--unread] [--read-all]\n")
		return 2
	}
	if *readAll {
		var out struct {
			Read int `json:"read"`
		}
		if err := c.do("POST", "/api/notifications/read-all", map[string]any{}, &out); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "Marked %d read.\n", out.Read)
		return 0
	}
	path := "/api/notifications?limit=50"
	if *unread {
		path += "&unread=1"
	}
	var list []struct {
		Level     string `json:"level"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		ReadAt    *int64 `json:"read_at"`
		CreatedAt int64  `json:"created_at"`
	}
	if err := c.do("GET", path, nil, &list); err != nil {
		return fail(stderr, err)
	}
	if len(list) == 0 {
		fmt.Fprintln(stdout, "No notifications.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, " \tWHEN\tLEVEL\tTITLE\tDETAIL")
	for _, n := range list {
		mark := "•" // unread
		if n.ReadAt != nil {
			mark = " "
		}
		when := time.UnixMilli(n.CreatedAt).Local().Format("Jan 2 15:04")
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", mark, when, n.Level, n.Title, strings.ReplaceAll(n.Body, "\n", " "))
	}
	tw.Flush()
	return 0
}
