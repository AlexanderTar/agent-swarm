package mcpserver

// Batch 4 (B10/F6) general-read bounds: validated filter.kind, field
// projection, default-50/max-200 pagination with a stable cursor, per-ref
// errors, and explicit unsupported-filter errors. The Batch 2 recovery
// query (recovery.go) owns its own limit/cursor and is untouched.

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
)

// readKinds is the validated filter.kind vocabulary (spec §6, F6).
var readKinds = []string{"item", "agent", "checkpoint", "artifact", "request", "worktree"}

const (
	readDefaultLimit = 50
	readMaxLimit     = 200
)

// parseReadKind validates filter.kind: empty means every collection.
func parseReadKind(kind string) (string, error) {
	if kind == "" {
		return "", nil
	}
	for _, k := range readKinds {
		if kind == k {
			return kind, nil
		}
	}
	return "", &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
		"unknown kind %q: kind must be item, agent, checkpoint, artifact, request or worktree.", kind)}
}

// parseReadLimit applies the default-50/max-200 bound (spec §6, F6),
// capping like the recovery query rather than refusing.
func parseReadLimit(limit int) int {
	if limit <= 0 {
		return readDefaultLimit
	}
	if limit > readMaxLimit {
		return readMaxLimit
	}
	return limit
}

// readFilterKeys are the only supported filter keys; anything else is an
// explicit unsupported-filter error, never silently ignored input.
var readFilterKeys = map[string]bool{"root": true, "type": true, "status": true, "q": true, "kind": true}

// checkFilterKeys refuses unknown filter keys from the raw object.
func checkFilterKeys(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	for k := range m {
		if !readFilterKeys[k] {
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"unsupported filter %q: filter supports root, type, status, q and kind only.", k)}
		}
	}
	return nil
}

// encodePageCursor builds the opaque filter-page cursor: updated_at millis
// plus the id tie-break, so same-timestamp rows order deterministically.
func encodePageCursor(updatedAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%s", updatedAt, id)))
}

// decodePageCursor parses a page cursor back; garbage is an explicit error.
func decodePageCursor(s string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, "", &items.Error{Code: items.CodeBadRequest, Message: "invalid cursor: not a page cursor."}
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", &items.Error{Code: items.CodeBadRequest, Message: "invalid cursor: not a page cursor."}
	}
	var ms int64
	if _, err := fmt.Sscanf(parts[0], "%d", &ms); err != nil {
		return 0, "", &items.Error{Code: items.CodeBadRequest, Message: "invalid cursor: not a page cursor."}
	}
	return ms, parts[1], nil
}

// readFieldAllowlist is the projectable vocabulary per collection. Identity
// keys (below) are always retained even when unlisted, with the cursor.
var readFieldAllowlist = map[string]map[string]bool{
	"item": {
		"id": true, "key": true, "type": true, "parent_id": true, "parent_key": true,
		"root_id": true, "root_key": true, "title": true, "brief": true,
		"acceptance": true, "status": true, "status_before_block": true,
		"priority": true, "role_hint": true, "tdd_exempt": true, "workflow": true,
		"steps": true, "units": true, "solo": true, "verify": true, "repos": true,
		"repos_version": true, "suggested_repos": true, "spike_intent": true,
		"origin_spike_id": true, "legacy_key": true, "sort_order": true,
		"revision": true, "archived_at": true, "created_at": true, "updated_at": true,
		"blocked_by": true, "progress": true, "active_agents": true,
		"open_requests": true, "context": true, "workflow_state": true, "crew": true,
	},
	"agent": {
		"name": true, "kind": true, "model": true, "role": true, "state": true,
		"role_overrides": true, "parent": true, "step": true,
	},
	"checkpoint": {"item": true, "kind": true, "summary": true, "created_at": true},
	"artifact":   {"artifact_id": true, "kind": true, "revision": true, "sections": true},
	"request":    {"request_id": true, "kind": true, "state": true},
	"worktree":   {"worktree_id": true, "path": true, "branch": true, "base_sha": true, "state": true},
}

// readIdentityKeys are retained in every projection of their collection.
var readIdentityKeys = map[string]string{
	"item": "key", "agent": "name", "checkpoint": "item",
	"artifact": "artifact_id", "request": "request_id", "worktree": "worktree_id",
}

// projectFields narrows m to the allowlisted fields, always keeping the
// collection's identity key. Empty fields returns m whole. Fields outside
// kind's own allowlist are dropped here, never an error: the caller
// validates every requested field up front (validateReadFields), so a field
// that belongs to a sibling collection in a mixed read simply selects
// nothing from this one, while identity still comes back.
func projectFields(kind string, m map[string]any, fields []string) (map[string]any, error) {
	if len(fields) == 0 {
		return m, nil
	}
	allow, ok := readFieldAllowlist[kind]
	if !ok {
		return nil, &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
			"unknown kind %q: kind must be item, agent, checkpoint, artifact, request or worktree.", kind)}
	}
	out := map[string]any{}
	for _, f := range fields {
		if !allow[f] {
			continue
		}
		if v, ok := m[f]; ok {
			out[f] = v
		}
	}
	if idKey := readIdentityKeys[kind]; idKey != "" {
		if v, ok := m[idKey]; ok {
			out[idKey] = v
		}
	}
	return out, nil
}

// validateReadFields refuses fields no returned collection understands.
// kinds lists the collections this read returns (one entry when filter.kind
// selects, all six otherwise); a field valid for any of them is accepted.
func validateReadFields(kinds []string, fields []string) error {
	for _, f := range fields {
		ok := false
		for _, k := range kinds {
			if readFieldAllowlist[k][f] {
				ok = true
				break
			}
		}
		if !ok {
			if len(kinds) == 1 {
				return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
					"unknown field %q for kind %q.", f, kinds[0])}
			}
			return &items.Error{Code: items.CodeBadRequest, Message: fmt.Sprintf(
				"unknown field %q: no read collection projects it.", f)}
		}
	}
	return nil
}

// sortFilterItems orders filter results newest-first with the id tie-break,
// the total order the page cursor walks.
func sortFilterItems(list []items.Item) {
	sort.Slice(list, func(i, j int) bool {
		iu, ju := db.Millis(list[i].UpdatedAt), db.Millis(list[j].UpdatedAt)
		if iu != ju {
			return iu > ju
		}
		return list[i].ID < list[j].ID
	})
}

// pageFilterItems slices the ordered filter results after the cursor.
// It returns the page and the next cursor ("" when the page is the last).
func pageFilterItems(list []items.Item, limit int, cursor string) ([]items.Item, string, error) {
	start := 0
	if cursor != "" {
		ms, id, err := decodePageCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		for start < len(list) {
			u := db.Millis(list[start].UpdatedAt)
			if u < ms || (u == ms && list[start].ID > id) {
				break
			}
			start++
		}
	}
	if start > len(list) {
		start = len(list)
	}
	end := start + limit
	if end > len(list) {
		end = len(list)
	}
	page := list[start:end]
	next := ""
	if end < len(list) {
		last := page[len(page)-1]
		next = encodePageCursor(db.Millis(last.UpdatedAt), last.ID)
	}
	return page, next, nil
}

// refError maps a ref-resolution failure to a sanitized per-ref entry: the
// ref and a stable code, never driver text.
func refError(ref string, err error) map[string]any {
	var ie *items.Error
	if (errors.As(err, &ie) && ie.Code == items.CodeNotFound) || errors.Is(err, sql.ErrNoRows) {
		return map[string]any{"ref": ref, "code": items.CodeNotFound,
			"message": fmt.Sprintf("unknown ref %q: no item, agent, artifact, request or worktree matches.", ref)}
	}
	return map[string]any{"ref": ref, "code": "read_failed",
		"message": fmt.Sprintf("could not resolve ref %q.", ref)}
}
