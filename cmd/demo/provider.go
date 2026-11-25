package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"coreadmin/internal/common/store"
	"coreadmin/internal/network/vendorintake"
)

// providerServer is the Provider Intake demo: a network vendor's full
// provider files into a sandbox of its own. Each file proposes one update;
// nothing changes provider data until it is approved.
type providerServer struct {
	mu   sync.Mutex
	proc *vendorintake.Processor
}

func newProviderIntake() (*vendorintake.Processor, error) {
	repos, err := newRepos("providers")
	if err != nil {
		return nil, err
	}
	if err := vendorintake.Setup(context.Background(), repos); err != nil {
		return nil, err
	}
	// A clock of its own, so the demo reads the same every time.
	clock := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	return &vendorintake.Processor{Repos: repos, Now: func() time.Time {
		clock = clock.Add(time.Second)
		return clock
	}}, nil
}

func (s *providerServer) handle(f func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out, err := f(r)
		writeJSON(w, out, err)
	}
}

func (s *providerServer) state(ctx context.Context) (any, error) {
	st, err := s.proc.State(ctx)
	if err != nil {
		return nil, err
	}
	examples, err := s.proc.Examples(ctx)
	return map[string]any{"state": st, "examples": examples}, err
}

func registerProviderIntake(mux *http.ServeMux) error {
	proc, err := newProviderIntake()
	if err != nil {
		return err
	}
	s := &providerServer{proc: proc}
	const base = "/provider-intake"
	mux.HandleFunc("GET "+base+"/api/state", s.handle(func(r *http.Request) (any, error) { return s.state(r.Context()) }))
	mux.HandleFunc("GET "+base+"/api/update", s.handle(func(r *http.Request) (any, error) {
		return s.proc.Update(r.Context(), r.URL.Query().Get("id"), r.URL.Query().Get("kind"))
	}))
	mux.HandleFunc("GET "+base+"/api/lookup", s.handle(func(r *http.Request) (any, error) {
		return s.proc.Lookup(r.Context(), r.URL.Query().Get("q"))
	}))
	mux.HandleFunc("POST "+base+"/api/send", s.handle(func(r *http.Request) (any, error) {
		var in struct{ Sample string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		for _, x := range vendorintake.Samples() {
			if x.ID != in.Sample {
				continue
			}
			job, u, err := s.proc.Ingest(r.Context(), x.SourceID, x.Filename, strings.NewReader(x.Text))
			if err != nil {
				return nil, err
			}
			id := job.GetId()
			if u != nil {
				id = u.GetId()
			}
			return map[string]any{"id": id}, nil
		}
		return nil, fmt.Errorf("no sample %q", in.Sample)
	}))
	mux.HandleFunc("POST "+base+"/api/decide", s.handle(func(r *http.Request) (any, error) {
		var in struct {
			ID      string
			Approve bool
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		ctx := store.WithActor(r.Context(), "ops")
		decide := s.proc.Reject
		if in.Approve {
			decide = s.proc.Approve
		}
		if _, err := decide(ctx, in.ID); err != nil {
			return nil, err
		}
		return s.state(r.Context())
	}))
	mux.HandleFunc("POST "+base+"/api/reset", s.handle(func(r *http.Request) (any, error) {
		fresh, err := newProviderIntake()
		if err != nil {
			return nil, err
		}
		s.proc = fresh
		return s.state(r.Context())
	}))
	return nil
}
