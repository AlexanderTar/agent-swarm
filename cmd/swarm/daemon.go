package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/advisor"
	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/hook"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/mcpserver"
	"github.com/AlexanderTar/agent-swarm/internal/notify"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
	"github.com/AlexanderTar/agent-swarm/internal/spawn"
	usagesvc "github.com/AlexanderTar/agent-swarm/internal/usage"
	"github.com/AlexanderTar/agent-swarm/internal/usagegate"
	"github.com/AlexanderTar/agent-swarm/internal/worktree"
	"github.com/AlexanderTar/agent-swarm/web"
)

const (
	retention    = 7 * 24 * time.Hour // events (A6) and idempotency rows (I11)
	kbResyncEach = 5 * time.Minute    // backstop for missed fsnotify events
)

type daemonConfig struct {
	Home       string
	Port       int
	Background bool        // start the KB, repo, catalog and prune loops
	ScanRoot   string      // folder scanned for repos; "" means the user's home
	Embedder   kb.Embedder // nil means Ollama
	Log        func(format string, args ...any)
	Ready      func(addr string)
	// Dev enables POST /api/dev/seed (Task 39). It does nothing else: usage
	// polling is gated by SWARM_USAGE=live alone (S-4), never by this flag,
	// so a daemon someone forgets to pass --dev to is never the difference
	// between polling the real world or not.
	Dev bool

	grace time.Duration           // shutdown budget; 0 means 5 s
	loops []func(context.Context) // extra background loops (tests)
}

// lookTmux resolves the absolute tmux path once at startup; every spawn and
// the Ghostty fallback use this string, never a bare "tmux" that PATH could
// resolve differently later.
func lookTmux() string {
	if p, err := exec.LookPath("tmux"); err == nil {
		return p
	}
	return "tmux"
}

// selfPath is the absolute path to this binary: agents need it in their hook
// and MCP configs, which is why it is resolved once, not left as os.Args[0].
func selfPath() string {
	bin, err := os.Executable()
	if err != nil {
		return "swarm"
	}
	if real, err := filepath.EvalSymlinks(bin); err == nil {
		return real
	}
	return bin
}

type logf = func(format string, args ...any)

func cmdDaemon(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("daemon", stderr, false)
	port := fs.Int("port", 7777, "port on 127.0.0.1")
	noBackground := fs.Bool("no-background", false, "skip KB indexing, repo scans and catalog refresh")
	dev := fs.Bool("dev", false, "enable POST /api/dev/seed (make dev only; never gates usage polling, S-4)")
	if code, done := parse(fs, args); done {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serve(ctx, daemonConfig{Home: *home, Port: *port, Background: !*noBackground, Dev: *dev,
		Log:   log.New(stderr, "", log.LstdFlags).Printf,
		Ready: func(addr string) { fmt.Fprintf(stdout, "swarm daemon listening on http://%s\n", addr) }})
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}

// loadOrCreateToken reuses the token file, creating it (0600) when missing or empty.
func loadOrCreateToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("Can't read the daemon token (%s): %w", path, err)
	}
	if tok := strings.TrimSpace(string(b)); tok != "" {
		return tok, os.Chmod(path, 0o600) // tighten a token copied in with a looser mode
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, os.Chmod(path, 0o600) // WriteFile keeps the mode of an existing file
}

// daemon holds the wired services; the caller closes db.
type daemon struct {
	cfg daemonConfig
	db  *db.DB
	ev  *events.Store
	cat *catalog.Service
	st  *settings.Store
	rp  *repos.Service
	idx *kb.Index
	api *httpapi.Server
	rt  *runtime.Store
	up  *usagesvc.Poller
}

// openDaemon prepares the home folder, opens the database and wires every service.
func openDaemon(ctx context.Context, cfg daemonConfig) (*daemon, error) {
	for _, dir := range []struct {
		path string
		mode os.FileMode
	}{{cfg.Home, 0o755}, {filepath.Join(cfg.Home, "run"), 0o700}, {filepath.Join(cfg.Home, "logs"), 0o755}, {filepath.Join(cfg.Home, "kb"), 0o755}} {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return nil, err
		}
	}
	// only run/ holds secrets: force its mode, and leave a home the user tightened alone
	if err := os.Chmod(filepath.Join(cfg.Home, "run"), 0o700); err != nil {
		return nil, err
	}
	d, err := db.Open(ctx, filepath.Join(cfg.Home, "swarm.db")) // refuses 1.x data before writing a token
	if err != nil {
		return nil, err
	}
	token, err := loadOrCreateToken(filepath.Join(cfg.Home, "run", "daemon.token"))
	if err == nil && token == "" { // httpapi.New panics on an empty token
		err = errors.New("the daemon token is empty")
	}
	if err != nil {
		d.Close()
		return nil, err
	}
	userHome, _ := os.UserHomeDir()
	if cfg.ScanRoot == "" {
		cfg.ScanRoot = userHome
	}
	if cfg.Embedder == nil {
		cfg.Embedder = kb.NewOllama()
	}
	if cfg.Log == nil {
		cfg.Log = log.Printf
	}

	now := time.Now
	ev := events.New(d, now)
	cat := &catalog.Service{DB: d, Events: ev, Fetchers: catalog.DefaultFetchers(userHome, os.Getenv("USER")), Now: now, Log: cfg.Log, Home: userHome}
	st := &settings.Store{DB: d, Events: ev, Now: now, ModelsFor: cat.ModelsFor, Installed: cat.Installed}
	rp := &repos.Service{DB: d, Events: ev, Run: execx.Run, Home: cfg.ScanRoot, Now: now, Log: cfg.Log,
		Excludes: func(ctx context.Context) []string {
			s, err := st.Get(ctx)
			if err != nil {
				cfg.Log("scan excludes: %v", err)
				return nil
			}
			return s.ScanExcludes
		}}
	idx := &kb.Index{DB: d, Dir: filepath.Join(cfg.Home, "kb"), Emb: cfg.Embedder, Now: now}
	if err := idx.Load(ctx); err != nil {
		d.Close()
		return nil, err
	}

	// tmux.conf is (re)written whenever it is missing or stale, so an upgrade
	// that changes TmuxConf() reaches an existing ~/.swarm without a reinstall.
	confPath := filepath.Join(cfg.Home, "tmux.conf")
	want := spawn.TmuxConf()
	if got, err := os.ReadFile(confPath); err != nil || string(got) != string(want) {
		if err := os.WriteFile(confPath, want, 0o644); err != nil {
			d.Close()
			return nil, err
		}
	}
	for _, dir := range []string{filepath.Join(cfg.Home, "work"), filepath.Join(cfg.Home, "run", "tokens"), filepath.Join(cfg.Home, "run", "advice"), filepath.Join(cfg.Home, "worktrees")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			d.Close()
			return nil, err
		}
	}

	it := &items.Store{DB: d, Events: ev, Now: now}

	// R9 / safety invariant S-1: the socket name comes from spawn.SocketFromEnv,
	// whose default is swarm-test-<pid>. Production is opt-in — the launchd
	// plist this task writes sets SWARM_TMUX_SOCKET=swarm — so a daemon started
	// by hand from a worktree, by a test, or by `make dev` can never reach the
	// live server.
	spawner := &spawn.Spawner{Socket: spawn.SocketFromEnv(os.Getenv), Conf: confPath,
		Tmux: lookTmux(), Run: execx.Run, Log: cfg.Log}
	wt := &worktree.Service{DB: d, Run: execx.Run, Now: now, Log: cfg.Log, Home: cfg.Home}
	nt := &notify.Service{DB: d, Events: ev, Now: now, Log: cfg.Log}
	adDeps := adapter.Deps{Home: cfg.Home, UserHome: userHome, Bin: selfPath(), Run: execx.Run,
		Start: execx.Start, Now: now, Log: cfg.Log}
	rt := &runtime.Store{DB: d, Events: ev, Items: it, Repos: rp, Settings: st, Catalog: cat,
		Home: cfg.Home, Now: now, Log: cfg.Log, Tmux: spawner, Worktree: wt, Notify: nt,
		Bin: selfPath(), DaemonURL: fmt.Sprintf("http://127.0.0.1:%d", cfg.Port),
		OSEnv: os.Getenv, BaseEnv: spawn.BaseEnv,
		// The two strings `swarm attach` and the Ghostty fallback need, so
		// neither writes `-L swarm` as a literal (safety invariant S-1, R11).
		// They come from the same Spawner the daemon spawns through, so they
		// can never disagree.
		TmuxPath: spawner.Tmux, TmuxSocketName: spawner.Socket}
	adDeps.PublishWake = rt.PublishWake // the claude channel bridge
	rt.Adapters = adapter.All(adDeps)
	adv := &advisor.Service{DB: d, Events: ev, Home: cfg.Home, UserHome: userHome,
		Adapters: rt.Adapters, Run: execx.Run, Now: now, Log: cfg.Log,
		MaxConcurrent: 2, Timeout: 240 * time.Second, Deliver: rt.DeliverAdvice}
	rt.Advisor = adv
	wt.OnRetained = rt.OnWorktreeRetained // the §17.5 "Worktree kept" notification
	it.RequestPayload = rt.RequestPayload // R5: full Request on request.*
	it.RequestOpened = rt.OnRequestOpened // §17.5 for the daemon-opened accept requests
	it.DepUnblocked = rt.OnDepUnblocked   // wake whatever was blocked_by an item that just finished
	// Safety invariant S-4: SourcesFromEnv returns nil unless SWARM_USAGE=live,
	// which only the installed launchd plist sets. Every other daemon — a
	// test's, e2e's, `make dev`'s, one started by hand in a worktree — gets no
	// sources, so it reads no keychain and calls no vendor endpoint. This is
	// the only call site, and cfg.Dev never enters this decision (F1).
	up := &usagesvc.Poller{DB: d, Events: ev, Settings: st, Now: now, Log: cfg.Log,
		Sources: usagesvc.SourcesFromEnv(os.Getenv, userHome, os.Getenv("USER"),
			&http.Client{Timeout: 10 * time.Second}, execx.Run, execx.Start)}
	// docs/specs/2026-09-19-usage-fallback-agent.md: rt.Usage lets the
	// spawn/retry path substitute a configured fallback agent when the
	// configured one is confirmed out of usage. usagegate depends on both
	// runtime and usage, so this is the one place that can wire the two
	// together without either package importing the other.
	rt.Usage = &usagegate.Gate{Poller: up, Now: now}
	mcpsrv := &mcpserver.Server{RT: rt, KB: idx, Advisor: adv, Log: cfg.Log, Version: version}
	hookH := &hook.Handler{DB: d, RT: rt, Adapters: rt.Adapters, Now: now, Log: cfg.Log, Advisor: adv}

	api := httpapi.New(httpapi.Deps{Version: version, Token: token, DB: d, Events: ev,
		Items: it, Repos: rp, Settings: st, Catalog: cat, KB: idx, Log: cfg.Log,
		RT: rt, Notify: nt, Usage: up, Advisor: adv, MCP: mcpsrv, Hook: hookH, Dev: cfg.Dev,
		Run: execx.Run, After: time.After, Web: web.Handler(web.Dist)})
	return &daemon{cfg: cfg, db: d, ev: ev, cat: cat, st: st, rp: rp, idx: idx, api: api,
		rt: rt, up: up}, nil
}

func serve(ctx context.Context, cfg daemonConfig) error {
	dm, err := openDaemon(ctx, cfg)
	if err != nil {
		return err
	}
	defer dm.db.Close()
	cfg = dm.cfg
	if cfg.grace == 0 {
		cfg.grace = 5 * time.Second
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.Port))
	if err != nil {
		return err
	}
	loopCtx, stopLoops := context.WithCancel(ctx)
	defer stopLoops()
	dm.rp.Ctx = loopCtx // shared scans (loop and rescan requests) stop at shutdown
	loops := cfg.loops
	if cfg.Background {
		loops = append(loops,
			func(ctx context.Context) {
				if err := dm.idx.Sync(ctx); err != nil && ctx.Err() == nil {
					cfg.Log("kb sync: %v", err)
				}
				if err := dm.idx.Watch(ctx, 500*time.Millisecond); err != nil {
					cfg.Log("kb watch: %v", err)
				}
			},
			func(ctx context.Context) { syncLoop(ctx, dm.idx, kbResyncEach, cfg.Log) },
			func(ctx context.Context) {
				dm.rp.Loop(ctx, func(ctx context.Context) time.Duration {
					s, err := dm.st.Get(ctx)
					if err != nil || s.ScanIntervalSec < 3600 {
						return 6 * time.Hour
					}
					return time.Duration(s.ScanIntervalSec) * time.Second
				})
			},
			dm.cat.Loop,
			func(ctx context.Context) { pruneLoop(ctx, dm.ev, dm.db, cfg.Log) },
			func(ctx context.Context) { dm.rt.ReconcileLoop(ctx, 5*time.Second) }, // §10.6
			func(ctx context.Context) { dm.rt.WakeLoop(ctx, 5*time.Second) },      // §11.3
			func(ctx context.Context) { dm.up.Loop(ctx, nil) },                    // §13 (a no-op with no sources, S-4)
		)
	}
	var bg sync.WaitGroup
	var running atomic.Int32
	for _, loop := range loops {
		running.Add(1)
		bg.Go(func() { defer running.Add(-1); loop(loopCtx) })
	}

	hs := &http.Server{Handler: dm.api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if cfg.Ready != nil {
		cfg.Ready(ln.Addr().String())
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	var serveErr error
	select {
	case serveErr = <-errc:
	case <-ctx.Done():
	}
	deadline := time.Now().Add(cfg.grace)
	stopLoops()
	if serveErr == nil {
		shutdown, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()
		dm.api.Close() // SSE streams never go idle; end them so Shutdown can finish
		if err := hs.Shutdown(shutdown); err != nil {
			hs.Close()
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	}
	// wait for the loops before the db closes, but never past the grace period
	loopsDone := make(chan struct{})
	go func() { bg.Wait(); close(loopsDone) }()
	select {
	case <-loopsDone:
	case <-time.After(time.Until(deadline)):
		if running.Load() > 0 { // the timer can win the race against loops that just finished
			cfg.Log("shutdown: background work still running after the grace period")
		}
	}
	return serveErr
}

// syncLoop re-syncs the knowledge base on a fixed period, alongside Watch.
func syncLoop(ctx context.Context, idx *kb.Index, every time.Duration, log logf) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := idx.Sync(ctx); err != nil && ctx.Err() == nil {
				log("kb sync: %v", err)
			}
		}
	}
}

func pruneLoop(ctx context.Context, ev *events.Store, d *db.DB, log logf) {
	for {
		prune(ctx, ev, d, time.Now(), log)
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}

// prune drops events and idempotency rows older than the retention window.
func prune(ctx context.Context, ev *events.Store, d *db.DB, now time.Time, log logf) {
	if _, err := ev.Prune(ctx, retention); err != nil {
		log("prune events: %v", err)
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM idempotency WHERE created_at < ?`,
		db.Millis(now.Add(-retention))); err != nil {
		log("prune idempotency: %v", err)
	}
}
