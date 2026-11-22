package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"coreadmin/internal/eligibility"
	"coreadmin/internal/eligibilitydemo"
	"coreadmin/internal/enrollmentintake"
	"coreadmin/internal/protodoc"
	"coreadmin/internal/store"
	"coreadmin/internal/x12"
)

// enrollmentServer is the Enrollment Intake and Eligibility demos: 834
// files into a sandbox of their own, applied to eligibility. The sandbox
// starts with the customers, plans and groups from the seed data, no
// members, and the open-enrollment full file loaded.
type enrollmentServer struct {
	mu   sync.Mutex
	proc *enrollmentintake.Processor
	elig *eligibility.Service
	now  func() time.Time
}

func newEnrollment() (*enrollmentServer, error) {
	repos, err := newRepos("enrollment")
	if err != nil {
		return nil, err
	}
	sb, err := eligibilitydemo.NewSandboxIn(repos)
	if err != nil {
		return nil, err
	}
	return &enrollmentServer{proc: sb.Intake, elig: sb.Elig, now: sb.Now}, nil
}

type enrollmentSample struct {
	enrollmentintake.Sample
	Sent bool `json:"sent"`
}

func (e *enrollmentServer) state(ctx context.Context) (any, error) {
	jobs, err := e.proc.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	sent, err := e.proc.Loaded(ctx)
	if err != nil {
		return nil, err
	}
	var samples []enrollmentSample
	for _, x := range enrollmentintake.Samples() {
		ic, _ := x12.Parse([]byte(x.Text))
		samples = append(samples, enrollmentSample{Sample: x, Sent: ic != nil && sent[ic.ControlNumber()]})
	}
	return map[string]any{"samples": samples, "jobs": jobs}, nil
}

func (e *enrollmentServer) handle(f func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		out, err := f(r)
		writeJSON(w, out, err)
	}
}

// registerEnrollment serves Enrollment Intake and Eligibility, which share
// one sandbox.
func registerEnrollment(mux *http.ServeMux) error {
	// Enrollment Intake: 834 files into a sandbox of their own.
	en, err := newEnrollment()
	if err != nil {
		return err
	}
	const ei = "/enrollment-intake"
	mux.HandleFunc("GET "+ei+"/api/state", en.handle(func(r *http.Request) (any, error) { return en.state(r.Context()) }))
	// Explanations of the enrollment record's fields, from the .proto
	// comments.
	mux.HandleFunc("GET "+ei+"/api/docs", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, protodoc.For("coreadmin.eligibility.v1.EnrollmentTransaction"), nil)
	})
	mux.HandleFunc("GET "+ei+"/api/job", en.handle(func(r *http.Request) (any, error) {
		return en.proc.View(r.Context(), r.URL.Query().Get("id"))
	}))
	mux.HandleFunc("POST "+ei+"/api/ingest", en.handle(func(r *http.Request) (any, error) {
		var in struct{ Filename, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		if in.Filename == "" {
			in.Filename = "upload.834"
		}
		job, err := en.proc.Ingest(r.Context(), in.Filename, []byte(in.Text))
		if err != nil {
			return nil, err
		}
		return en.proc.View(r.Context(), job.GetId())
	}))
	mux.HandleFunc("POST "+ei+"/api/preview", en.handle(func(r *http.Request) (any, error) {
		var in struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return en.proc.Preview(r.Context(), []byte(in.Text))
	}))
	mux.HandleFunc("POST "+ei+"/api/reset", en.handle(func(r *http.Request) (any, error) {
		fresh, err := newEnrollment()
		if err != nil {
			return nil, err
		}
		en.proc, en.elig, en.now = fresh.proc, fresh.elig, fresh.now
		return en.state(r.Context())
	}))

	// Eligibility: members derived from the 834s loaded above, every
	// version, and full-file approvals.
	const el = "/eligibility"
	mux.HandleFunc("GET "+el+"/api/overview", en.handle(func(r *http.Request) (any, error) {
		return en.elig.Overview(r.Context(), en.now())
	}))
	mux.HandleFunc("GET "+el+"/api/member", en.handle(func(r *http.Request) (any, error) {
		return en.elig.Member(r.Context(), r.URL.Query().Get("id"), en.now())
	}))
	mux.HandleFunc("POST "+el+"/api/approve", en.handle(func(r *http.Request) (any, error) {
		var in struct{ JobID, Set, Note string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		if err := en.elig.ApproveFullFile(store.WithActor(r.Context(), "ops"), in.JobID, in.Set, in.Note); err != nil {
			return nil, err
		}
		return en.elig.Overview(r.Context(), en.now())
	}))
	mux.HandleFunc("POST "+el+"/api/regenerate", en.handle(func(r *http.Request) (any, error) {
		var in struct{ DryRun bool }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		changes, err := en.elig.Regenerate(r.Context(), in.DryRun)
		if err != nil {
			return nil, err
		}
		var names []string
		for _, c := range changes {
			names = append(names, c.After.GetName().GetFirst()+" "+c.After.GetName().GetLast())
		}
		return map[string]any{"dryRun": in.DryRun, "changed": names}, nil
	}))
	// The walkthrough plays its examples on fresh sandboxes of its own.
	mux.HandleFunc("GET "+el+"/api/tour", func(w http.ResponseWriter, r *http.Request) {
		t, err := eligibilitydemo.BuildTour(r.Context())
		writeJSON(w, t, err)
	})
	mux.HandleFunc("POST "+el+"/api/reset", en.handle(func(r *http.Request) (any, error) {
		fresh, err := newEnrollment()
		if err != nil {
			return nil, err
		}
		en.proc, en.elig, en.now = fresh.proc, fresh.elig, fresh.now
		return en.elig.Overview(r.Context(), en.now())
	}))
	return nil
}
