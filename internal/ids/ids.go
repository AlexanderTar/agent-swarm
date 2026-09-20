// Package ids makes prefixed ULIDs, per-type item keys and kebab-case names (§4).
package ids

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"
	"golang.org/x/text/unicode/norm"
)

const MaxName = 48

var ErrEmptyName = errors.New("Enter a name containing a letter or number.")

var prefixes = map[string]bool{
	"itm": true, "agt": true, "ses": true, "msg": true, "ckp": true, "wt": true,
	"req": true, "art": true, "ntf": true, "repo": true, "adv": true,
}

// New returns "<prefix>_<ULID>". ulid.Make is monotonic and goroutine-safe.
func New(prefix string) string {
	if !prefixes[prefix] {
		panic("ids: unknown prefix " + prefix)
	}
	return prefix + "_" + ulid.Make().String()
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// Kebab normalises a name to at most 48 characters.
func Kebab(s string) (string, error) { return KebabMax(s, MaxName) }

// KebabMax is Kebab with a custom length cap (default names use 24).
func KebabMax(s string, max int) (string, error) {
	var b strings.Builder
	for _, r := range norm.NFKD.String(s) {
		if r < 128 {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(b.String()), "-"), "-")
	if len(out) > max {
		if out[max] == '-' {
			out = out[:max]
		} else if i := strings.LastIndex(out[:max], "-"); i > 0 {
			out = out[:i]
		} else {
			out = out[:max]
		}
		out = strings.Trim(out, "-")
	}
	if out == "" {
		return "", ErrEmptyName
	}
	return out, nil
}

// Unique appends -2, -3, … until taken returns false, keeping 48 characters.
func Unique(base string, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		b := base
		if len(b)+len(suffix) > MaxName {
			b = strings.TrimRight(b[:MaxName-len(suffix)], "-")
		}
		if c := b + suffix; !taken(c) {
			return c
		}
	}
}

var keyTypes = map[string]bool{"epic": true, "story": true, "task": true, "bug": true, "spike": true, "chore": true}

// NextKey allocates the next "<TYPE>-<n>" inside the caller's transaction.
func NextKey(ctx context.Context, tx *sql.Tx, typ string) (string, error) {
	if !keyTypes[typ] {
		return "", fmt.Errorf("ids: unknown item type %q", typ)
	}
	var n int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO key_counters(type, next) VALUES (?, 2)
		 ON CONFLICT(type) DO UPDATE SET next = next + 1
		 RETURNING next - 1`, typ).Scan(&n)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(typ) + "-" + strconv.FormatInt(n, 10), nil
}
