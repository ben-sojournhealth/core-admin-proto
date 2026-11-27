package main

import (
	"encoding/json"
	"errors"
	"net/http"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/employer/portal"
)

// registerEmployerPortal serves the Employer Portal: one customer's view
// of its own enrollment, read from the sandbox Enrollment Intake and
// Eligibility share, so a file loaded there shows up here. Choosing the
// employer stands in for signing in.
func registerEmployerPortal(mux *http.ServeMux) error {
	en := enrollment
	if en == nil {
		return errors.New("employer portal: the enrollment sandbox isn't registered")
	}
	// Built per request: Reset replaces the sandbox.
	p := func() *portal.Portal { return &portal.Portal{Repos: en.repos, Elig: en.elig, Intake: en.proc} }
	const ep = "/employer-portal"
	mux.HandleFunc("GET "+ep+"/api/employers", en.handle(func(r *http.Request) (any, error) {
		return p().Employers(r.Context())
	}))
	mux.HandleFunc("GET "+ep+"/api/view", en.handle(func(r *http.Request) (any, error) {
		// on: the date the roster and counts are about; today if absent.
		var on *commonv1.Date
		if s := r.URL.Query().Get("on"); s != "" {
			d, err := dates.Parse(s)
			if err != nil {
				return nil, err
			}
			on = d
		}
		return p().View(r.Context(), r.URL.Query().Get("customer"), en.now(), on)
	}))
	// One member's history: every version, with the file that caused it.
	mux.HandleFunc("GET "+ep+"/api/member", en.handle(func(r *http.Request) (any, error) {
		return p().Member(r.Context(), r.URL.Query().Get("customer"), r.URL.Query().Get("id"), en.now())
	}))
	// Sample files for the file check, and the check itself: how we would
	// read a file, with nothing loaded.
	mux.HandleFunc("GET "+ep+"/api/samples", en.handle(func(r *http.Request) (any, error) {
		return p().Samples(r.URL.Query().Get("customer")), nil
	}))
	mux.HandleFunc("POST "+ep+"/api/check", en.handle(func(r *http.Request) (any, error) {
		var in struct{ Customer, Text string }
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			return nil, err
		}
		return p().CheckFile(r.Context(), in.Customer, []byte(in.Text))
	}))
	return nil
}
