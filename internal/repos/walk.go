package repos

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

var skipNames = map[string]bool{
	"node_modules": true, "vendor": true, "Pods": true, "DerivedData": true, ".build": true,
	"build": true, "dist": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, "Carthage": true, "bower_components": true,
}

type WalkResult struct {
	Repos      []string            // real paths of folders whose .git is a directory
	LinkDirs   map[string][]string // folder → real targets of its directory symlinks
	Workspaces []string            // *.code-workspace files
	Errors     int                 // unreadable folders
}

// ExpandHome resolves "~" and relative paths against home.
func ExpandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	}
	return filepath.Join(home, p)
}

// Walk scans home without following symlinks (§12.3).
func Walk(home string, excludes []string) WalkResult {
	w, _ := walk(context.Background(), home, excludes)
	return w
}

// walk is Walk that stops with ctx; a cancelled walk returns ctx's error and no repos.
func walk(ctx context.Context, home string, excludes []string) (WalkResult, error) {
	skip := map[string]bool{
		filepath.Join(home, "Library"):  true,
		filepath.Join(home, ".Trash"):   true,
		filepath.Join(home, "Music"):    true,
		filepath.Join(home, "Pictures"): true,
		filepath.Join(home, "Movies"):   true,
	}
	for _, e := range excludes {
		skip[ExpandHome(e, home)] = true
	}
	res := WalkResult{LinkDirs: map[string][]string{}}
	seen := map[string]bool{}
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			res.Errors++
			if d != nil && d.IsDir() && path != home {
				return fs.SkipDir
			}
			return nil
		}
		if path != home && (skipNames[d.Name()] || skip[path]) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path != home && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if target, err := filepath.EvalSymlinks(path); err == nil {
				if fi, err := os.Stat(target); err == nil && fi.IsDir() {
					if !isSkipped(target, skip) {
						parent := filepath.Dir(path)
						res.LinkDirs[parent] = append(res.LinkDirs[parent], target)
					}
				}
			}
			return nil
		}
		if !d.IsDir() {
			if strings.HasSuffix(d.Name(), ".code-workspace") {
				res.Workspaces = append(res.Workspaces, path)
			}
			return nil
		}
		if IsRepo(path) {
			if real, err := filepath.EvalSymlinks(path); err == nil && !seen[real] {
				seen[real] = true
				res.Repos = append(res.Repos, real)
			}
		}
		return nil
	})
	if err != nil {
		return WalkResult{}, err
	}
	sort.Strings(res.Repos)
	return res, nil
}

func isSkipped(p string, skip map[string]bool) bool {
	if skip[p] {
		return true
	}
	for s := range skip {
		if strings.HasPrefix(p, s+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type Group struct {
	Name   string
	Source string // workspace_dir | code_workspace | remote_owner
	Origin string // folder or .code-workspace path; "" for remote_owner
	Repos  []string
}

var sourceOrder = map[string]int{"workspace_dir": 0, "code_workspace": 1, "remote_owner": 2}

// Groups derives §12.3 groups. owners maps repo path → remote owner.
func Groups(w WalkResult, owners map[string]string) []Group {
	known := map[string]bool{}
	for _, r := range w.Repos {
		known[r] = true
	}
	var out []Group
	add := func(g Group, members []string) {
		slices.Sort(members)
		members = slices.Compact(members)
		if len(members) > 0 {
			g.Repos = members
			out = append(out, g)
		}
	}
	for dir, targets := range w.LinkDirs {
		var members []string
		for _, t := range targets {
			if known[t] {
				members = append(members, t)
			}
		}
		add(Group{Name: filepath.Base(dir), Source: "workspace_dir", Origin: dir}, members)
	}
	for _, f := range w.Workspaces {
		var members []string
		for _, p := range codeWorkspaceFolders(f) {
			main, ok := MainRepoOf(p)
			if !ok {
				continue
			}
			if real, err := filepath.EvalSymlinks(main); err == nil && known[real] {
				members = append(members, real)
			}
		}
		add(Group{Name: strings.TrimSuffix(filepath.Base(f), ".code-workspace"), Source: "code_workspace", Origin: f}, members)
	}
	byOwner := map[string][]string{}
	for path, owner := range owners {
		if owner != "" && known[path] {
			byOwner[owner] = append(byOwner[owner], path)
		}
	}
	for owner, members := range byOwner {
		if len(members) >= 2 {
			add(Group{Name: owner, Source: "remote_owner"}, members)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if sourceOrder[out[i].Source] != sourceOrder[out[j].Source] {
			return sourceOrder[out[i].Source] < sourceOrder[out[j].Source]
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Origin < out[j].Origin
	})
	return out
}

// codeWorkspaceFolders reads folders[].path, resolved relative to the file. Invalid JSON yields nothing.
func codeWorkspaceFolders(file string) []string {
	body, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var doc struct {
		Folders []struct {
			Path string `json:"path"`
		} `json:"folders"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	var out []string
	for _, f := range doc.Folders {
		p := f.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(file), p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}
