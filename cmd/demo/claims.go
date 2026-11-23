package main

import (
	"net/http"

	"coreadmin/internal/claims/claimsdemo"
)

// registerClaims serves "How claims works", the page behind the Claims
// domain on the map. Its examples play on fresh sandboxes each time, so
// there is no state and no reset.
func registerClaims(mux *http.ServeMux) error {
	mux.HandleFunc("GET /claims/api/tour", func(w http.ResponseWriter, r *http.Request) {
		t, err := claimsdemo.BuildTour(r.Context())
		writeJSON(w, t, err)
	})
	return nil
}
