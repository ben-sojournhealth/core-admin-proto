package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"

	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claimsintake"
	"coreadmin/internal/journey"
	"coreadmin/internal/sim"
	"coreadmin/internal/x12"
)

// journeyServer backs the Journey workbench with its own sandbox. Claims
// Intake loads files into the same sandbox, so claims it takes in show up
// in Journey's queues.
type journeyServer struct {
	mu     sync.Mutex
	sim    *sim.Sim
	intake *claimsintake.Processor
}

func newJourney() (*journeyServer, error) {
	repos, err := newRepos("journey")
	if err != nil {
		return nil, err
	}
	s, err := journey.NewIn(repos)
	if err != nil {
		return nil, err
	}
	return &journeyServer{sim: s, intake: &claimsintake.Processor{
		Repos: s.Repos, Claims: s.Claims, Now: s.Claims.Now, Logf: s.Claims.Logf,
		Files: map[string][]byte{}, ReceiverID: claimsintake.OurID,
	}}, nil
}

type sampleView struct {
	claimsintake.Sample
	Sent bool `json:"sent"`
}

func (j *journeyServer) intakeState(ctx context.Context) (any, error) {
	jobs, err := j.intake.Jobs(ctx)
	if err != nil {
		return nil, err
	}
	sent, err := j.intake.Loaded(ctx)
	if err != nil {
		return nil, err
	}
	var samples []sampleView
	for _, x := range claimsintake.Samples() {
		ic, _ := x12.Parse([]byte(x.Text))
		samples = append(samples, sampleView{Sample: x, Sent: ic != nil && sent[ic.ControlNumber()]})
	}
	return map[string]any{"samples": samples, "jobs": jobs}, nil
}

func (j *journeyServer) handle(f func(r *http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		defer j.mu.Unlock()
		out, err := f(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Printf("encode: %v", err)
		}
	}
}

// registerJourney serves Journey (the admin portal) and Claims Intake,
// which share one sandbox.
func registerJourney(mux *http.ServeMux) error {
	j, err := newJourney()
	if err != nil {
		return err
	}
	const jp = "/journey"
	mux.HandleFunc("GET "+jp+"/api/state", j.handle(func(*http.Request) (any, error) { return journey.Snapshot(j.sim) }))
	mux.HandleFunc("GET "+jp+"/api/claim", j.handle(func(r *http.Request) (any, error) {
		return journey.GetDetail(j.sim, r.URL.Query().Get("id"))
	}))
	mux.HandleFunc("POST "+jp+"/api/action", j.handle(func(r *http.Request) (any, error) {
		var a sim.Action
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			return nil, err
		}
		return j.sim.Apply(a), nil
	}))
	reset := func() error {
		fresh, err := newJourney()
		if err != nil {
			return err
		}
		j.sim, j.intake = fresh.sim, fresh.intake
		return nil
	}
	mux.HandleFunc("POST "+jp+"/api/reset", j.handle(func(*http.Request) (any, error) {
		if err := reset(); err != nil {
			return nil, err
		}
		return journey.Snapshot(j.sim)
	}))

	// Claims Intake: 837 files into the Journey sandbox.
	const ci = "/claims-intake"
	mux.HandleFunc("GET "+ci+"/api/state", j.handle(func(r *http.Request) (any, error) { return j.intakeState(r.Context()) }))
	mux.HandleFunc("GET "+ci+"/api/job", j.handle(func(r *http.Request) (any, error) {
		return j.intake.View(r.Context(), r.URL.Query().Get("id"))
	}))
	mux.HandleFunc("POST "+ci+"/api/ingest", j.handle(func(r *http.Request) (any, error) {
		var in struct{ Filename, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		if in.Filename == "" {
			in.Filename = "upload.837"
		}
		var job *ingestionv1.IngestionJob
		res := j.sim.Do("clearinghouse", "Loaded 837 file "+in.Filename, func(ctx context.Context) error {
			var err error
			job, err = j.intake.Ingest(ctx, in.Filename, []byte(in.Text))
			return err
		})
		if !res.OK {
			return nil, errors.New(res.Error)
		}
		return j.intake.View(r.Context(), job.GetId())
	}))
	mux.HandleFunc("POST "+ci+"/api/preview", j.handle(func(r *http.Request) (any, error) {
		var in struct{ Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return j.intake.Preview(r.Context(), []byte(in.Text))
	}))
	mux.HandleFunc("POST "+ci+"/api/reset", j.handle(func(r *http.Request) (any, error) {
		if err := reset(); err != nil {
			return nil, err
		}
		return j.intakeState(r.Context())
	}))
	return nil
}
