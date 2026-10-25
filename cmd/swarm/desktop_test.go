package main

import "testing"

// macOS leaves windows to the menu bar and keeps BaseEnv; Linux opens them
// from the daemon and uses LinuxBaseEnv.
func TestDesktopForPicksThePlatformPieces(t *testing.T) {
	none := func(string) string { return "" }
	mac := desktopFor("darwin")
	if mac.terminal != nil || mac.baseEnv(none)["LANG"] != "en_US.UTF-8" {
		t.Errorf("darwin: terminal set = %v, LANG = %q", mac.terminal != nil, mac.baseEnv(none)["LANG"])
	}
	linux := desktopFor("linux")
	if linux.terminal == nil || linux.baseEnv(none)["LANG"] != "C.UTF-8" {
		t.Errorf("linux: terminal set = %v, LANG = %q", linux.terminal != nil, linux.baseEnv(none)["LANG"])
	}
}
