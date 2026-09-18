package advisor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"

	"github.com/AlexanderTar/agent-swarm/internal/db"
	"github.com/AlexanderTar/agent-swarm/internal/ids"
)

// NativeEntry is one native-advisor call found in a Claude transcript (P0-14).
type NativeEntry struct {
	RequestID                            string
	Index                                int
	Model                                string
	Input, Output, CacheRead, CacheWrite int
	Answer                               string
}

// ParseNativeAdvisor finds every advisor call in a slice of Claude transcript
// lines (P0-14). Advisor usage is an element of message.usage.iterations[] with
// type "advisor_message", repeated on every content line of one response, so the
// result is deduplicated by requestId plus the element's index.
func ParseNativeAdvisor(lines []byte) ([]NativeEntry, error) {
	type iteration struct {
		Type                     string `json:"type"`
		Model                    string `json:"model"`
		InputTokens              int    `json:"input_tokens"`
		OutputTokens             int    `json:"output_tokens"`
		CacheReadInputTokens     int    `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int    `json:"cache_creation_input_tokens"`
	}
	type content struct {
		Type    string `json:"type"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	type record struct {
		Type      string `json:"type"`
		RequestID string `json:"requestId"`
		Message   struct {
			Content []content `json:"content"`
			Usage   struct {
				Iterations []iteration `json:"iterations"`
			} `json:"usage"`
		} `json:"message"`
	}
	seen := map[string]bool{}
	var out []NativeEntry
	answers := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(lines))
	sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
	for sc.Scan() {
		var r record
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil || r.Type != "assistant" {
			continue // a half-written line is skipped, not fatal
		}
		for _, c := range r.Message.Content {
			if c.Content.Type == "advisor_result" && c.Content.Text != "" {
				answers[r.RequestID] = c.Content.Text
			}
		}
		for i, it := range r.Message.Usage.Iterations {
			if it.Type != "advisor_message" {
				continue
			}
			key := r.RequestID + "#" + strconv.Itoa(i)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, NativeEntry{RequestID: key, Index: i, Model: it.Model,
				Input: it.InputTokens, Output: it.OutputTokens,
				CacheRead: it.CacheReadInputTokens, CacheWrite: it.CacheCreationInputTokens})
		}
	}
	for i := range out {
		out[i].Answer = answers[strings.Split(out[i].RequestID, "#")[0]]
	}
	return out, sc.Err()
}

// ScanTranscript reads the new bytes of a Claude session's own transcript
// since the last scan and records one native-advisor `advice` row per call
// found (P0-14). The per-session byte offset lives only in memory: a restart
// is a new Service with an empty offset map, and the advice_native_request
// unique index is what makes rescanning from byte 0 a no-op rather than a
// duplicate (no ResetOffsets — a restart already builds a fresh Service, so
// there is nothing a test needs to reset by hand, D65).
func (s *Service) ScanTranscript(ctx context.Context, sessionID, path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	offset := s.getOffset(sessionID)
	if _, err := f.Seek(offset, 0); err != nil {
		return err
	}
	data, err := readAll(f)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}

	// Advance the offset only past the last complete line: a half-written
	// trailing line must be re-read next time, not skipped forever.
	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		return nil
	}
	complete := data[:lastNL+1]

	entries, err := ParseNativeAdvisor(complete)
	if err != nil {
		return err
	}
	if len(entries) > 0 {
		sess, err := s.resolveSession(ctx, sessionID)
		if err != nil {
			return err
		}
		now := db.Millis(s.now())
		for _, e := range entries {
			_, err := s.DB.ExecContext(ctx, `INSERT INTO advice
				(id, session_id, item_id, advisor_kind, advisor_model, question, state, mode,
				 answer, source_request_id, input_tokens, output_tokens, cache_read_tokens,
				 cache_write_tokens, created_at, finished_at)
				VALUES (?, ?, ?, 'claude', ?, '(built-in advisor)', 'answered', 'native', ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (session_id, source_request_id) WHERE source_request_id IS NOT NULL DO NOTHING`,
				ids.New("adv"), sessionID, sess.ItemID, e.Model, e.Answer, e.RequestID,
				e.Input, e.Output, e.CacheRead, e.CacheWrite, now, now)
			if err != nil {
				return err
			}
		}
		s.Events.Notify()
	}
	s.setOffset(sessionID, offset+int64(len(complete)))
	return nil
}

func readAll(f *os.File) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *Service) getOffset(sessionID string) int64 {
	s.offsetsMu.Lock()
	defer s.offsetsMu.Unlock()
	return s.offsets[sessionID]
}

func (s *Service) setOffset(sessionID string, offset int64) {
	s.offsetsMu.Lock()
	defer s.offsetsMu.Unlock()
	if s.offsets == nil {
		s.offsets = map[string]int64{}
	}
	s.offsets[sessionID] = offset
}
