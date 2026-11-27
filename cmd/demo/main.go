// Command demo serves the interactive demos. The landing page is the
// domain map; components with a demo link into it.
//
//	go run ./cmd/demo            # http://localhost:8080 in the main checkout
//	go run ./cmd/demo -addr localhost:9000   # another address
//
// Each checkout has its own port (checkout.go): the main checkout 8080, a
// worktree 8081 and up, kept from its first start. Every page shows which
// checkout it comes from.
//
// Each demo lives under its own path (/claims-lifecycle/ and its API at
// /claims-lifecycle/api/), and registers its routes from its own file
// here. Demo state is in the local Postgres, in a schema per sandbox,
// shared by every browser tab; Reset starts over from the seed data.
// Journey and Claims Intake share one sandbox, as do Enrollment Intake,
// Eligibility and the Employer Portal.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"

	"coreadmin/internal/common/pgdb"
	"coreadmin/internal/common/protodoc"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/demos"
)

//go:embed web
var webFS embed.FS

// Each demo registers its routes on the mux.
var registrations = []func(*http.ServeMux) error{
	registerClaims,
	registerAdjudication,
	registerLifecycle,
	registerJourney,
	registerEnrollment,
	registerEmployerPortal, // after registerEnrollment: it shows that sandbox
	registerRemittance,
	registerProviderIntake,
	registerServiceDefinitions,
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
	addr := flag.String("addr", "", "listen address (default: this checkout's port, see checkout.go)")
	flag.Parse()
	here, err := thisCheckout()
	if err != nil {
		log.Fatal(err)
	}
	if *addr == "" {
		*addr = fmt.Sprintf("localhost:%d", here.Port)
	}
	// Everything runs on the local Postgres (see pgdb); this starts it if
	// it isn't running, and leaves it running.
	if _, err := pgdb.Pool(context.Background()); err != nil {
		log.Fatal(err)
	}
	log.Printf("Postgres: %s (sandboxes lifecycle, journey, enrollment, providers, each a schema per domain like journey_claims; throwaway s_* sandboxes per request)", pgdb.URL(pgdb.Database()))

	web, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /", withCheckout(web, here, http.FileServerFS(web)))
	mux.HandleFunc("GET /api/demos", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(demos.Catalog())
	})
	// The proto services on the map's lines, and one rendered.
	mux.HandleFunc("GET /api/interfaces", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, demos.Interfaces, nil)
	})
	// A service rendered for the methods a caller uses.
	mux.HandleFunc("GET /api/contract", func(w http.ResponseWriter, r *http.Request) {
		var methods []string
		if m := r.URL.Query().Get("methods"); m != "" {
			methods = strings.Split(m, ",")
		}
		c, err := protodoc.Interface(r.URL.Query().Get("service"), methods)
		writeJSON(w, c, err)
	})
	mux.HandleFunc("GET /api/interface", func(w http.ResponseWriter, r *http.Request) {
		in, ok := demos.InterfaceOn(r.URL.Query().Get("from"), r.URL.Query().Get("to"))
		if !ok {
			http.Error(w, "no proto interface on that line", http.StatusNotFound)
			return
		}
		c, err := in.Contract()
		writeJSON(w, map[string]any{"interface": in, "contract": c}, err)
	})
	for _, register := range registrations {
		if err := register(mux); err != nil {
			log.Fatal(err)
		}
	}

	log.Printf("demos on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
