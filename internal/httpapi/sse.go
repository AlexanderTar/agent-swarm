package httpapi

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	var after int64
	live := cursor == "" // no cursor: live events only; the client fetches state itself (contracts §5)
	if !live {
		n, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || n < 0 {
			s.writeErr(w, apiErr(http.StatusBadRequest, "bad_request", "after must be an event sequence number."))
			return
		}
		after = n
	}
	ctx := r.Context()
	wake, unsubscribe := s.Events.Subscribe() // before the first read, so no wakeup is lost
	defer unsubscribe()
	if live { // read before the headers go out, so an event published right after connecting is not skipped
		var err error
		if after, err = s.Events.Latest(ctx); err != nil {
			s.writeErr(w, err)
			return
		}
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	send := func(format string, args ...any) error {
		rc.SetWriteDeadline(time.Now().Add(s.WriteTimeout)) // unsupported writers just ignore it
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			return err
		}
		return nil
	}
	// storeFailed logs a store error unless the client already left, then ends the stream.
	storeFailed := func(err error) {
		if ctx.Err() == nil {
			s.Log("httpapi: events stream: %v", err)
		}
	}
	flush := func() error {
		rc.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		return rc.Flush()
	}

	expired, err := s.Events.Expired(ctx, after)
	if err != nil {
		storeFailed(err)
		return
	}
	if expired {
		if send("event: reset\ndata: {}\n\n") != nil {
			return
		}
		if after, err = s.Events.Latest(ctx); err != nil {
			storeFailed(err)
			return
		}
	}
	if flush() != nil {
		return
	}
	ping := time.NewTicker(s.PingInterval)
	defer ping.Stop()
	for {
		evs, err := s.Events.After(ctx, after, 500)
		if err != nil {
			storeFailed(err)
			return
		}
		for _, e := range evs {
			if send("id: %d\nevent: %s\ndata: %s\n\n", e.Seq, e.Type, e.Payload) != nil {
				return
			}
			after = e.Seq
		}
		if len(evs) > 0 && flush() != nil {
			return
		}
		if len(evs) == 500 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.done: // the daemon is shutting down
			return
		case <-wake:
		case <-ping.C:
			if send(": ping\n\n") != nil || flush() != nil {
				return
			}
		}
	}
}
