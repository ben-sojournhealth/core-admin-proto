package claimsdemo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"coreadmin/internal/claims/claimsdb"
	"coreadmin/internal/claims/claimsdemo"
	"coreadmin/internal/common/protodoc"
)

// The walkthrough's examples say what the slides claim they say.
func TestTour(t *testing.T) {
	tour, err := claimsdemo.BuildTour(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Every ClaimsService method is on the endpoints slide, once.
	placed := map[string]bool{}
	for _, g := range tour.Endpoints {
		for _, e := range g.Endpoints {
			if placed[e.Name] {
				t.Errorf("endpoint %s is placed twice", e.Name)
			}
			placed[e.Name] = true
			// Every operation claims.Service implements is called by an example.
			if e.Built && len(e.Examples) == 0 {
				t.Errorf("endpoint %s is built but no example calls it", e.Name)
			}
		}
	}
	for _, m := range tour.Service.Methods {
		if !placed[m.Name] {
			t.Errorf("ClaimsService.%s is not on the endpoints slide", m.Name)
		}
		delete(placed, m.Name)
	}
	for name := range placed {
		t.Errorf("the endpoints slide has %s, which ClaimsService doesn't", name)
	}

	// Every field of Claim is in one group of the data model slide.
	fields := protodoc.For("coreadmin.claims.v1.Claim").Messages["coreadmin.claims.v1.Claim"]
	grouped := map[string]bool{}
	for _, g := range tour.Model {
		for _, f := range g.Fields {
			if _, ok := fields[f.Name]; !ok {
				t.Errorf("model group %s has %s, which Claim doesn't", g.Title, f.Name)
			}
			if grouped[f.Name] {
				t.Errorf("Claim.%s is in two model groups", f.Name)
			}
			grouped[f.Name] = true
		}
	}
	for name := range fields {
		if !grouped[name] {
			t.Errorf("Claim.%s is not on the data model slide", name)
		}
	}
	// Every table is listed, with what it holds.
	if len(tour.Tables) != len(claimsdb.Tables)+1 {
		t.Errorf("%d tables listed, the schema has %d and claim_number_blocks", len(tour.Tables), len(claimsdb.Tables))
	}
	for _, tb := range tour.Tables {
		if tb.Holds == "" {
			t.Errorf("table %s has no description", tb.Name)
		}
	}

	// Each story, as the claims stand after each call: "number OUTCOME
	// payment" once adjudicated (+ what superseded it), "number PENDED
	// pends" before, with holds.
	want := map[string][]string{
		// Process, pend, reprocess; a preview and an estimate save nothing; an
		// adjudicated claim is not re-run.
		"decide": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"ProcessClaim: 26075-E-00001 APPROVED; 26075-E-00002 PENDED PRIOR_AUTH_REQUIRED",
			"PreviewReadjudication (saves nothing): 26075-E-00001 APPROVED; 26075-E-00002 PENDED PRIOR_AUTH_REQUIRED",
			"ReprocessClaim: 26075-E-00001 APPROVED; 26075-E-00002 APPROVED",
			"EstimateClaim (saves nothing): 26075-E-00001 APPROVED; 26075-E-00002 APPROVED",
			"ReprocessClaim refused: 26075-E-00001 APPROVED; 26075-E-00002 APPROVED",
		},
		// The outcome follows from the lines; only a full denial is DENIED.
		"outcomes": {
			"Plan Design: ",
			"ProcessClaim: 26075-E-00001 APPROVED",
			"ProcessClaim: 26075-E-00001 APPROVED; 26075-E-00002 PARTIALLY_DENIED",
			"ProcessClaim: 26075-E-00001 APPROVED; 26075-E-00002 PARTIALLY_DENIED; 26075-E-00003 DENIED",
		},
		// Before release nothing is reported: adjusting is refused, the claim is
		// reopened and re-run in place, and no second version appears. After
		// release, reopening is refused.
		"reopen": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"SelectForPayment: 26075-E-00001 APPROVED SELECTED",
			"AdjustClaim refused: 26075-E-00001 APPROVED SELECTED",
			"PreviewAdjustment (saves nothing) refused: 26075-E-00001 APPROVED SELECTED",
			"PreviewReadjudication (saves nothing): 26075-E-00001 APPROVED SELECTED",
			"ReopenClaim: 26075-E-00001 PENDED REOPENED",
			"ReprocessClaim: 26075-E-00001 APPROVED",
			"SelectForPayment: 26075-E-00001 APPROVED SELECTED",
			"ReleasePaymentRun: 26075-E-00001 APPROVED ISSUED",
			"ReopenClaim refused: 26075-E-00001 APPROVED ISSUED",
			"RecordPaymentEvent: 26075-E-00001 APPROVED CLEARED",
		},
		// A provider's correction of an unreported claim: the original is closed
		// as denied and gets its own answer; nothing is superseded.
		"providerBefore": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"ProcessClaim: 26075-E-00001 DENIED (REPLACED_BY_PROVIDER); 26075-E-00002 APPROVED replaces 26075-E-00001 (QUOTED_NUMBER)",
			"ProcessClaim: 26075-E-00001 DENIED (REPLACED_BY_PROVIDER); 26075-E-00002 APPROVED replaces 26075-E-00001 (QUOTED_NUMBER); 26075-E-00003 PENDED ORIGINAL_NOT_FOUND",
			"SelectForPayment: 26075-E-00001 DENIED (REPLACED_BY_PROVIDER) SELECTED; 26075-E-00002 APPROVED SELECTED replaces 26075-E-00001 (QUOTED_NUMBER); 26075-E-00003 PENDED ORIGINAL_NOT_FOUND",
			"ReleasePaymentRun: 26075-E-00001 DENIED (REPLACED_BY_PROVIDER) ISSUED; 26075-E-00002 APPROVED ISSUED replaces 26075-E-00001 (QUOTED_NUMBER); 26075-E-00003 PENDED ORIGINAL_NOT_FOUND",
		},
		// A provider's correction and void of a reported claim supersede it, with
		// numbers of their own.
		"providerAfter": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"SelectForPayment: 26075-E-00001 APPROVED SELECTED",
			"ReleasePaymentRun: 26075-E-00001 APPROVED ISSUED",
			"ProcessClaim: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED replaces 26075-E-00001 (QUOTED_NUMBER)",
			"SelectForPayment: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED SELECTED replaces 26075-E-00001 (QUOTED_NUMBER)",
			"ReleasePaymentRun: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED ISSUED replaces 26075-E-00001 (QUOTED_NUMBER)",
			"ProcessClaim: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED+VOID ISSUED replaces 26075-E-00001 (QUOTED_NUMBER); 26075-E-00003 VOID replaces 26075-E-00002 (QUOTED_NUMBER)",
		},
		// Our adjustment and void of a reported claim are versions of one number.
		"ours": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"SelectForPayment: 26075-E-00001 APPROVED SELECTED",
			"ReleasePaymentRun: 26075-E-00001 APPROVED ISSUED",
			"PreviewAdjustment (saves nothing): 26075-E-00001 APPROVED ISSUED",
			"AdjustClaim: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00001-2 APPROVED replaces 26075-E-00001 (LINKED)",
			"SelectForPayment: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00001-2 APPROVED SELECTED replaces 26075-E-00001 (LINKED)",
			"ReleasePaymentRun: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00001-2 APPROVED ISSUED replaces 26075-E-00001 (LINKED)",
			"VoidClaim: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00001-2 APPROVED+VOID ISSUED replaces 26075-E-00001 (LINKED); 26075-E-00001-3 VOID replaces 26075-E-00001-2 (LINKED)",
		},
		// An adjustment leaves the later claim as it was.
		"ripple": {
			"ProcessClaim: 26075-E-00001 APPROVED",
			"ProcessClaim: 26075-E-00001 APPROVED; 26075-E-00002 APPROVED",
			"SelectForPayment: 26075-E-00001 APPROVED SELECTED; 26075-E-00002 APPROVED SELECTED",
			"ReleasePaymentRun: 26075-E-00001 APPROVED ISSUED; 26075-E-00002 APPROVED ISSUED",
			"AdjustClaim: 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED ISSUED; 26075-E-00001-2 APPROVED replaces 26075-E-00001 (LINKED)",
			"PreviewAdjustment (saves nothing): 26075-E-00001 APPROVED+REPLACEMENT ISSUED; 26075-E-00002 APPROVED ISSUED; 26075-E-00001-2 APPROVED replaces 26075-E-00001 (LINKED)",
		},
	}
	for id, steps := range want {
		ex := tour.Examples[id]
		var got []string
		for _, c := range ex.Calls {
			got = append(got, describe(c))
		}
		if strings.Join(got, "\n") != strings.Join(steps, "\n") {
			t.Errorf("%s:\n%s\nwant:\n%s", id, strings.Join(got, "\n"), strings.Join(steps, "\n"))
		}
	}

	// What the read-only operations answer: the pended MRI would be
	// approved with its authorization; the selected visit, run again,
	// would be decided as it stands.
	projected := func(id string, n int) string {
		var out struct {
			Claim struct {
				Status       string `json:"status"`
				Adjudication struct {
					Outcome   string `json:"outcome"`
					TotalPaid struct {
						Cents string `json:"cents"`
					} `json:"total_paid"`
				} `json:"adjudication"`
			} `json:"claim"`
		}
		if err := json.Unmarshal(tour.Examples[id].Calls[n].Response, &out); err != nil {
			t.Fatal(err)
		}
		// protojson leaves a zero amount out.
		paid := out.Claim.Adjudication.TotalPaid.Cents
		if paid == "" {
			paid = "0"
		}
		return out.Claim.Status + " " + out.Claim.Adjudication.Outcome + " " + paid
	}
	if got := projected("decide", 2); got != "CLAIM_STATUS_ADJUDICATED CLAIM_OUTCOME_APPROVED 0" {
		t.Errorf("decide: the MRI's preview is %s", got)
	}
	if got := projected("reopen", 4); got != "CLAIM_STATUS_ADJUDICATED CLAIM_OUTCOME_APPROVED 10500" {
		t.Errorf("reopen: the visit's preview is %s", got)
	}

	// The outcomes slide draws each claim's lines.
	oc := tour.Examples["outcomes"].Calls
	var lines []string
	for _, c := range oc[len(oc)-1].Claims {
		for _, l := range c.Lines {
			lines = append(lines, c.Number[len(c.Number)-1:]+":"+l.Procedure+" "+l.Status)
		}
	}
	if got := strings.Join(lines, "; "); got != "1:99213 PAYABLE; 2:99214 PAYABLE; 2:99499 DENIED; 3:99499 DENIED" {
		t.Errorf("outcomes: lines %s", got)
	}

	// The ripple slide: the later claim stands at $96 and would now pay $0.
	if d, _ := tour.Examples["ripple"].Facts["later"].(*claimsdemo.DeltaView); d == nil || d.Held != 9600 || d.Projected != 0 {
		t.Errorf("ripple: later claim %+v", d)
	}

	// The delta slide: one replacement, against a reported original whose
	// payment is held, was returned, or was nothing because it was denied.
	deltas := []string{}
	for _, k := range []string{"PAID", "RETURNED", "DENIED"} {
		d, _ := tour.Examples["delta"].Facts[k].(*claimsdemo.DeltaView)
		if d == nil {
			t.Fatalf("delta %s missing", k)
		}
		deltas = append(deltas, fmt.Sprintf("%s %s %d→%d %+d", k, d.Basis, d.Held, d.Projected, d.Delta))
	}
	if got := strings.Join(deltas, "; "); got != "PAID PAYMENT 10500→6500 -4000; RETURNED PAYMENT_NOT_HELD 0→6500 +6500; DENIED PAYMENT 0→6500 +6500" {
		t.Errorf("deltas: %s", got)
	}

	// The 835 slide: paid, then reversed and netted, then a void that
	// can't be netted.
	var rems []string
	for _, r := range tour.Examples["remit"].Remittances {
		s := fmt.Sprintf("%d", r.Amount)
		if r.Error != "" {
			s = r.Error
		}
		for _, e := range r.Entries {
			s += fmt.Sprintf(" %s %s %d", e.Number, e.Status, e.Paid)
		}
		rems = append(rems, s)
	}
	if got := strings.Join(rems, "; "); got != "6500 26075-E-00001 PROCESSED 6500; 4000 26075-E-00001 REVERSAL -6500 26075-E-00001-2 PROCESSED 10500; NET_NEGATIVE" {
		t.Errorf("835s: %s", got)
	}
	// The data model slide draws the reopened claim's rows: one per change.
	re := tour.Examples["reopen"].Calls
	if c := re[len(re)-1].Claims[0]; c.Revisions != 7 || c.Runs != 3 {
		t.Errorf("reopen: the claim has %d revisions and %d runs", c.Revisions, c.Runs)
	}
}

// describe is a call and every claim after it.
func describe(c *claimsdemo.Call) string {
	s := c.Endpoint
	if c.ReadOnly {
		s += " (saves nothing)"
	}
	if c.Rejected != "" {
		s += " refused"
	}
	var cs []string
	for _, v := range c.Claims {
		// Once adjudicated: the outcome, plus what superseded it.
		d := v.Number + " " + v.Status
		if v.Status == "ADJUDICATED" {
			d = v.Number + " " + v.Outcome
		}
		if v.Superseded != "" {
			d += "+" + v.Superseded
		}
		if v.Denial != "" {
			d += " (" + v.Denial + ")"
		}
		if len(v.Pends) > 0 {
			d += " " + strings.Join(v.Pends, ",")
		}
		if v.Payment != "" {
			d += " " + v.Payment
		}
		if v.Replaces != "" {
			d += " replaces " + v.Replaces + " (" + v.Match + ")"
		}
		cs = append(cs, d)
	}
	return s + ": " + strings.Join(cs, "; ")
}
