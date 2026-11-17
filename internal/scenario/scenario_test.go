package scenario_test

import (
	"testing"

	"coreadmin/internal/scenario"
)

// TestScenarios plays every scenario in the library from a fresh sandbox
// and requires every step's expectations to hold.
func TestScenarios(t *testing.T) {
	seen := map[string]bool{}
	for i := range scenario.Library {
		sc := &scenario.Library[i]
		if seen[sc.ID] {
			t.Fatalf("duplicate scenario id %q", sc.ID)
		}
		seen[sc.ID] = true
		t.Run(sc.ID, func(t *testing.T) {
			r, err := scenario.NewRunner()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Load(sc.ID); err != nil {
				t.Fatal(err)
			}
			for n := 1; ; n++ {
				res, ok := r.Step()
				if !ok {
					break
				}
				sr := r.Results[len(r.Results)-1]
				if sr.Error != "" {
					t.Errorf("step %d %q failed: %s", n, sc.Steps[n-1].Label, sr.Error)
				}
				for _, c := range sr.Checks {
					if !c.Pass {
						t.Errorf("step %d %q: expected %q, but %s\n  log: %q", n, sc.Steps[n-1].Label, c.Text, c.Detail, res.Lines)
					}
				}
			}
		})
	}
}
