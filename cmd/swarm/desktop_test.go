package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/lan"
)

// macOS leaves windows to the menu bar, keeps BaseEnv and reads sign-ins from
// the keychain; Linux opens windows from the daemon, uses LinuxBaseEnv and
// reads Claude's credentials file.
func TestPlatformForPicksThePlatformPieces(t *testing.T) {
	none := func(string) string { return "" }
	mac := platformFor("darwin", "/Users/a")
	if mac.terminal != nil || mac.baseEnv(none)["LANG"] != "en_US.UTF-8" || mac.creds.Where != "the keychain" {
		t.Errorf("darwin: terminal set = %v, LANG = %q, creds = %q", mac.terminal != nil, mac.baseEnv(none)["LANG"], mac.creds.Where)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	linux := platformFor("linux", "/home/a")
	if linux.terminal == nil || linux.baseEnv(none)["LANG"] != "C.UTF-8" || linux.creds.Where != "/home/a/.claude/.credentials.json" {
		t.Errorf("linux: terminal set = %v, LANG = %q, creds = %q", linux.terminal != nil, linux.baseEnv(none)["LANG"], linux.creds.Where)
	}
	// mDNS through dns-sd on macOS, avahi-publish on Linux.
	if _, ok := mac.registrar(nil).(*lan.DNSSD); !ok {
		t.Errorf("darwin registrar = %T", mac.registrar(nil))
	}
	if _, ok := linux.registrar(nil).(*lan.Avahi); !ok {
		t.Errorf("linux registrar = %T", linux.registrar(nil))
	}
	// Linux's media and snap folders are skipped there only: a Mac may keep repositories in ~/Videos.
	if len(mac.scanSkips) != 0 || !slices.Equal(linux.scanSkips, []string{"~/Videos", "~/snap"}) {
		t.Errorf("scan skips: darwin %q, linux %q", mac.scanSkips, linux.scanSkips)
	}
}

func TestLANWarningsNameThePlatformsFirewall(t *testing.T) {
	mac, linux := lanWarnings("darwin"), lanWarnings("linux")
	if !strings.Contains(mac, "System Settings > Network > Firewall") || strings.Contains(mac, "ufw") {
		t.Errorf("macOS warnings:\n%s", mac)
	}
	if !strings.Contains(linux, "sudo ufw allow <port>/tcp") || !strings.Contains(linux, "avahi-utils") || strings.Contains(linux, "macOS") {
		t.Errorf("Linux warnings:\n%s", linux)
	}
	for _, w := range []string{mac, linux} {
		if !strings.Contains(w, "This is plain HTTP.") {
			t.Errorf("the HTTP warning is missing:\n%s", w)
		}
	}
}
