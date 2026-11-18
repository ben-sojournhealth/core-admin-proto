package scenario_test

import (
	"regexp"
	"testing"

	"coreadmin/internal/scenario"
)

// Replacement and void claims get UUIDv7 ids; the short name clm_A-2
// resolves to them.
func TestReplacementIDs(t *testing.T) {
	r, err := scenario.NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Load("adjust-unpaid"); err != nil {
		t.Fatal(err)
	}
	for {
		if _, ok := r.Step(); !ok {
			break
		}
	}
	id := r.Sim.Resolve("clm_A-2")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("clm_A-2 resolves to %q, want a UUIDv7", id)
	}
	st, err := r.Sim.State()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range st.Claims {
		if c.ID == id && (c.Label != "clm_A-2" || c.OriginalLabel != "clm_A" || c.Number == "") {
			t.Errorf("replacement shows label %q, original %q, number %q", c.Label, c.OriginalLabel, c.Number)
		}
	}
}
