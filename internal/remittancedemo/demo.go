// Package remittancedemo is the Remittance demo: adjudication results in,
// an 835 out. Each sample adjudicates its claims on a fresh sandbox and
// builds the remittance for them, so every run starts from the same place
// and nothing is kept between runs.
package remittancedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/dates"
	"coreadmin/internal/remittance"
	"coreadmin/internal/sim"
	"coreadmin/internal/x12"
)

// Sample is one way an adjudication becomes an 835.
type Sample struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Group   string `json:"group"`
	run     func(ctx context.Context, e *env) ([]*claimsv1.Claim, error)
}

// env is what a sample works with: a fresh sandbox, the builder, and the
// remittances sent earlier in the story.
type env struct {
	s       *sim.Sim
	b       *remittance.Builder
	earlier []*remittancev1.Remittance
	trace   int
	// How payments in this run are made.
	method remittancev1.PaymentMethod
}

func (e *env) nextTrace() string { e.trace++; return fmt.Sprintf("1002%02d", e.trace) }

// line is one service line to bill.
type line struct {
	proc    string
	dollars int64
}

// receive submits and adjudicates a claim from Dr. Chen's practice, as
// it would arrive from an 837.
func (e *env) receive(ctx context.Context, pcn, card string, auth string, lines ...line) (*claimsv1.Claim, error) {
	dos := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 3, Day: 12}, End: &commonv1.Date{Year: 2026, Month: 3, Day: 12}}
	c := &claimsv1.Claim{
		Type: claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL, Source: claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837,
		PatientControlNumber: pcn, MemberCardId: card, ServiceDates: dos, DiagnosisCodes: []string{"M2551"},
		BillingProvider: &claimsv1.ProviderRef{Npi: "1234567893", Tin: "311234567", Name: "Dayton Family Medicine LLC"},
	}
	if auth != "" {
		c.AuthorizationNumbers = []string{auth}
	}
	var total int64
	for i, l := range lines {
		c.Lines = append(c.Lines, &claimsv1.ClaimLine{
			LineNumber: int32(i + 1), ProcedureCode: l.proc, PlaceOfServiceCode: "11", UnitsMilli: 1000, ServiceDates: dos,
			Charge: &commonv1.Money{Cents: l.dollars * 100}, DiagnosisPointers: []int32{1},
		})
		total += l.dollars * 100
	}
	c.TotalCharge = &commonv1.Money{Cents: total}
	return e.s.Claims.Receive(ctx, c)
}

// report sends an earlier 835 for claims, so a later one can reverse it.
func (e *env) report(ctx context.Context, cs ...*claimsv1.Claim) error {
	r, err := e.b.Build(ctx, cs, payment(e.method, e.nextTrace(), e.b.Now()))
	if err != nil {
		return err
	}
	if r, err = e.b.Save(ctx, r); err != nil {
		return err
	}
	e.earlier = append(e.earlier, r)
	return nil
}

// denyUncovered makes the plan deny services no benefit covers, instead
// of pending them for review.
func (e *env) denyUncovered(ctx context.Context) error {
	plan, err := e.s.Repos.Plans.Get(ctx, "plan_acme_ppo")
	if err != nil {
		return err
	}
	for _, v := range plan.GetVersions() {
		v.DefaultBehavior = planv1.DefaultBenefitBehavior_DEFAULT_BENEFIT_BEHAVIOR_DENY
	}
	_, err = e.s.Repos.Plans.Update(ctx, plan)
	return err
}

// Samples lists the demo's samples.
func Samples() []Sample {
	return []Sample{
		{ID: "office-visit", Group: "One claim", Title: "Office visit with a copay",
			Summary: "One line. Allowed amount from the fee schedule; the member owes a $30 copay.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				c, err := e.receive(ctx, "RIV-88213", "ACM100101", "", line{"99213", 180})
				return one(c, err)
			}},
		{ID: "preventive", Group: "One claim", Title: "Preventive visit paid in full",
			Summary: "No member cost share: only the contractual write-off.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				c, err := e.receive(ctx, "RIV-88214", "ACM100101", "", line{"99396", 250})
				return one(c, err)
			}},
		{ID: "deductible", Group: "One claim", Title: "All to the deductible",
			Summary: "Processed, but the whole allowed amount is the member's deductible: nothing to pay.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				c, err := e.receive(ctx, "RIV-88215", "ACM100101", "", line{"29881", 3200})
				return one(c, err)
			}},
		{ID: "multi-line", Group: "One claim", Title: "Two service lines",
			Summary: "An office visit and an authorized MRI on one claim: one CLP, two SVC loops.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				c, err := e.receive(ctx, "PAT-40711", "ACM100102", "PA-2207", line{"99214", 220}, line{"70551", 1400})
				return one(c, err)
			}},
		{ID: "denied", Group: "One claim", Title: "Denied claim",
			Summary: "A service the plan doesn't cover. Nothing is paid, so the 835 is a notice only.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				if err := e.denyUncovered(ctx); err != nil {
					return nil, err
				}
				c, err := e.receive(ctx, "OKA-10552", "ACM100103", "", line{"99499", 120})
				return one(c, err)
			}},
		{ID: "partial-denial", Group: "One claim", Title: "One line denied",
			Summary: "The visit is paid; the second line isn't covered and the member may be billed for it.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				if err := e.denyUncovered(ctx); err != nil {
					return nil, err
				}
				c, err := e.receive(ctx, "OKA-10553", "ACM100103", "", line{"99213", 180}, line{"99499", 120})
				return one(c, err)
			}},
		{ID: "batch", Group: "One payment", Title: "Three claims, one check",
			Summary: "Claims for three patients of the same practice, paid together.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				var out []*claimsv1.Claim
				for _, x := range []struct {
					pcn, card, proc string
					d               int64
				}{
					{"RIV-88213", "ACM100101", "99213", 180}, {"PAT-40711", "ACM100102", "99396", 250}, {"RIV-88301", "ACM100103", "99214", 220},
				} {
					c, err := e.receive(ctx, x.pcn, x.card, "", line{x.proc, x.d})
					if err != nil {
						return nil, err
					}
					out = append(out, c)
				}
				return out, nil
			}},
		{ID: "corrected", Group: "After an earlier 835", Title: "Corrected claim",
			Summary: "Paid on an earlier 835, then corrected to a higher visit level. The old report is reversed and the difference paid.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				orig, err := e.receive(ctx, "RIV-88213", "ACM100101", "", line{"99213", 180})
				if err != nil {
					return nil, err
				}
				if err := e.report(ctx, orig); err != nil {
					return nil, err
				}
				fix := claimsCopy(orig)
				fix.Lines[0].ProcedureCode = "99214"
				adj, err := e.s.Claims.AdjustClaim(ctx, &claimsv1.AdjustClaimRequest{OriginalClaimId: orig.GetId(), CorrectedClaim: fix, Reason: "corrected E/M level"})
				if err != nil {
					return nil, err
				}
				return []*claimsv1.Claim{adj.GetClaim()}, nil
			}},
		{ID: "void-netted", Group: "After an earlier 835", Title: "Void, netted against a payment",
			Summary: "A paid claim is voided; the money comes back out of the next payment to the same practice.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				orig, err := e.receive(ctx, "RIV-88213", "ACM100101", "", line{"99213", 180})
				if err != nil {
					return nil, err
				}
				if err := e.report(ctx, orig); err != nil {
					return nil, err
				}
				v, err := e.s.Claims.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: orig.GetId(), Reason: "billed in error"})
				if err != nil {
					return nil, err
				}
				next, err := e.receive(ctx, "PAT-40790", "ACM100102", "", line{"99214", 220})
				if err != nil {
					return nil, err
				}
				return []*claimsv1.Claim{v.GetVoidClaim(), next}, nil
			}},
		{ID: "void-alone", Group: "After an earlier 835", Title: "Void with nothing to net against",
			Summary: "The reversal would make the payment negative. That recovery needs a PLB adjustment, which isn't built yet.",
			run: func(ctx context.Context, e *env) ([]*claimsv1.Claim, error) {
				orig, err := e.receive(ctx, "RIV-88213", "ACM100101", "", line{"99213", 180})
				if err != nil {
					return nil, err
				}
				if err := e.report(ctx, orig); err != nil {
					return nil, err
				}
				v, err := e.s.Claims.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: orig.GetId(), Reason: "billed in error"})
				if err != nil {
					return nil, err
				}
				return []*claimsv1.Claim{v.GetVoidClaim()}, nil
			}},
	}
}

func one(c *claimsv1.Claim, err error) ([]*claimsv1.Claim, error) {
	if err != nil {
		return nil, err
	}
	return []*claimsv1.Claim{c}, nil
}

// ---------- the view ----------

// View is a sample's run: the claims going in, the 835 coming out.
type View struct {
	Sample  Sample       `json:"sample"`
	Method  string       `json:"method"`
	Claims  []ClaimView  `json:"claims"`
	Payment *PaymentView `json:"payment,omitempty"`
	// Why no 835 could be built.
	Error    string    `json:"error,omitempty"`
	Segments []SegView `json:"segments"`
	X12      string    `json:"x12"`
	// The stored Remittance record (protojson, proto field names).
	Record json.RawMessage `json:"record,omitempty"`
	// Earlier 835s the story sent, rendered.
	Earlier []string `json:"earlier"`
}

type PaymentView struct {
	Method  string `json:"method"`
	Amount  int64  `json:"amount"`
	Trace   string `json:"trace"`
	Date    string `json:"date"`
	Payer   string `json:"payer"`
	Payee   string `json:"payee"`
	Entries int    `json:"entries"`
}

type ClaimView struct {
	Key     string `json:"key"`
	Number  string `json:"number"`
	PCN     string `json:"pcn"`
	Patient string `json:"patient"`
	Status  string `json:"status"`
	// "original", "corrected" or "void".
	Kind string `json:"kind"`
	// Reported on an earlier 835, and reversed on this one.
	Reversed bool `json:"reversed"`
	// For a corrected claim or void: the number of the version it replaces.
	Replaces string     `json:"replaces,omitempty"`
	Charge   int64      `json:"charge"`
	Paid     int64      `json:"paid"`
	Member   int64      `json:"member"`
	Lines    []LineView `json:"lines"`
}

type LineView struct {
	Key        string       `json:"key"`
	Line       int32        `json:"line"`
	Procedure  string       `json:"procedure"`
	Charge     int64        `json:"charge"`
	Allowed    int64        `json:"allowed"`
	Paid       int64        `json:"paid"`
	CostShares []AmountView `json:"costShares"`
	Denial     string       `json:"denial,omitempty"`
	CAS        []CASView    `json:"cas"`
	// Remark codes (RARCs), with a short reading.
	Remarks []CASView `json:"remarks"`
}

type AmountView struct {
	Label  string `json:"label"`
	Amount int64  `json:"amount"`
}

type CASView struct {
	Code   string `json:"code"`
	Amount int64  `json:"amount"`
	Text   string `json:"text"`
}

type SegView struct {
	Text string `json:"text"`
	// What the segment is, in a few words.
	Note string `json:"note"`
	// The claim and line it reports, for highlighting.
	Claim string `json:"claim,omitempty"`
	Line  string `json:"line,omitempty"`
	// Part of a reversal.
	Reversal bool `json:"reversal,omitempty"`
}

var record = protojson.MarshalOptions{UseProtoNames: true}

// Run plays a sample on a fresh sandbox and builds its 835.
func Run(ctx context.Context, id, method string) (*View, error) {
	var x *Sample
	for _, s := range Samples() {
		if s.ID == id {
			x = &s
			break
		}
	}
	if x == nil {
		return nil, fmt.Errorf("no sample %q", id)
	}
	s, err := sim.New()
	if err != nil {
		return nil, err
	}
	m := remittancev1.PaymentMethod_PAYMENT_METHOD_ACH
	if method == "CHECK" {
		m = remittancev1.PaymentMethod_PAYMENT_METHOD_CHECK
	}
	e := &env{s: s, method: m, b: &remittance.Builder{Repos: s.Repos, ContactName: "Provider Services", ContactPhone: "(937) 555-0100", Now: s.Claims.Now}}
	in, err := x.run(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", x.ID, err)
	}

	v := &View{Sample: *x, Method: strings.TrimPrefix(m.String(), "PAYMENT_METHOD_"), Segments: []SegView{}, Earlier: []string{}}
	for i, r := range e.earlier {
		v.Earlier = append(v.Earlier, remittance.X12(r, envelope(s, i+1)))
	}
	// The claims going in: each replacement or void after the version it
	// replaces.
	for _, c := range in {
		if id := c.GetOriginalClaimId(); id != "" {
			if o, err := s.Repos.Claims.Get(ctx, id); err == nil {
				cv := claimView(ctx, s, o)
				cv.Reversed = reportedEarlier(e.earlier, o.GetId())
				v.Claims = append(v.Claims, cv)
			}
		}
		cv := claimView(ctx, s, c)
		if c.GetOriginalClaimId() != "" {
			if o, err := s.Repos.Claims.Get(ctx, c.GetOriginalClaimId()); err == nil {
				cv.Replaces = claims.DisplayNumber(o)
			}
		}
		v.Claims = append(v.Claims, cv)
	}

	rem, err := e.b.Build(ctx, in, payment(m, e.nextTrace(), e.b.Now()))
	if err != nil {
		v.Error = err.Error()
		if errors.Is(err, remittance.ErrNetNegative) {
			v.Error = "The reversal takes back more than this payment pays. That recovery is reported as a provider-level adjustment (PLB) against a later payment, which isn't built yet."
		}
		return v, nil
	}
	v.Payment = &PaymentView{
		Method: strings.TrimPrefix(rem.GetPayment().GetMethod().String(), "PAYMENT_METHOD_"), Amount: rem.GetPayment().GetAmount().GetCents(),
		Trace: rem.GetPayment().GetTraceNumber(), Date: dates.Format(rem.GetPayment().GetDate()),
		Payer: rem.GetPayer().GetName(), Payee: rem.GetPayee().GetName(), Entries: len(rem.GetClaims()),
	}
	v.Record, _ = record.Marshal(rem)
	v.X12 = remittance.X12(rem, envelope(s, len(e.earlier)+1))
	v.Segments = segments(v.X12, rem)
	return v, nil
}

func reportedEarlier(earlier []*remittancev1.Remittance, claimID string) bool {
	for _, r := range earlier {
		for _, e := range r.GetClaims() {
			if e.GetClaimId() == claimID {
				return true
			}
		}
	}
	return false
}

func payment(m remittancev1.PaymentMethod, trace string, now time.Time) *remittancev1.RemittancePayment {
	p := &remittancev1.RemittancePayment{Method: m, TraceNumber: trace, Date: dates.FromTime(now)}
	if m == remittancev1.PaymentMethod_PAYMENT_METHOD_ACH {
		// Sandbox accounts: the plan's account at its bank, the practice's
		// at theirs.
		p.FromAccount = &remittancev1.BankAccount{RoutingNumber: "041000124", AccountNumber: "7700012345"}
		p.ToAccount = &remittancev1.BankAccount{RoutingNumber: "042000314", AccountNumber: "3300098765"}
	}
	return p
}

func envelope(s *sim.Sim, n int) remittance.Envelope {
	return remittance.Envelope{Sender: "COREADMIN", Receiver: "CLEARPATH", Control: fmt.Sprintf("%09d", 500+n), Time: s.Claims.Now()}
}

func claimView(ctx context.Context, s *sim.Sim, c *claimsv1.Claim) ClaimView {
	adj := c.GetAdjudication()
	cv := ClaimView{
		Key: c.GetId(), Number: claims.DisplayNumber(c), PCN: c.GetPatientControlNumber(),
		Status: strings.TrimPrefix(c.GetStatus().String(), "CLAIM_STATUS_"),
		Kind:   map[claimsv1.ClaimFrequency]string{7: "corrected", 8: "void"}[c.GetFrequency()],
		Charge: c.GetTotalCharge().GetCents(), Paid: adj.GetTotalPaid().GetCents(), Member: adj.GetTotalMemberResponsibility().GetCents(),
		Lines: []LineView{},
	}
	if cv.Kind == "" {
		cv.Kind = "original"
	}
	if m, err := s.Repos.Members.Get(ctx, c.GetMemberId()); err == nil {
		cv.Patient = m.GetName().GetFirst() + " " + m.GetName().GetLast()
	}
	if cv.Kind == "void" {
		return cv
	}
	for _, l := range c.GetLines() {
		la := l.GetAdjudication()
		lv := LineView{
			Key: fmt.Sprintf("%s:%d", c.GetId(), l.GetLineNumber()), Line: l.GetLineNumber(),
			Procedure: strings.Join(append([]string{l.GetProcedureCode()}, l.GetModifiers()...), "-"),
			Charge:    l.GetCharge().GetCents(), Allowed: la.GetPricing().GetAllowed().GetCents(), Paid: la.GetPaid().GetCents(),
			CostShares: []AmountView{}, CAS: []CASView{}, Remarks: []CASView{},
		}
		for _, r := range la.GetRemarkCodes() {
			lv.Remarks = append(lv.Remarks, CASView{Code: r, Text: map[string]string{"N130": "see plan benefit documents"}[r]})
		}
		for _, cs := range la.GetCostShares() {
			lv.CostShares = append(lv.CostShares, AmountView{Label: costShareName(cs.GetType()), Amount: cs.GetAmount().GetCents()})
		}
		if d := la.GetDenial(); d != nil {
			lv.Denial = d.GetMessage()
			if d.GetMemberLiable() {
				lv.CostShares = append(lv.CostShares, AmountView{Label: "Not covered", Amount: la.GetCharge().GetCents()})
			}
		}
		for _, a := range la.GetCasAdjustments() {
			g := groupCode(a.GetGroup())
			lv.CAS = append(lv.CAS, CASView{Code: g + "-" + a.GetReasonCode(), Amount: a.GetAmount().GetCents(), Text: carcText(g, a.GetReasonCode())})
		}
		cv.Lines = append(cv.Lines, lv)
	}
	return cv
}

// segments reads the rendered 835 back and ties each segment to the claim
// and line it reports.
func segments(raw string, rem *remittancev1.Remittance) []SegView {
	ic, err := x12.Parse([]byte(raw))
	if err != nil {
		return nil
	}
	var out []SegView
	entry, ln := -1, -1
	var claimKey, lineKey string
	rev := false
	for _, sg := range ic.Segments {
		switch sg.ID() {
		case "CLP":
			entry++
			ln, lineKey = -1, ""
			e := rem.GetClaims()[entry]
			claimKey = e.GetClaimId()
			rev = e.GetStatus() == remittancev1.RemittanceClaimStatus_REMITTANCE_CLAIM_STATUS_REVERSAL
		case "SVC":
			ln++
			e := rem.GetClaims()[entry]
			lineKey = fmt.Sprintf("%s:%d", e.GetClaimId(), e.GetLines()[ln].GetLineNumber())
		case "SE", "GE", "IEA", "LX":
			claimKey, lineKey, rev = "", "", false
		}
		sv := SegView{Text: sg.String(ic.Delims) + "~", Note: note(sg)}
		if entry >= 0 && claimKey != "" {
			sv.Claim, sv.Line, sv.Reversal = claimKey, lineKey, rev
		}
		out = append(out, sv)
	}
	return out
}

// note says what a segment is.
func note(s x12.Segment) string {
	switch s.ID() {
	case "ISA", "GS", "ST", "SE", "GE", "IEA":
		return "envelope"
	case "BPR":
		if s.E(1) == "H" {
			return "notice only: nothing paid"
		}
		return "payment: " + map[string]string{"ACH": "ACH transfer", "CHK": "check"}[s.E(4)]
	case "TRN":
		return "trace: check or EFT number"
	case "DTM":
		return map[string]string{"405": "production date", "050": "date received", "472": "date of service", "150": "service from", "151": "service to"}[s.E(1)]
	case "N1":
		return map[string]string{"PR": "payer", "PE": "payee"}[s.E(1)]
	case "N3", "N4":
		return "payer address"
	case "PER":
		return "payer contact"
	case "REF":
		return "payee tax id"
	case "LX":
		return "claims follow"
	case "CLP":
		return map[string]string{"1": "claim: processed", "4": "claim: denied", "22": "claim: reversal"}[s.E(2)]
	case "NM1":
		return map[string]string{"QC": "patient", "82": "rendering provider"}[s.E(1)]
	case "SVC":
		return "service line: charge, paid"
	case "CAS":
		return "adjustments: " + map[string]string{"CO": "provider writes off", "PR": "patient owes", "OA": "other", "PI": "payer reduction"}[s.E(1)]
	case "AMT":
		return "allowed amount"
	case "LQ":
		return "remark code"
	}
	return ""
}

func groupCode(g claimsv1.CasGroup) string {
	return map[claimsv1.CasGroup]string{1: "CO", 2: "PR", 3: "OA", 4: "PI"}[g]
}

func costShareName(t claimsv1.CostShareType) string {
	return map[claimsv1.CostShareType]string{1: "Deductible", 2: "Copay", 3: "Coinsurance"}[t]
}

// carcText is a short reading of the claim adjustment reason codes the
// engine uses.
func carcText(group, code string) string {
	t := map[string]string{
		"1": "deductible", "2": "coinsurance", "3": "copay", "45": "above the fee schedule",
		"96": "not covered", "27": "after coverage ended", "B7": "provider not eligible",
	}[code]
	if t == "" {
		t = "reason " + code
	}
	return t
}

func claimsCopy(c *claimsv1.Claim) *claimsv1.Claim { return proto.Clone(c).(*claimsv1.Claim) }
