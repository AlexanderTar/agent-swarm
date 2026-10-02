package httpapi

import "net/http"

func (s *Server) bugRoutes() []route {
	return []route{{"GET", "/api/bugs", authDaemon, s.listBugs}}
}

type bugWire struct {
	ID           string `json:"id"`
	CreatedAt    int64  `json:"created_at"`
	Reporter     string `json:"reporter"`
	RootItemKey  string `json:"root_item_key"`
	Title        string `json:"title"`
	BoardItemKey string `json:"board_item_key"`
}

// listBugs returns the stored swarm_report_bug reports, newest first.
func (s *Server) listBugs(w http.ResponseWriter, r *http.Request) {
	if s.rtNotWired(w) {
		return
	}
	rows, err := s.RT.DB.QueryContext(r.Context(), `SELECT id, created_at, reporter_agent_name, root_item_key, title,
		COALESCE(board_item_key, '') FROM bug_reports ORDER BY created_at DESC, id DESC`)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	defer rows.Close()
	out := []bugWire{}
	for rows.Next() {
		var b bugWire
		if err := rows.Scan(&b.ID, &b.CreatedAt, &b.Reporter, &b.RootItemKey, &b.Title, &b.BoardItemKey); err != nil {
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
