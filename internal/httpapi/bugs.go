package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) bugRoutes() []route {
	return []route{
		{"GET", "/api/bugs", authDaemon, s.listBugs},
		{"GET", "/api/bugs/{id}", authDaemon, s.getBug},
		{"POST", "/api/bugs/{id}/issue", authDaemon, s.setBugIssue},
	}
}

type bugWire struct {
	ID           string `json:"id"`
	CreatedAt    int64  `json:"created_at"`
	Reporter     string `json:"reporter"`
	RootItemKey  string `json:"root_item_key"`
	Title        string `json:"title"`
	BoardItemKey string `json:"board_item_key"`
	IssueURL     string `json:"issue_url"`
}

// bugDetailWire is one report with every field swarm_report_bug stored.
type bugDetailWire struct {
	bugWire
	WhatHappened   string `json:"what_happened"`
	Repro          string `json:"repro"`
	Evidence       string `json:"evidence"`
	UserSaid       string `json:"user_said"`
	Cause          string `json:"cause"`
	Area           string `json:"area"`
	TranscriptPath string `json:"transcript_path"`
}

// listBugs returns the stored swarm_report_bug reports, newest first.
func (s *Server) listBugs(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	rows, err := s.RT.DB.QueryContext(r.Context(), `SELECT id, created_at, reporter_agent_name, root_item_key, title,
		COALESCE(board_item_key, ''), issue_url FROM bug_reports ORDER BY created_at DESC, id DESC`)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []bugWire{}
	for rows.Next() {
		var b bugWire
		if err := rows.Scan(&b.ID, &b.CreatedAt, &b.Reporter, &b.RootItemKey, &b.Title, &b.BoardItemKey, &b.IssueURL); err != nil {
			s.writeErr(w, err)
			return
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getBug returns one stored report with all its fields.
func (s *Server) getBug(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	var b bugDetailWire
	err := s.RT.DB.QueryRowContext(r.Context(), `SELECT id, created_at, reporter_agent_name, root_item_key, title,
		COALESCE(board_item_key, ''), issue_url, what_happened, repro, evidence, user_said, cause, area, transcript_path
		FROM bug_reports WHERE id = ?`, r.PathValue("id")).Scan(&b.ID, &b.CreatedAt, &b.Reporter, &b.RootItemKey,
		&b.Title, &b.BoardItemKey, &b.IssueURL, &b.WhatHappened, &b.Repro, &b.Evidence, &b.UserSaid, &b.Cause,
		&b.Area, &b.TranscriptPath)
	if errors.Is(err, sql.ErrNoRows) {
		s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "No bug report "+r.PathValue("id")+"."))
		return
	}
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

// setBugIssue records the GitHub issue URL a report was filed as. It only
// writes while the URL is empty, so a report can't be filed twice.
func (s *Server) setBugIssue(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	var body struct {
		IssueURL string `json:"issue_url"`
	}
	if err := readJSON(r, &body); err != nil {
		s.writeErr(w, err)
		return
	}
	body.IssueURL = strings.TrimSpace(body.IssueURL)
	if body.IssueURL == "" {
		s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "issue_url is required."))
		return
	}
	id := r.PathValue("id")
	res, err := s.RT.DB.ExecContext(r.Context(), `UPDATE bug_reports SET issue_url = ? WHERE id = ? AND issue_url = ''`, body.IssueURL, id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var cur string
		err := s.RT.DB.QueryRowContext(r.Context(), `SELECT issue_url FROM bug_reports WHERE id = ?`, id).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) {
			s.writeErr(w, apiErr(http.StatusNotFound, "not_found", "No bug report "+id+"."))
			return
		}
		if err != nil {
			s.writeErr(w, err)
			return
		}
		s.writeErr(w, apiErr(http.StatusConflict, "conflict", "Bug report "+id+" is already filed: "+cur))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "issue_url": body.IssueURL})
}
