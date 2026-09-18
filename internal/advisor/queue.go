package advisor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexanderTar/agent-swarm/internal/adapter"
	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/events"
	"github.com/AlexanderTar/agent-swarm/internal/execx"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
	"github.com/AlexanderTar/agent-swarm/internal/runtime"
)

const defaultMaxConcurrent = 2
const defaultTimeout = 240 * time.Second

// Service runs the simulated advisor: headless read-only CLI calls, queued at
// most MaxConcurrent at a time across the daemon, at most one per session
// (§11.6). It has no settings store by design: the §11.6 cap lives on
// MaxConcurrent, wired by cmd/swarm.
type Service struct {
	DB       *db.DB
	Events   *events.Store
	Home     string
	UserHome string
	Adapters map[runtime.AgentKind]adapter.Adapter
	Run      execx.Runner
	Now      func() time.Time
	Log      func(format string, args ...any)

	MaxConcurrent int
	Timeout       time.Duration

	// Deliver hands a finished advice run to the session that asked for it,
	// once the caller has already gone on without waiting for it (§11.6). It
	// is wired to runtime.Store's advice message enqueue with an immediate
	// wake (Task 35).
	Deliver func(ctx context.Context, sessionID string, adv runtime.Advice) error

	once sync.Once
	sem  chan struct{}

	// offsetsMu/offsets is ScanTranscript's per-session byte offset (Task 27).
	// It lives only in memory: a restart is a new Service with an empty map.
	offsetsMu sync.Mutex
	offsets   map[string]int64
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Service) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return defaultTimeout
}

func (s *Service) semaphore() chan struct{} {
	s.once.Do(func() {
		n := s.MaxConcurrent
		if n <= 0 {
			n = defaultMaxConcurrent
		}
		s.sem = make(chan struct{}, n)
	})
	return s.sem
}

// Mode implements runtime.Advisor: it delegates to the package-level Mode func
// so *Service satisfies the interface runtime.Store.Advisor is typed as.
func (s *Service) Mode(sessionKind, advisorKind runtime.AgentKind, advisorModel string, capable bool) string {
	return Mode(sessionKind, advisorKind, advisorModel, capable)
}

// advisorSession is what Ask needs about the calling session and its agent.
type advisorSession struct {
	AgentID, AgentName          string
	AgentKind                   runtime.AgentKind
	Role                        string
	ItemID, ItemKey, RootItemID string
	Brief                       string
	AdvisorKind, AdvisorModel   string
	AdvisorEffort               string
	Cwd, ProviderSessionID      string
}

func (s *Service) resolveSession(ctx context.Context, sessionID string) (advisorSession, error) {
	var out advisorSession
	var kind string
	err := s.DB.QueryRowContext(ctx, `
		SELECT a.id, a.name, a.kind, a.role, a.item_id, a.root_item_id, a.brief,
		       COALESCE(a.advisor_kind, ''), COALESCE(a.advisor_model, ''), COALESCE(a.advisor_effort, ''),
		       s.cwd, COALESCE(s.provider_session_id, ''), i.key
		FROM sessions s
		JOIN agents a ON a.id = s.agent_id
		JOIN items i ON i.id = a.item_id
		WHERE s.id = ?`, sessionID).Scan(
		&out.AgentID, &out.AgentName, &kind, &out.Role, &out.ItemID, &out.RootItemID, &out.Brief,
		&out.AdvisorKind, &out.AdvisorModel, &out.AdvisorEffort,
		&out.Cwd, &out.ProviderSessionID, &out.ItemKey)
	if errors.Is(err, sql.ErrNoRows) {
		return advisorSession{}, fmt.Errorf("no such session: %s", sessionID)
	}
	if err != nil {
		return advisorSession{}, err
	}
	out.AgentKind = runtime.AgentKind(kind)
	return out, nil
}

// startAdvice checks the §11.6 one-per-session rule and inserts the row in one
// transaction, so two concurrent Asks from the same session cannot both pass
// the check (SQLite's _txlock=immediate serialises the two transactions).
func (s *Service) startAdvice(ctx context.Context, adv runtime.Advice) error {
	return s.DB.Tx(ctx, func(tx *sql.Tx) error {
		var state string
		err := tx.QueryRowContext(ctx,
			`SELECT state FROM advice WHERE session_id = ? AND state IN ('queued', 'running') LIMIT 1`,
			adv.SessionID).Scan(&state)
		if err == nil {
			return errors.New("advice_busy: wait for your current advice request to finish.")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO advice
			(id, session_id, item_id, advisor_kind, advisor_model, advisor_effort, question,
			 context_path, context_chars, state, mode, created_at)
			VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, ?)`,
			adv.ID, adv.SessionID, adv.ItemID, adv.AdvisorKind, adv.AdvisorModel, adv.AdvisorEffort,
			adv.Question, adv.ContextPath, adv.ContextChars, adv.State, adv.Mode, db.Millis(adv.CreatedAt))
		return err
	})
}

func (s *Service) setRunning(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE advice SET state = 'running' WHERE id = ?`, id)
	if err == nil {
		s.Events.Notify()
	}
	return err
}

func (s *Service) finishOK(ctx context.Context, adv runtime.Advice, answer string, u Usage, dur time.Duration) runtime.Advice {
	adv.State, adv.Answer = "answered", answer
	adv.DurationMs = int(dur.Milliseconds())
	adv.InputTokens, adv.OutputTokens = u.Input, u.Output
	adv.CacheReadTokens, adv.CacheWriteTokens = u.CacheRead, u.CacheWrite
	adv.CostUSD = u.CostUSD
	finished := s.now()
	adv.FinishedAt = &finished
	_, err := s.DB.ExecContext(ctx, `UPDATE advice SET state = ?, answer = ?, duration_ms = ?,
		input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?,
		cost_usd = ?, finished_at = ? WHERE id = ?`,
		adv.State, adv.Answer, adv.DurationMs, adv.InputTokens, adv.OutputTokens,
		adv.CacheReadTokens, adv.CacheWriteTokens, adv.CostUSD, db.Millis(finished), adv.ID)
	if err != nil {
		s.logf("advisor: recording %s: %v", adv.ID, err)
	} else {
		s.Events.Notify()
	}
	return adv
}

func (s *Service) finishError(ctx context.Context, adv runtime.Advice, state, rawErr string, dur time.Duration) runtime.Advice {
	adv.State = state
	adv.Error = failureMessage(rawErr)
	adv.DurationMs = int(dur.Milliseconds())
	finished := s.now()
	adv.FinishedAt = &finished
	_, err := s.DB.ExecContext(ctx, `UPDATE advice SET state = ?, error = ?, duration_ms = ?, finished_at = ? WHERE id = ?`,
		adv.State, adv.Error, adv.DurationMs, db.Millis(finished), adv.ID)
	if err != nil {
		s.logf("advisor: recording %s: %v", adv.ID, err)
	} else {
		s.Events.Notify()
	}
	return adv
}

// Ask is §11.6: write the context file, run the advisor at most MaxConcurrent
// at a time, and return inline within wait or hand the result to Deliver.
func (s *Service) Ask(ctx context.Context, sessionID, question string, focus []string, wait time.Duration) (runtime.Advice, error) {
	sess, err := s.resolveSession(ctx, sessionID)
	if err != nil {
		return runtime.Advice{}, err
	}

	var turns []Turn
	if tp, err := TranscriptPath(sess.AgentKind, s.UserHome, sess.Cwd, sess.ProviderSessionID); err == nil {
		turns, _ = ReadTranscript(sess.AgentKind, tp, maxTranscriptTurns) // best effort
	}
	body := BuildContext(ContextInput{Brief: sess.Brief, Question: question, Focus: focus, Transcript: turns})

	id := ids.New("adv")
	dir := filepath.Join(s.Home, "run", "advice", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return runtime.Advice{}, err
	}
	ctxPath := filepath.Join(dir, "context.md")
	if err := os.WriteFile(ctxPath, []byte(body), 0o600); err != nil {
		return runtime.Advice{}, err
	}

	adv := runtime.Advice{
		ID: id, SessionID: sessionID, ItemID: sess.ItemID,
		AdvisorKind: sess.AdvisorKind, AdvisorModel: sess.AdvisorModel, AdvisorEffort: sess.AdvisorEffort,
		Question: question, ContextPath: ctxPath, ContextChars: len(body),
		State: "queued", Mode: "simulated", CreatedAt: s.now(),
	}
	if err := s.startAdvice(ctx, adv); err != nil {
		return runtime.Advice{}, err
	}
	s.Events.Notify()

	resultCh := make(chan runtime.Advice, 1)
	var claimed int32
	deliver := func(final runtime.Advice) {
		if atomic.CompareAndSwapInt32(&claimed, 0, 1) {
			resultCh <- final
			return
		}
		if s.Deliver != nil {
			_ = s.Deliver(context.Background(), sessionID, final)
		}
	}
	go s.run(sess, adv, deliver)

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case final := <-resultCh:
		return final, nil
	case <-timer.C:
		if atomic.CompareAndSwapInt32(&claimed, 0, 1) {
			pending := adv
			pending.State = "running"
			return pending, nil
		}
		// the run claimed it in the tiny window between the timer firing and
		// this check; its result is already waiting on resultCh.
		return <-resultCh, nil
	}
}

// run executes one advisor call outside the caller's request context: a slow
// run must keep going after Ask has already returned "running" to its caller.
func (s *Service) run(sess advisorSession, adv runtime.Advice, deliver func(runtime.Advice)) {
	ctx := context.Background()
	if err := s.setRunning(ctx, adv.ID); err != nil {
		s.logf("advisor: %v", err)
	}

	sem := s.semaphore()
	sem <- struct{}{}
	defer func() { <-sem }()

	dir := filepath.Dir(adv.ContextPath)
	prompt := fillPrompt(sess.AgentName, sess.Role, sess.ItemKey, adv.ContextPath)
	argv, err := AdvisorCommand(runtime.AgentKind(adv.AdvisorKind), adv.AdvisorModel, adv.AdvisorEffort, dir, prompt)
	if err != nil {
		deliver(s.finishError(ctx, adv, "failed", err.Error(), 0))
		return
	}
	if s.Run == nil {
		deliver(s.finishError(ctx, adv, "failed", "no advisor runner configured", 0))
		return
	}

	runCtx, cancel := context.WithTimeout(ctx, s.timeout())
	defer cancel()
	start := s.now()
	out, runErr := s.Run(runCtx, argv[0], argv[1:]...)
	dur := s.now().Sub(start)

	if errors.Is(runErr, context.DeadlineExceeded) {
		deliver(s.finishError(ctx, adv, "timed_out", "the advisor did not answer in time", dur))
		return
	}
	if runErr != nil {
		deliver(s.finishError(ctx, adv, "failed", runErr.Error(), dur))
		return
	}
	answer, usage, perr := ParseAnswer(runtime.AgentKind(adv.AdvisorKind), out, nil, dir, s.UserHome)
	if perr != nil {
		deliver(s.finishError(ctx, adv, "failed", perr.Error(), dur))
		return
	}
	deliver(s.finishOK(ctx, adv, answer, usage, dur))
}

// List returns one agent's advice, newest first (§11.6). Another agent's
// advice never appears: the query is scoped by name through the session that
// asked for it.
func (s *Service) List(ctx context.Context, agentName string) ([]runtime.Advice, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT ad.id, ad.session_id, ad.item_id, ad.advisor_kind, ad.advisor_model,
		       COALESCE(ad.advisor_effort, ''), ad.question, COALESCE(ad.context_path, ''),
		       COALESCE(ad.context_chars, 0), ad.state, COALESCE(ad.answer, ''), COALESCE(ad.error, ''),
		       ad.mode, COALESCE(ad.source_request_id, ''), COALESCE(ad.duration_ms, 0),
		       COALESCE(ad.input_tokens, 0), COALESCE(ad.output_tokens, 0),
		       COALESCE(ad.cache_read_tokens, 0), COALESCE(ad.cache_write_tokens, 0), ad.cost_usd,
		       ad.created_at, ad.finished_at
		FROM advice ad
		JOIN sessions ses ON ses.id = ad.session_id
		JOIN agents a ON a.id = ses.agent_id
		WHERE a.name = ?
		ORDER BY ad.created_at DESC, ad.rowid DESC`, agentName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runtime.Advice
	for rows.Next() {
		var a runtime.Advice
		var createdMs int64
		var finishedMs sql.NullInt64
		if err := rows.Scan(&a.ID, &a.SessionID, &a.ItemID, &a.AdvisorKind, &a.AdvisorModel,
			&a.AdvisorEffort, &a.Question, &a.ContextPath, &a.ContextChars, &a.State, &a.Answer,
			&a.Error, &a.Mode, &a.SourceRequestID, &a.DurationMs, &a.InputTokens, &a.OutputTokens,
			&a.CacheReadTokens, &a.CacheWriteTokens, &a.CostUSD, &createdMs, &finishedMs); err != nil {
			return nil, err
		}
		a.CreatedAt = db.FromMillis(createdMs)
		if finishedMs.Valid {
			f := db.FromMillis(finishedMs.Int64)
			a.FinishedAt = &f
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
