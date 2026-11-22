package main

import (
	"net/http"

	"coreadmin/internal/remittancedemo"
)

// registerRemittance serves the Remittance demo: adjudication results in,
// an 835 out. Each run uses a fresh sandbox, so there is no state and no
// reset.
func registerRemittance(mux *http.ServeMux) error {
	const rp = "/remittance"
	mux.HandleFunc("GET "+rp+"/api/samples", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, remittancedemo.Samples(), nil)
	})
	mux.HandleFunc("GET "+rp+"/api/run", func(w http.ResponseWriter, r *http.Request) {
		v, err := remittancedemo.Run(r.Context(), r.URL.Query().Get("sample"), r.URL.Query().Get("method"))
		writeJSON(w, v, err)
	})
	return nil
}
