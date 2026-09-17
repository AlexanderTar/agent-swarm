package items

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type ListFilter struct {
	View   string // "tree" (default) | "flat"
	Type   Type
	Status Status
	Q      string
	Root   string // top-level item key
}

// ftsQuery turns free text into an FTS5 query: every token quoted, prefix-matched, ANDed.
func ftsQuery(q string) string {
	var parts []string
	for _, tok := range strings.Fields(q) {
		tok = strings.ReplaceAll(tok, `"`, "")
		if strings.IndexFunc(tok, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			parts = append(parts, `"`+tok+`"*`)
		}
	}
	return strings.Join(parts, " ")
}

func keyNumber(key string) int {
	n, _ := strconv.Atoi(key[strings.LastIndex(key, "-")+1:])
	return n
}

func (s *Store) List(ctx context.Context, f ListFilter) ([]Item, int, error) {
	if f.View != "" && f.View != "tree" && f.View != "flat" {
		return nil, 0, errf(CodeBadRequest, "view must be tree or flat.")
	}
	if f.Type != "" && !slices.Contains([]Type{Epic, Story, Task, Bug, Spike}, f.Type) {
		return nil, 0, errf(CodeBadRequest, "Unknown item type %q.", f.Type)
	}
	if f.Status != "" && !slices.Contains(allStatuses, f.Status) {
		return nil, 0, errf(CodeBadRequest, "Unknown status %q.", f.Status)
	}
	where := []string{"i.archived_at IS NULL"}
	var args []any
	if f.Type != "" {
		where, args = append(where, "i.type = ?"), append(args, f.Type)
	}
	if f.Status != "" {
		where, args = append(where, "i.status = ?"), append(args, f.Status)
	}
	if f.Root != "" {
		where, args = append(where, "r.key = ?"), append(args, f.Root)
	}
	if q := ftsQuery(f.Q); q != "" {
		where, args = append(where, "i.rowid IN (SELECT rowid FROM items_fts WHERE items_fts MATCH ?)"), append(args, q)
	} else if strings.TrimSpace(f.Q) != "" {
		return []Item{}, 0, nil // only punctuation: nothing can match
	}
	matches, err := s.queryItems(ctx, s.DB, `SELECT `+itemCols+` WHERE `+strings.Join(where, " AND ")+
		` ORDER BY i.updated_at DESC`, args...)
	if err != nil {
		return nil, 0, err
	}
	if f.View == "flat" {
		return nonNilItems(matches), len(matches), nil
	}

	byID := map[string]Item{}
	for _, it := range matches {
		byID[it.ID] = it
	}
	var missing []string
	for _, it := range matches {
		if it.ParentID != "" {
			missing = append(missing, it.ParentID)
		}
	}
	for len(missing) > 0 {
		var next []string
		for _, id := range missing {
			if _, ok := byID[id]; ok {
				continue
			}
			p, err := s.getByID(ctx, s.DB, id)
			if err != nil {
				return nil, 0, err
			}
			p.Context = true
			byID[id] = p
			if p.ParentID != "" {
				next = append(next, p.ParentID)
			}
		}
		missing = next
	}
	var ctxItems []Item
	for _, it := range byID {
		if it.Context {
			ctxItems = append(ctxItems, it)
		}
	}
	if err := s.enrich(ctx, s.DB, ctxItems); err != nil {
		return nil, 0, err
	}
	for _, it := range ctxItems {
		byID[it.ID] = it
	}

	children := map[string][]Item{}
	var tops []Item
	for _, it := range byID {
		if it.ParentID == "" {
			tops = append(tops, it)
		} else {
			children[it.ParentID] = append(children[it.ParentID], it)
		}
	}
	sort.Slice(tops, func(i, j int) bool {
		if tops[i].Priority != tops[j].Priority {
			return tops[i].Priority < tops[j].Priority
		}
		return tops[i].CreatedAt.After(tops[j].CreatedAt)
	})
	out := []Item{}
	var walk func(it Item)
	walk = func(it Item) {
		out = append(out, it)
		kids := children[it.ID]
		sort.Slice(kids, func(i, j int) bool {
			if kids[i].SortOrder != kids[j].SortOrder {
				return kids[i].SortOrder < kids[j].SortOrder
			}
			return keyNumber(kids[i].Key) < keyNumber(kids[j].Key)
		})
		for _, k := range kids {
			walk(k)
		}
	}
	for _, t := range tops {
		walk(t)
	}
	return out, len(matches), nil
}

func nonNilItems(its []Item) []Item {
	if its == nil {
		return []Item{}
	}
	return its
}
