package main

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"coreadmin/internal/repo"
	"coreadmin/internal/scenario"
	"coreadmin/internal/sim"
)

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

// registerLifecycle serves the Claims Lifecycle demo: the claims engine
// driven by hand or by scripted scenarios, in its own sandbox.
func registerLifecycle(mux *http.ServeMux) error {
	s, err := newServer()
	if err != nil {
		return err
	}
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
	return nil
}
