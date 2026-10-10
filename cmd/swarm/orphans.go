package main

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"

	"github.com/AlexanderTar/agent-swarm/internal/install"
)

// sessionLookup answers doctor's "which agent and state is session id?" from
// the database directly, read-only so a doctor run never migrates or locks it.
// Any failure (no database yet, unknown id) reads as "not found".
func sessionLookup(home string) func(context.Context, string) (install.SessionRef, bool) {
	return func(ctx context.Context, id string) (install.SessionRef, bool) {
		dsn := (&url.URL{Scheme: "file", Opaque: filepath.Join(home, "swarm.db"),
			RawQuery: "mode=ro&_pragma=busy_timeout(2000)"}).String()
		d, err := sql.Open("sqlite", dsn)
		if err != nil {
			return install.SessionRef{}, false
		}
		defer d.Close()
		var ref install.SessionRef
		err = d.QueryRowContext(ctx, `SELECT a.name, s.state FROM sessions s
			JOIN agents a ON a.id = s.agent_id WHERE s.id = ?`, id).Scan(&ref.Agent, &ref.State)
		return ref, err == nil
	}
}
