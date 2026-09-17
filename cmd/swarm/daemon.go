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
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/catalog"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/httpapi"
	"github.com/AlexanderTar/agent-swarm/internal/items"
	"github.com/AlexanderTar/agent-swarm/internal/kb"
	"github.com/AlexanderTar/agent-swarm/internal/repos"
	"github.com/AlexanderTar/agent-swarm/internal/settings"
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

	grace time.Duration           // shutdown budget; 0 means 5 s
	loops []func(context.Context) // extra background loops (tests)
}

type logf = func(format string, args ...any)

func cmdDaemon(args []string, stdout, stderr io.Writer) int {
	fs, home, _ := flags("daemon", stderr, false)
	port := fs.Int("port", 7777, "port on 127.0.0.1")
	noBackground := fs.Bool("no-background", false, "skip KB indexing, repo scans and catalog refresh")
	if code, done := parse(fs, args); done {
		return code
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := serve(ctx, daemonConfig{Home: *home, Port: *port, Background: !*noBackground,
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
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return nil, err
		}
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
	cat := &catalog.Service{DB: d, Events: ev, Fetchers: catalog.DefaultFetchers(userHome, os.Getenv("USER")), Now: now, Log: cfg.Log}
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
	api := httpapi.New(httpapi.Deps{Version: version, Token: token, DB: d, Events: ev,
		Items: &items.Store{DB: d, Events: ev, Now: now}, Repos: rp, Settings: st, Catalog: cat, KB: idx, Log: cfg.Log})
	return &daemon{cfg: cfg, db: d, ev: ev, cat: cat, st: st, rp: rp, idx: idx, api: api}, nil
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
		)
	}
	var bg sync.WaitGroup
	for _, loop := range loops {
		bg.Go(func() { loop(loopCtx) })
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
		if err := hs.Shutdown(shutdown); err != nil {
			hs.Close() // SSE streams never go idle
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
		cfg.Log("shutdown: background work still running after the grace period")
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
