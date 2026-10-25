package desktop

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// ErrNoSession: no live Wayland or X11 display to open a window on.
var ErrNoSession = errors.New("No desktop session to open a terminal in. Use swarm attach NAME instead.")

// sessionKeys are what a GUI program needs from the graphical session.
var sessionKeys = []string{"WAYLAND_DISPLAY", "DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR"}

// SessionEnv is the current graphical session's environment. A daemon
// started by systemd can predate the graphical login (linger) or outlive it,
// so its own environment may lack or misstate the display: the user manager's
// environment, which the desktop updates at login, is read at call time
// instead, and the daemon's own is the fallback when that read fails. A
// display only counts when its socket exists, since a desktop can leave stale
// values behind at logout; a dead one is left out of the result.
func (l Launcher) SessionEnv(ctx context.Context) (map[string]string, error) {
	vals := map[string]string{}
	if out, err := l.Run(ctx, "systemctl", "--user", "show-environment"); err == nil {
		vals = ParseEnvironment(string(out))
	} else {
		for _, k := range sessionKeys {
			if v := l.Getenv(k); v != "" {
				vals[k] = v
			}
		}
	}
	env := map[string]string{}
	for _, k := range sessionKeys {
		if v := vals[k]; v != "" {
			env[k] = v
		}
	}
	if w := env["WAYLAND_DISPLAY"]; w != "" && !l.Exists(waylandSocket(w, env["XDG_RUNTIME_DIR"])) {
		delete(env, "WAYLAND_DISPLAY")
	}
	if d := env["DISPLAY"]; d != "" && !l.Exists(x11Socket(d)) {
		delete(env, "DISPLAY")
	}
	if env["WAYLAND_DISPLAY"] == "" && env["DISPLAY"] == "" {
		return nil, ErrNoSession
	}
	return env, nil
}

// childEnv is base with every session key replaced by session's: a key the
// session lacks (a display found dead, an XAUTHORITY the desktop no longer
// sets) is removed, never left at the daemon's possibly stale value.
func childEnv(base []string, session map[string]string) []string {
	var out []string
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(sessionKeys, k) {
			out = append(out, kv)
		}
	}
	for _, k := range sessionKeys {
		if v, ok := session[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// waylandSocket is where a Wayland compositor listens: WAYLAND_DISPLAY is a
// name under XDG_RUNTIME_DIR, or an absolute path.
func waylandSocket(display, runtimeDir string) string {
	if filepath.IsAbs(display) {
		return display
	}
	if runtimeDir == "" {
		return ""
	}
	return filepath.Join(runtimeDir, display)
}

// x11Socket is the local X server's socket for DISPLAY=:N[.S]. A remote
// display (host:N) has no local socket to check, so it is taken on trust.
func x11Socket(display string) string {
	rest, ok := strings.CutPrefix(display, ":")
	if !ok {
		if strings.HasPrefix(display, "unix:") {
			rest = strings.TrimPrefix(display, "unix:")
		} else {
			return "/" // exists: trust a remote display
		}
	}
	n, _, _ := strings.Cut(rest, ".")
	if _, err := strconv.Atoi(n); err != nil {
		return ""
	}
	return "/tmp/.X11-unix/X" + n
}

// ParseEnvironment reads `systemctl show-environment`: one KEY=VALUE per
// line, the value printed as is or, when it holds anything a shell would
// read specially, quoted as $'…' with C escapes (systemd's
// shell_maybe_quote with SHELL_ESCAPE_POSIX).
func ParseEnvironment(out string) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		if inner, ok := strings.CutPrefix(v, "$'"); ok && strings.HasSuffix(inner, "'") {
			v = unescapeC(inner[:len(inner)-1])
		}
		env[k] = v
	}
	return env
}

// unescapeC undoes systemd's cescape: \a \b \f \n \r \t \v \\ \" \' and
// \NNN octal (\xHH too, for safety).
func unescapeC(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i+1 == len(s) {
			b.WriteByte(c)
			continue
		}
		i++
		switch e := s[i]; e {
		case 'a':
			b.WriteByte('\a')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'v':
			b.WriteByte('\v')
		case 'x':
			if i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteString(`\x`)
		default:
			if e >= '0' && e <= '7' && i+2 < len(s) {
				if n, err := strconv.ParseUint(s[i:i+3], 8, 8); err == nil {
					b.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			b.WriteByte(e)
		}
	}
	return b.String()
}
