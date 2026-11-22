package main

import (
	"net/http"

	"coreadmin/internal/claims/adjudication"
)

// registerAdjudication serves the adjudication map: the engine's steps,
// what each can pend or deny, and what it asks other domains.
func registerAdjudication(mux *http.ServeMux) error {
	mux.HandleFunc("GET /adjudication/api/steps", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, adjudication.Steps, nil)
	})
	return nil
}
