package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// WriteIfChanged writes body to path atomically, creating parent folders, and only
// when the bytes differ from what is already there. Idempotence is a Global
// Constraint: swarm install runs many times and must not churn mtimes or modes.
// When the bytes already match but the mode has drifted (e.g. a synced
// skill's exec bit was reset by a tool that doesn't preserve it), the mode is
// still corrected: content-equal is not the same as fully in sync.
func WriteIfChanged(path string, body []byte, mode os.FileMode) (bool, error) {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, body) {
		if fi, err := os.Stat(path); err == nil && fi.Mode().Perm() != mode {
			return true, os.Chmod(path, mode)
		}
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".swarm-tmp"
	if err := os.WriteFile(tmp, body, mode); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	return true, os.Chmod(path, mode)
}

// EditJSON applies edit to the JSON object at path and writes the result back only
// if it changed. A missing file starts from an empty object when create is true and
// is left untouched otherwise. A file that does not parse is an error and is never
// overwritten: it is the user's configuration, and replacing it would lose data.
func EditJSON(path string, create bool, edit func(m map[string]any) error) (bool, error) {
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
	case os.IsNotExist(err) && create:
		old = []byte("{}")
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
	m := map[string]any{}
	if len(bytes.TrimSpace(old)) > 0 {
		if err := json.Unmarshal(old, &m); err != nil {
			return false, fmt.Errorf("%s: %w", path, err)
		}
	}
	if err := edit(m); err != nil {
		return false, err
	}
	next, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return false, err
	}
	next = append(next, '\n')
	return WriteIfChanged(path, next, 0o644)
}

var blankRun = regexp.MustCompile(`\n{3,}`)

// RemoveMarkedSpan deletes the lines from the first line containing start through
// the first line containing end after it, inclusive, and collapses the blank run
// that is left. Without both markers it changes nothing: guessing where an
// unterminated block ends could take the user's own text with it.
func RemoveMarkedSpan(text, start, end string) (string, bool) {
	lines := strings.Split(text, "\n")
	from := -1
	for i, l := range lines {
		if strings.Contains(l, start) {
			from = i
			break
		}
	}
	if from < 0 {
		return text, false
	}
	to := -1
	for i := from + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], end) {
			to = i
			break
		}
	}
	if to < 0 {
		return text, false
	}
	out := strings.Join(append(lines[:from:from], lines[to+1:]...), "\n")
	return blankRun.ReplaceAllString(out, "\n\n"), true
}

// RemoveLink removes path when it is a symlink, without following it (§20: the v1
// links are "removed as links, never followed"). A real file or directory is left
// alone and reported through wasRealDir so the caller can use the agent's own
// uninstall command instead.
func RemoveLink(path string) (removed bool, wasRealDir bool, err error) {
	fi, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return false, fi.IsDir(), nil
	}
	if err := os.Remove(path); err != nil {
		return false, false, err
	}
	return true, false, nil
}

// CopyFile copies src to dst, creating dst's parents. Used for the migration's
// config backups (§20 step 3).
func CopyFile(src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	fi, err := os.Stat(src)
	mode := os.FileMode(0o644)
	if err == nil {
		mode = fi.Mode().Perm()
	}
	return os.WriteFile(dst, body, mode)
}
