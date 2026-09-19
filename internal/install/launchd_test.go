package install

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/AlexanderTar/agent-swarm/internal/execx"
	_ "modernc.org/sqlite"
)

var bg = context.Background()

func TestLaunchPath(t *testing.T) {
	got := LaunchPath("~/bin:/Users/a/.volta/bin::relative/bin:/usr/bin:~/bin:/opt/homebrew/bin/", "/Users/a")
	want := "/Users/a/bin:/Users/a/.volta/bin:/usr/bin:/opt/homebrew/bin:/usr/local/bin:/bin:/usr/sbin:/sbin"
	if got != want {
		t.Fatalf("LaunchPath = %q", got)
	}
}

func testConfig(t *testing.T) Config {
	dir := t.TempDir()
	return Config{Bin: "/Users/a/.swarm/bin/swarm", Home: filepath.Join(dir, "swarm-home"),
		LaunchAgentsDir: filepath.Join(dir, "LaunchAgents"), Path: "/Users/a/bin:/usr/bin&x", UID: 501,
		User: "a", UserHome: "/Users/a"}
}

func TestPlistContent(t *testing.T) {
	c := testConfig(t)
	p := Plist(c)
	if err := xml.Unmarshal(p, new(struct{ XMLName xml.Name })); err != nil {
		t.Fatalf("plist is not well-formed XML: %v\n%s", err, p)
	}
	for _, want := range []string{
		"<key>Label</key><string>dev.swarm.daemon</string>",
		"<array><string>/Users/a/.swarm/bin/swarm</string><string>daemon</string><string>--home</string><string>" + c.Home + "</string></array>",
		"<key>PATH</key><string>/Users/a/bin:/usr/bin&amp;x</string>",
		"<key>SWARM_HOME</key><string>" + c.Home + "</string>",
		"<key>StandardErrorPath</key><string>" + c.Home + "/logs/daemon.err.log</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>SuccessfulExit</key><false/>",
		"<key>USER</key><string>a</string>",
		"<key>HOME</key><string>/Users/a</string>",
	} {
		if !bytes.Contains(p, []byte(want)) {
			t.Errorf("plist lacks %s\n%s", want, p)
		}
	}
	c.User, c.UserHome = "", ""
	if p := Plist(c); bytes.Contains(p, []byte("<key>USER</key>")) || bytes.Contains(p, []byte("<key>HOME</key>")) {
		t.Errorf("empty USER/HOME must be omitted\n%s", p)
	}
	if PlistPath(c) != filepath.Join(c.LaunchAgentsDir, "dev.swarm.daemon.plist") {
		t.Error("PlistPath")
	}
}

// The plist is the one place SWARM_TMUX_SOCKET=swarm and SWARM_USAGE=live
// appear (P2 T35, R9, S-1, S-4). Drop either and the installed daemon quietly
// stops working; add either anywhere else and those invariants are gone.
func TestPlistCarriesTheProductionEnvironment(t *testing.T) {
	p := Plist(testConfig(t))
	for _, want := range []string{
		"<key>SWARM_TMUX_SOCKET</key><string>swarm</string>",
		"<key>SWARM_USAGE</key><string>live</string>",
	} {
		if !bytes.Contains(p, []byte(want)) {
			t.Errorf("the plist is missing %s\n%s", want, p)
		}
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	c := testConfig(t)
	f := &execx.Fake{}
	var out bytes.Buffer
	if err := Install(bg, c, f.Runner(), true, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.LaunchAgentsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry run created the LaunchAgents folder")
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("dry run ran %v", f.Calls())
	}
	s := out.String()
	for _, want := range []string{"Would write " + PlistPath(c), "<key>PATH</key>",
		"launchctl bootout gui/501/dev.swarm.daemon", "launchctl bootstrap gui/501 " + PlistPath(c)} {
		if !strings.Contains(s, want) {
			t.Errorf("dry-run output lacks %q:\n%s", want, s)
		}
	}
}

func TestInstallWritesPlistAndBootstraps(t *testing.T) {
	c := testConfig(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon":  {Err: errors.New("exit 3: no such process")},
		"launchctl bootstrap gui/501 " + PlistPath(c): {},
	}}
	var out bytes.Buffer
	if err := Install(bg, c, f.Runner(), false, &out); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(PlistPath(c))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("plist: %v %v", fi, err)
	}
	if got, _ := os.ReadFile(PlistPath(c)); !bytes.Equal(got, Plist(c)) {
		t.Fatal("plist content differs")
	}
	if fi, err := os.Stat(filepath.Join(c.Home, "run")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("run dir: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(c.Home, "logs")); err != nil {
		t.Fatal("logs dir missing")
	}
	want := []string{"launchctl bootout gui/501/dev.swarm.daemon", "launchctl bootstrap gui/501 " + PlistPath(c)}
	if !slices.Equal(f.Calls(), want) {
		t.Fatalf("calls = %v", f.Calls())
	}
	if !strings.Contains(out.String(), "Installed "+PlistPath(c)) {
		t.Fatalf("output = %s", out.String())
	}

	f.Responses["launchctl bootstrap gui/501 "+PlistPath(c)] = execx.Result{Err: errors.New("exit 5: input/output error")}
	if err := Install(bg, c, f.Runner(), false, io.Discard); err == nil || !strings.Contains(err.Error(), "launchctl bootstrap") {
		t.Fatalf("bootstrap failure: %v", err)
	}
}

func legacyDB(t *testing.T, home string) {
	t.Helper()
	os.MkdirAll(home, 0o755)
	d, err := sql.Open("sqlite", "file:"+filepath.Join(home, "swarm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE schema_meta (version INTEGER)`); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesLegacyData(t *testing.T) {
	c := testConfig(t)
	if HasLegacyData(filepath.Join(c.Home, "swarm.db")) {
		t.Fatal("missing file is not legacy data")
	}
	legacyDB(t, c.Home)
	if !HasLegacyData(filepath.Join(c.Home, "swarm.db")) {
		t.Fatal("schema_meta must be detected")
	}
	for _, dry := range []bool{true, false} {
		err := Install(bg, c, (&execx.Fake{}).Runner(), dry, io.Discard)
		if err == nil || err.Error() != "Agent Swarm 1.x data found. Run `swarm migrate` first." {
			t.Fatalf("dry=%v: %v", dry, err)
		}
	}
	if _, err := os.Stat(c.LaunchAgentsDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused install wrote files")
	}
}

func TestInstallBootoutErrors(t *testing.T) {
	notLoaded := []error{
		errors.New("launchctl: exit status 3: Boot-out failed: 3: No such process"),
		errors.New("launchctl: exit status 113: Could not find service \"dev.swarm.daemon\" in domain for port"),
	}
	exit3 := exec.Command("sh", "-c", "exit 3").Run() // a real *exec.ExitError with code 3
	notLoaded = append(notLoaded, fmt.Errorf("launchctl: %w: ", exit3))
	for _, e := range notLoaded {
		c := testConfig(t)
		f := &execx.Fake{Responses: map[string]execx.Result{
			"launchctl bootout gui/501/dev.swarm.daemon":  {Err: e},
			"launchctl bootstrap gui/501 " + PlistPath(c): {},
		}}
		if err := Install(bg, c, f.Runner(), false, io.Discard); err != nil {
			t.Errorf("%v: %v", e, err)
		}
	}

	c := testConfig(t)
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon": {Err: errors.New("launchctl: exit status 1: Operation not permitted")},
	}}
	err := Install(bg, c, f.Runner(), false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "launchctl bootout") || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("bootout failure: %v", err)
	}
	if want := []string{"launchctl bootout gui/501/dev.swarm.daemon"}; !slices.Equal(f.Calls(), want) {
		t.Fatalf("bootstrap ran after bootout failed: %v", f.Calls())
	}
}

func TestInstallTightensRunDir(t *testing.T) {
	c := testConfig(t)
	run := filepath.Join(c.Home, "run")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(run, 0o755); err != nil {
		t.Fatal(err)
	}
	f := &execx.Fake{Responses: map[string]execx.Result{
		"launchctl bootout gui/501/dev.swarm.daemon":  {},
		"launchctl bootstrap gui/501 " + PlistPath(c): {},
	}}
	if err := Install(bg, c, f.Runner(), false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(run); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("run dir: %v %v", fi, err)
	}
}

// internal/migrate needs the same not-loaded judgement for its step 2 bootout
// (§20), so notLoaded is exported rather than duplicated.
func TestNotLoadedIsExportedForMigrate(t *testing.T) {
	if !NotLoaded(errors.New("Could not find service \"dev.swarm.updater\"")) {
		t.Error("a missing service must count as not loaded")
	}
	if NotLoaded(errors.New("Operation not permitted")) {
		t.Error("a real failure must not count as not loaded")
	}
}
