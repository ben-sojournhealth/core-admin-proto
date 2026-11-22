package remittancedemo_test

import (
	"context"
	"strings"
	"testing"

	"coreadmin/internal/claims/remittancedemo"
)

// Every sample runs, and builds the 835 it describes.
func TestSamples(t *testing.T) {
	want := map[string]struct {
		claims  string // claim statuses, in order shown
		clps    string // CLP02 of each entry
		method  string
		noBuild bool
	}{
		"office-visit":   {"APPROVED", "1", "ACH", false},
		"preventive":     {"APPROVED", "1", "ACH", false},
		"deductible":     {"APPROVED", "1", "NONE", false},
		"multi-line":     {"APPROVED", "1", "ACH", false},
		"denied":         {"DENIED", "4", "NONE", false},
		"partial-denial": {"APPROVED", "1", "ACH", false},
		"batch":          {"APPROVED APPROVED APPROVED", "1 1 1", "ACH", false},
		"corrected":      {"ADJUSTED APPROVED", "22 1", "ACH", false},
		"void-netted":    {"VOIDED APPROVED APPROVED", "22 1", "ACH", false},
		"void-alone":     {"VOIDED APPROVED", "", "", true},
	}
	samples := remittancedemo.Samples()
	if len(samples) != len(want) {
		t.Fatalf("%d samples, %d expectations", len(samples), len(want))
	}
	for _, x := range samples {
		v, err := remittancedemo.Run(context.Background(), x.ID, "ACH")
		if err != nil {
			t.Fatalf("%s: %v", x.ID, err)
		}
		w := want[x.ID]
		var statuses, clps []string
		for _, c := range v.Claims {
			statuses = append(statuses, c.Status)
		}
		for _, s := range v.Segments {
			if strings.HasPrefix(s.Text, "CLP*") {
				clps = append(clps, strings.Split(s.Text, "*")[2])
			}
		}
		got := strings.Join(statuses, " ")
		if got != w.claims || strings.Join(clps, " ") != w.clps || (v.Error != "") != w.noBuild {
			t.Errorf("%s: claims %q, CLP02 %v, error %q", x.ID, got, clps, v.Error)
		}
		if !w.noBuild && v.Payment.Method != w.method {
			t.Errorf("%s: method %s, want %s", x.ID, v.Payment.Method, w.method)
		}
		// Every claim and line segment ties back to something shown.
		keys := map[string]bool{}
		for _, c := range v.Claims {
			keys[c.Key] = true
			for _, l := range c.Lines {
				keys[l.Key] = true
			}
		}
		for _, s := range v.Segments {
			if (s.Claim != "" && !keys[s.Claim]) || (s.Line != "" && !keys[s.Line]) {
				t.Errorf("%s: segment %s points at nothing shown", x.ID, s.Text)
			}
		}
	}
}

func TestCheckPayment(t *testing.T) {
	v, err := remittancedemo.Run(context.Background(), "office-visit", "CHECK")
	if err != nil {
		t.Fatal(err)
	}
	if v.Payment.Method != "CHECK" || !strings.HasPrefix(v.Segments[3].Text, "BPR*I*65*C*CHK") {
		t.Errorf("%s %s", v.Payment.Method, v.Segments[3].Text)
	}
}
