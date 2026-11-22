// Command demo serves the interactive demos. The landing page is the
// domain map; components with a demo link into it.
//
//	go run ./cmd/demo            # then open http://localhost:8080
//	go run ./cmd/demo -addr localhost:8081   # a second copy, e.g. in another worktree
//
// Each demo lives under its own path (/claims-lifecycle/ and its API at
// /claims-lifecycle/api/), and registers its routes from its own file
// here. Demo state is in the local Postgres, in a schema per sandbox,
// shared by every browser tab; Reset starts over from the seed data.
// Journey and Claims Intake share one sandbox, as do Enrollment Intake and
// Eligibility.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"net/http"

	"coreadmin/internal/demos"
	"coreadmin/internal/pgdb"
	"coreadmin/internal/repo"
)

//go:embed web
var webFS embed.FS

// Each demo registers its routes on the mux.
var registrations = []func(*http.ServeMux) error{
	registerLifecycle,
	registerJourney,
	registerEnrollment,
	registerRemittance,
}

// newRepos gives a long-lived sandbox its schema in the demo database,
// dropped and recreated: on start, and on Reset.
func newRepos(schema string) (*repo.Repos, error) {
	return repo.Open(context.Background(), schema, true)
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
	log.Printf("Postgres: %s (sandboxes lifecycle, journey, enrollment, each a schema per domain like journey_claims; throwaway s_* sandboxes per request)", pgdb.URL(pgdb.Database()))

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
	for _, register := range registrations {
		if err := register(mux); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("demos on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
