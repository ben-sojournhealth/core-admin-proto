// Command demo serves the interactive demos. The landing page is an
// architecture diagram; components with a demo link into it.
//
//	go run ./cmd/demo            # then open http://localhost:8080
//
// Each demo lives under its own path (/claims-lifecycle/ and its API at
// /claims-lifecycle/api/). All state is in memory and shared by every
// browser tab; Reset starts over from the seed data. Journey and Claims
// Intake share one sandbox.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"log"
	"net/http"
	"sync"

	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claimsintake"
	"coreadmin/internal/demos"
	"coreadmin/internal/journey"
	"coreadmin/internal/remittancedemo"
	"coreadmin/internal/scenario"
	"coreadmin/internal/sim"
	"coreadmin/internal/x12"
)

//go:embed web
var webFS embed.FS

type stepView struct {
	Label   string   `json:"label"`
	Why     string   `json:"why,omitempty"`
	Expects []string `json:"expects"`
}

type scenarioView struct {
	ID      string     `json:"id"`
	Group   string     `json:"group"`
	Title   string     `json:"title"`
	Summary string     `json:"summary"`
	Steps   []stepView `json:"steps"`
}

type activeView struct {
	ID      string                `json:"id"`
	Next    int                   `json:"next"`
	Results []scenario.StepResult `json:"results"`
}

type response struct {
	State     *sim.State     `json:"state"`
	Scenarios []scenarioView `json:"scenarios"`
	Active    *activeView    `json:"active"`
	Result    *sim.Result    `json:"result,omitempty"`
}

type server struct {
	mu        sync.Mutex
	runner    *scenario.Runner
	scenarios []scenarioView
}

func newServer() (*server, error) {
	r, err := scenario.NewRunner()
	if err != nil {
		return nil, err
	}
	s := &server{runner: r}
	for _, sc := range scenario.Library {
		v := scenarioView{ID: sc.ID, Group: sc.Group, Title: sc.Title, Summary: sc.Summary}
		for _, st := range sc.Steps {
			sv := stepView{Label: st.Label, Why: st.Why, Expects: []string{}}
			if st.Rejected != "" {
				sv.Expects = append(sv.Expects, "Refused: "+st.Rejected)
			}
			for _, c := range st.Checks {
				sv.Expects = append(sv.Expects, c.Text)
			}
			v.Steps = append(v.Steps, sv)
		}
		s.scenarios = append(s.scenarios, v)
	}
	return s, nil
}

// reply writes the whole view after every request, so the page always
// re-renders from one consistent snapshot.
func (s *server) reply(w http.ResponseWriter, res *sim.Result, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	st, err := s.runner.Sim.State()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := response{State: st, Scenarios: s.scenarios, Result: res}
	if a := s.runner.Active; a != nil {
		out.Active = &activeView{ID: a.ID, Next: s.runner.Next, Results: s.runner.Results}
		if out.Active.Results == nil {
			out.Active.Results = []scenario.StepResult{}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		log.Printf("encode: %v", err)
	}
}

func (s *server) handle(f func(r *http.Request) (*sim.Result, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		res, err := f(r)
		s.reply(w, res, err)
	}
}

// journeyServer backs the Journey workbench with its own sandbox. Claims
// Intake loads files into the same sandbox, so claims it takes in show up
// in Journey's queues.
type journeyServer struct {
	mu     sync.Mutex
	sim    *sim.Sim
	intake *claimsintake.Processor
}

func newJourney() (*journeyServer, error) {
	s, err := journey.New()
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

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode: %v", err)
	}
}

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	flag.Parse()

	s, err := newServer()
	if err != nil {
		log.Fatal(err)
	}
	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /api/demos", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(demos.Catalog())
	})

	// Claims Lifecycle.
	const cl = "/claims-lifecycle"
	mux.HandleFunc("GET "+cl+"/api/state", s.handle(func(*http.Request) (*sim.Result, error) { return nil, nil }))
	mux.HandleFunc("POST "+cl+"/api/action", s.handle(func(r *http.Request) (*sim.Result, error) {
		var a sim.Action
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			return nil, err
		}
		return s.runner.Sim.Apply(a), nil
	}))
	mux.HandleFunc("POST "+cl+"/api/reset", s.handle(func(*http.Request) (*sim.Result, error) { return nil, s.runner.Reset() }))
	mux.HandleFunc("POST "+cl+"/api/scenario/load", s.handle(func(r *http.Request) (*sim.Result, error) {
		return nil, s.runner.Load(r.URL.Query().Get("id"))
	}))
	mux.HandleFunc("POST "+cl+"/api/scenario/step", s.handle(func(*http.Request) (*sim.Result, error) {
		res, _ := s.runner.Step()
		return res, nil
	}))
	mux.HandleFunc("POST "+cl+"/api/scenario/runall", s.handle(func(*http.Request) (*sim.Result, error) {
		var last *sim.Result
		for {
			res, ok := s.runner.Step()
			if !ok {
				return last, nil
			}
			last = res
		}
	}))

	// Journey: the examiner workbench.
	j, err := newJourney()
	if err != nil {
		log.Fatal(err)
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

	// Remittance: adjudication results in, an 835 out. Each run uses a
	// fresh sandbox, so there is no state and no reset.
	const rp = "/remittance"
	mux.HandleFunc("GET "+rp+"/api/samples", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, remittancedemo.Samples(), nil)
	})
	mux.HandleFunc("GET "+rp+"/api/run", func(w http.ResponseWriter, r *http.Request) {
		v, err := remittancedemo.Run(r.Context(), r.URL.Query().Get("sample"), r.URL.Query().Get("method"))
		writeJSON(w, v, err)
	})

	log.Printf("demos on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
