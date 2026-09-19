package hook

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AttrReason and SignReason are the §17.3 sentences.
const AttrReason = "Remove agent attribution; commits are made on the user's behalf only."
const SignReason = "Don't disable commit signing."

var (
	// Only git and gh write commands are inspected (§11.2).
	watched = regexp.MustCompile(`\b(git\s+(commit|merge|tag)|gh\s+pr\s+(create|edit))\b|\bgit\s+-c\b`)
	// Trailers arrive in both spellings (P0-7), so the check is case-insensitive.
	trailer   = regexp.MustCompile(`(?i)co-authored-by:`)
	generated = regexp.MustCompile(`Generated with \[|Generated with Claude|Generated with Codex|\x{1F916}`)
	noSign    = regexp.MustCompile(`--no-gpg-sign|commit\.gpgsign\s*=\s*false`)
	// -F/--file/--body-file paths, quoted or bare.
	filePaths = regexp.MustCompile(`(?:-F|--file|--body-file)(?:[=\s]+)("[^"]+"|'[^']+'|\S+)`)
)

type AttrCheck struct {
	ReadFile func(string) ([]byte, error)
}

// Block reports whether a shell command must be denied, with the §17.3 reason.
// A -F/--file/--body-file path is resolved against the hook's cwd and read now
// (P0-7); "-F -" is covered by the command text itself.
func (a AttrCheck) Block(command, cwd string) (bool, string) {
	if !watched.MatchString(command) {
		return false, ""
	}
	if noSign.MatchString(command) {
		return true, SignReason
	}
	if trailer.MatchString(command) || generated.MatchString(command) {
		return true, AttrReason
	}
	read := a.ReadFile
	if read == nil {
		read = os.ReadFile
	}
	for _, m := range filePaths.FindAllStringSubmatch(command, -1) {
		p := strings.Trim(m[1], `"'`)
		if p == "-" {
			continue // the text is in the command, already checked above
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}
		b, err := read(p)
		if err != nil {
			continue
		}
		if trailer.Match(b) || generated.Match(b) {
			return true, AttrReason
		}
	}
	return false, ""
}
