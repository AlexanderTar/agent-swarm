package httpapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

// itemWire is Item on the wire (contracts §3.1): ms timestamps, null for unset optional
// fields, arrays never null, origin_spike_key resolved. Outer fields shadow the embedded ones.
type itemWire struct {
	items.Item
	ParentID          *string         `json:"parent_id"`
	ParentKey         *string         `json:"parent_key"`
	StatusBeforeBlock *string         `json:"status_before_block"`
	RoleHint          *string         `json:"role_hint"`
	TddExempt         *string         `json:"tdd_exempt"`
	SpikeIntent       *string         `json:"spike_intent"`
	OriginSpikeID     *string         `json:"origin_spike_id"`
	OriginSpikeKey    *string         `json:"origin_spike_key"`
	LegacyKey         *string         `json:"legacy_key"`
	Progress          *items.Progress `json:"progress"`
	ArchivedAt        *int64          `json:"archived_at"`
	CreatedAt         int64           `json:"created_at"`
	UpdatedAt         int64           `json:"updated_at"`
	Context           bool            `json:"context"`
}

func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) itemOut(ctx context.Context, it items.Item) (itemWire, error) {
	it.Acceptance, it.Repos, it.SuggestedRepos, it.BlockedBy =
		orEmpty(it.Acceptance), orEmpty(it.Repos), orEmpty(it.SuggestedRepos), orEmpty(it.BlockedBy)
	w := itemWire{Item: it, ParentID: orNull(it.ParentID), ParentKey: orNull(it.ParentKey),
		StatusBeforeBlock: orNull(string(it.StatusBeforeBlock)), RoleHint: orNull(it.RoleHint),
		TddExempt: orNull(it.TddExempt), SpikeIntent: orNull(it.SpikeIntent), OriginSpikeID: orNull(it.OriginSpikeID),
		LegacyKey: orNull(it.LegacyKey), Progress: it.Progress, Context: it.Context,
		CreatedAt: db.Millis(it.CreatedAt), UpdatedAt: db.Millis(it.UpdatedAt)}
	if it.ArchivedAt != nil {
		ms := db.Millis(*it.ArchivedAt)
		w.ArchivedAt = &ms
	}
	if it.OriginSpikeID != "" {
		// ponytail: one query per spike-born item; join in items.Store if lists get long.
		var key string
		if err := s.DB.QueryRowContext(ctx, `SELECT key FROM items WHERE id = ?`, it.OriginSpikeID).Scan(&key); err != nil {
			return itemWire{}, err
		}
		w.OriginSpikeKey = &key
	}
	return w, nil
}

func (s *Server) itemsOut(ctx context.Context, list []items.Item) ([]itemWire, error) {
	out := make([]itemWire, 0, len(list))
	for _, it := range list {
		w, err := s.itemOut(ctx, it)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func (s *Server) itemRoutes() []route {
	return []route{
		{"GET", "/api/items", authDaemon, s.listItems},
		{"POST", "/api/items", authDaemon, s.createItem},
		{"GET", "/api/items/{key}", authDaemon, s.getItem},
		{"PATCH", "/api/items/{key}", authDaemon, s.patchItem},
		{"POST", "/api/items/{key}/deps", authDaemon, s.addDep},
		{"DELETE", "/api/items/{key}/deps/{blockedBy}", authDaemon, s.removeDep},
		{"GET", "/api/items/{key}/graph", authDaemon, s.itemGraph},
	}
}

// idempotent runs fn once per (caller, request_id) and replays the stored result (I11).
// ponytail: one server-wide lock; per-caller locks if UI writes ever contend.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, requestID, tool string, status int, fn func(context.Context) (any, error)) {
	if requestID == "" {
		v, err := fn(r.Context())
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, status, v)
		return
	}
	if len(requestID) > 64 {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "request_id must be at most 64 characters."))
		return
	}
	sum := sha256.Sum256([]byte(s.Token))
	caller := "ui:" + hex.EncodeToString(sum[:])
	// the write and its replay record must not be split by a client abort, or a
	// retry with the same request_id creates a second item
	ctx := context.WithoutCancel(r.Context())
	s.idemMu.Lock()
	defer s.idemMu.Unlock()
	var stored string
	err := s.DB.QueryRowContext(ctx, `SELECT result_json FROM idempotency WHERE caller = ? AND request_id = ?`,
		caller, requestID).Scan(&stored)
	if err == nil {
		writeJSON(w, status, json.RawMessage(stored))
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		s.writeErr(w, err)
		return
	}
	v, err := fn(ctx)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	body, _ := json.Marshal(v)
	if _, err := s.DB.ExecContext(ctx, `INSERT OR IGNORE INTO idempotency (caller, request_id, tool, result_json, created_at)
		VALUES (?, ?, ?, ?, ?)`, caller, requestID, tool, string(body), db.Millis(time.Now())); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, status, json.RawMessage(body))
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, n, err := s.Items.List(r.Context(), items.ListFilter{View: q.Get("view"), Type: items.Type(q.Get("type")),
		Status: items.Status(q.Get("status")), Q: q.Get("q"), Root: q.Get("root")})
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out, err := s.itemsOut(r.Context(), list)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out, "matches": n})
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RequestID  string   `json:"request_id"`
		Type       string   `json:"type"`
		Title      string   `json:"title"`
		Brief      string   `json:"brief"`
		Acceptance []string `json:"acceptance"`
		ParentKey  string   `json:"parent_key"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if body.Type == string(items.Spike) {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "Spikes start with an intent. Use New spike."))
		return
	}
	s.idempotent(w, r, body.RequestID, "POST /api/items", http.StatusCreated, func(ctx context.Context) (any, error) {
		it, err := s.Items.Create(ctx, items.CreateInput{Type: items.Type(body.Type), Title: body.Title,
			Brief: body.Brief, Acceptance: body.Acceptance, ParentKey: body.ParentKey}, items.User(via(r)))
		if err != nil {
			return nil, err
		}
		if it, err = s.Items.Get(ctx, it.Key); err != nil {
			return nil, err
		}
		return s.itemOut(ctx, it)
	})
}

func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	ctx, key := r.Context(), r.PathValue("key")
	it, err := s.Items.Get(ctx, key)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	ancestors, err := s.Items.Ancestors(ctx, key)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	children, err := s.Items.Children(ctx, key)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	blockedBy, blocks, err := s.Items.Deps(ctx, key)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	item, err := s.itemOut(ctx, it)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := map[string]any{"item": item, "agents": []any{}, "requests": []any{}, "artifacts": []any{}}
	// RT is nil in a P1-only harness (no P2 wired yet, D13); once wired (always
	// true from cmd/swarm's daemon, Task 35), these three fill in.
	if s.RT != nil {
		flat, err := s.RT.AgentTree(ctx, it.RootKey)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		var onThisItem []runtime.Agent
		for _, a := range flat {
			if a.ItemID == it.ID {
				onThisItem = append(onThisItem, a)
			}
		}
		live := s.livePanes(ctx)
		agentNodes, err := s.agentNodesFor(ctx, onThisItem, flat, live)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out["agents"] = agentNodes
		reqs, err := s.openRequestsWire(ctx, key, "")
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out["requests"] = reqs
		artifacts, err := s.RT.ArtifactsFor(ctx, it.ID)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		artifactWires, err := s.artifactsOut(ctx, artifacts)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		out["artifacts"] = artifactWires
	}
	lists := map[string][]items.Item{"ancestors": ancestors, "children": children, "blocked_by": blockedBy, "blocks": blocks}
	wired := map[string][]itemWire{}
	for name, v := range lists {
		if wired[name], err = s.itemsOut(ctx, v); err != nil {
			s.writeErr(w, err)
			return
		}
	}
	out["ancestors"], out["children"] = wired["ancestors"], wired["children"]
	out["deps"] = map[string]any{"blocked_by": wired["blocked_by"], "blocks": wired["blocks"]}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      *string       `json:"title"`
		Brief      *string       `json:"brief"`
		Acceptance *[]string     `json:"acceptance"`
		Priority   *int          `json:"priority"`
		Status     *items.Status `json:"status"`
		Revision   *int          `json:"revision"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if body.Revision == nil {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "revision is required."))
		return
	}
	it, err := s.Items.Update(r.Context(), r.PathValue("key"), items.Patch{Title: body.Title, Brief: body.Brief,
		Acceptance: body.Acceptance, Priority: body.Priority, Status: body.Status, Revision: *body.Revision}, items.User(via(r)))
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out, err := s.itemOut(r.Context(), it)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) addDep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		BlockedBy string `json:"blocked_by"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	if body.BlockedBy == "" {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "blocked_by is required."))
		return
	}
	if err := s.Items.AddDep(r.Context(), r.PathValue("key"), body.BlockedBy, items.User(via(r))); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) removeDep(w http.ResponseWriter, r *http.Request) {
	if err := s.Items.RemoveDep(r.Context(), r.PathValue("key"), r.PathValue("blockedBy"), items.User(via(r))); err != nil {
		s.writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) itemGraph(w http.ResponseWriter, r *http.Request) {
	hops := 1
	if h := r.URL.Query().Get("hops"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil {
			s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "hops must be a number."))
			return
		}
		hops = n
	}
	g, err := s.Items.Graph(r.Context(), r.PathValue("key"), r.URL.Query().Get("scope"), hops)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
