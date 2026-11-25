package main

import (
	"context"
	"net/http"
	"sync"

	"coreadmin/gen/coreadmin/claims/v1/claimsv1connect"
	"coreadmin/internal/claims/servicedefsdemo"
	"coreadmin/internal/common/store"
)

// registerServiceDefinitions serves the Service Definitions demo. The page
// calls the ServiceDefinitionService itself, mounted here as Connect
// serves it (JSON over POST at /coreadmin.claims.v1.ServiceDefinitionService/<Method>),
// over a sandbox of its own that starts with the seeded definitions.
func registerServiceDefinitions(mux *http.ServeMux) error {
	var mu sync.RWMutex
	var api http.Handler
	path := ""
	fresh := func() error {
		repos, err := newRepos("servicedefs")
		if err != nil {
			return err
		}
		svc, err := servicedefsdemo.NewIn(context.Background(), repos)
		if err != nil {
			return err
		}
		p, h := claimsv1connect.NewServiceDefinitionServiceHandler(svc)
		mu.Lock()
		path, api = p, h
		mu.Unlock()
		return nil
	}
	if err := fresh(); err != nil {
		return err
	}
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		mu.RLock()
		h := api
		mu.RUnlock()
		h.ServeHTTP(w, r.WithContext(store.WithActor(r.Context(), "ops")))
	})
	// "How it works" plays its examples on a fresh sandbox of its own.
	mux.HandleFunc("GET /service-definitions/api/tour", func(w http.ResponseWriter, r *http.Request) {
		t, err := servicedefsdemo.BuildTour(r.Context())
		writeJSON(w, t, err)
	})
	mux.HandleFunc("POST /service-definitions/api/reset", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]bool{"ok": true}, fresh())
	})
	return nil
}
