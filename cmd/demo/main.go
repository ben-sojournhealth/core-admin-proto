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

	"time"

	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claimsintake"
	"coreadmin/internal/demos"
	"coreadmin/internal/eligibility"
	"coreadmin/internal/eligibilitydemo"
	"coreadmin/internal/enrollmentintake"
	"coreadmin/internal/journey"
	"coreadmin/internal/pgdb"
	"coreadmin/internal/protodoc"
	"coreadmin/internal/remittancedemo"
	"coreadmin/internal/repo"
	"coreadmin/internal/scenario"
	"coreadmin/internal/sim"
	"coreadmin/internal/store"
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
	r, err := scenario.NewRunner(func() (*repo.Repos, error) { return newRepos("lifecycle") })
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

// newRepos gives a long-lived sandbox its schema in the demo database,
// dropped and recreated: on start, and on Reset.
func newRepos(schema string) (*repo.Repos, error) {
	return repo.Open(context.Background(), schema, true)
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
	// Everything runs on the local Postgres (see pgdb); this starts it if
	// it isn't running, and leaves it running.
	if _, err := pgdb.Pool(context.Background()); err != nil {
		log.Fatal(err)
	}
	log.Printf("Postgres: %s (schemas lifecycle, journey, enrollment; throwaway s_* schemas per request)", pgdb.URL(pgdb.Database()))

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

	// Enrollment Intake: 834 files into a sandbox of their own.
	en, err := newEnrollment()
	if err != nil {
		log.Fatal(err)
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
