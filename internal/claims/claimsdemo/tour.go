// Package claimsdemo is the "How claims works" walkthrough: the service's
// endpoints, the claim record, and examples of decisions, corrections,
// adjustments and payments. Every example is played on a fresh sandbox by
// calling the real ClaimsService operations, so what the slides show is
// what the code does today; a test holds the slides' claims to it.
package claimsdemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/claims/claimsdb"
	"coreadmin/internal/claims/remittance"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/protodoc"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/sim"
)

const serviceName = "coreadmin.claims.v1.ClaimsService"

// Tour is everything the walkthrough shows.
type Tour struct {
	// ClaimsService as declared in proto.
	Service *protodoc.Contract `json:"service"`
	// Its methods, grouped by what they're for, and whether claims.Service
	// implements each.
	Endpoints []EndpointGroup `json:"endpoints"`
	// The Claim record's fields, grouped, with their proto comments.
	Model []FieldGroup `json:"model"`
	// The Claims schema's tables.
	Tables   []Table             `json:"tables"`
	Examples map[string]*Example `json:"examples"`
}

type EndpointGroup struct {
	Title     string     `json:"title"`
	Endpoints []Endpoint `json:"endpoints"`
}

type Endpoint struct {
	Name string `json:"name"`
	// A few words.
	Does string `json:"does"`
	// claims.Service has the method. False: declared in proto only.
	Built bool   `json:"built"`
	Doc   string `json:"doc"`
	// Which examples call it.
	Examples []string `json:"examples"`
}

// endpointGroups places every ClaimsService method. A test checks none is
// missing.
var endpointGroups = []EndpointGroup{
	{Title: "Decide", Endpoints: []Endpoint{
		{Name: "ProcessClaim", Does: "take in a claim and adjudicate it"},
		{Name: "ReprocessClaim", Does: "re-run a pended claim"},
	}},
	{Title: "Change it before it is reported", Endpoints: []Endpoint{
		{Name: "ReopenClaim", Does: "back to pended, to re-run in place"},
	}},
	{Title: "Change it after it is reported", Endpoints: []Endpoint{
		{Name: "AdjustClaim", Does: "replace it with a corrected version"},
		{Name: "VoidClaim", Does: "cancel it"},
	}},
	{Title: "Ask, saving nothing", Endpoints: []Endpoint{
		{Name: "EstimateClaim", Does: "what a claim we don't have would pay"},
		{Name: "PreviewReadjudication", Does: "an unreported claim, run again now"},
		{Name: "PreviewAdjustment", Does: "a reported claim's replacement and delta"},
	}},
	{Title: "Read", Endpoints: []Endpoint{
		{Name: "GetClaim", Does: "one claim"},
		{Name: "ListClaims", Does: "claims by customer, member, status, payment state"},
	}},
}

type FieldGroup struct {
	Title string `json:"title"`
	// Where these fields come from.
	By     string  `json:"by"`
	Fields []Field `json:"fields"`
}

type Field struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
	// For a message field, its type's short name.
	Type string `json:"type,omitempty"`
}

// modelGroups places every field of Claim. A test checks none is missing.
var modelGroups = []struct {
	title, by string
	fields    []string
}{
	{"Identity", "set on receipt, never changed", []string{"id", "claim_number", "claim_version", "received_time", "clearinghouse_received_time", "timely_filing_date", "source", "source_record", "clearinghouse_id", "clearinghouse_trace_number"}},
	{"As submitted", "from the provider, kept verbatim; the only inputs adjudication reads", []string{"type", "frequency", "patient_control_number", "payer_claim_control_number", "member_card_id", "submitted_patient", "submitted_subscriber",
		"billing_provider", "rendering_provider", "referring_provider", "facility", "diagnosis_codes", "service_dates", "total_charge", "lines", "institutional", "authorization_numbers"}},
	{"Also submitted", "from the provider, kept verbatim; situational, and not read by adjudication yet", []string{"submitted_transaction", "submitted_coverage", "submitted_payer", "pay_to_address", "pay_to_plan",
		"place_of_service_code", "indicators", "related_causes", "dates", "attachments", "contract", "patient_paid", "references", "file_information", "notes",
		"ambulance_transport", "spinal_manipulation", "conditions", "diagnosis_code_qualifiers", "anesthesia_procedure_codes", "condition_codes", "repricing",
		"primary_care_provider", "service_facility", "supervising_provider", "ambulance_pickup", "ambulance_dropoff", "other_insurance", "unmapped_segments"}},
	{"Examiner inputs", "from examiners; adjudication honors them on every run", []string{"original_link", "pend_overrides"}},
	{"Decision", "from adjudication: output only, rewritten by every run", []string{"status", "adjudication", "adjudication_history", "customer_id", "member_id", "subscriber_id", "original_claim_id"}},
	{"Superseded", "set once, when a replacement or void of it is adjudicated", []string{"superseded_by"}},
	{"Payment", "from the payment system", []string{"payment"}},
	{"Record", "stamped by the store", []string{"audit"}},
}

type Table struct {
	Name    string  `json:"name"`
	Holds   string  `json:"holds"`
	Indexes []Index `json:"indexes"`
}

type Index struct {
	Name string `json:"name"`
	On   string `json:"on"`
	Why  string `json:"why"`
}

// What each table holds, by name. A test checks every table has a line.
var tableHolds = map[string]string{
	"claims":              "Claim: submitted fields, every adjudication run, payment state",
	"family_accumulators": "FamilyAccumulators: one ledger per subscriber unit, plan and benefit period",
	"fee_schedules":       "FeeSchedule: allowed amounts by procedure code",
	"ingestion_jobs":      "IngestionJob: each 837 file received, and its 999",
	"service_definitions": "ServiceDefinition: named groups of services by code",
	"remittances":         "Remittance: each 835 as sent",
	"claim_number_blocks": "Claim numbers reserved per file; never reused",
}

// Example is one story, played on its own sandbox.
type Example struct {
	Calls []*Call `json:"calls"`
	// Facts the slides quote, by name.
	Facts map[string]any `json:"facts,omitempty"`
	// 835s built in the story, oldest first.
	Remittances []*RemittanceView `json:"remittances,omitempty"`
}

// Call is one operation in a story.
type Call struct {
	// The ClaimsService method, or the stand-in's function.
	Endpoint string `json:"endpoint"`
	// "rpc": a ClaimsService operation. "standin": the stand-in for the
	// payment system (internal/claims/payment.go). "remittance": the 835
	// builder. "setup": a direct edit of another domain's data.
	Kind string `json:"kind"`
	// What happens, in a few words.
	Label string `json:"label"`
	// The operation calculates and saves nothing (EstimateClaim and the
	// two previews).
	ReadOnly bool `json:"readOnly,omitempty"`
	// Request and response, protojson with proto field names.
	Request  json.RawMessage `json:"request,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	// Why the call was refused.
	Rejected string `json:"rejected,omitempty"`
	// The service's activity log lines for the call.
	Log   []string   `json:"log"`
	Delta *DeltaView `json:"delta,omitempty"`
	// Every claim in the sandbox after the call, in the order received.
	Claims []*ClaimView `json:"claims"`
}

type DeltaView struct {
	Basis     string `json:"basis"`
	Held      int64  `json:"held"`
	Projected int64  `json:"projected"`
	Delta     int64  `json:"delta"`
}

// ClaimView is a claim's state, as the slides draw it.
type ClaimView struct {
	ID     string `json:"id"`
	Number string `json:"number"`
	// "original", "corrected" or "void" (frequency 1, 7, 8).
	Kind string `json:"kind"`
	// A version of an existing number (our adjustment or void), rather
	// than a submission with a number of its own.
	Version   int32  `json:"version"`
	Source    string `json:"source"`
	Procedure string `json:"procedure"`
	// RECEIVED, PENDED or ADJUDICATED.
	Status string `json:"status"`
	// What was decided, once adjudicated: APPROVED, PARTIALLY_DENIED,
	// DENIED or VOID.
	Outcome string `json:"outcome,omitempty"`
	// What took its place, if anything: REPLACEMENT or VOID.
	Superseded string   `json:"superseded,omitempty"`
	Pends      []string `json:"pends"`
	// The number of the claim it replaces or voids, once matched.
	Replaces string `json:"replaces,omitempty"`
	// How adjudication matched the original.
	Match string `json:"match,omitempty"`
	// The number the provider quoted (REF*F8).
	Quoted string `json:"quoted,omitempty"`
	// Adjudicated plan payment and member responsibility, cents.
	Pays int64 `json:"pays"`
	Owes int64 `json:"owes"`
	// Claim.payment: empty until a payment run selects the claim.
	Payment string `json:"payment,omitempty"`
	Trace   string `json:"trace,omitempty"`
	// Why every line was denied, when one reason denied them all.
	Denial string `json:"denial,omitempty"`
	// Each line's result: its procedure and PAYABLE, DENIED or PENDED.
	Lines []LineView `json:"lines"`
	// Adjudication runs so far.
	Runs int `json:"runs"`
	// Rows in the claims table for this claim: one per version written.
	Revisions int `json:"revisions"`
}

type LineView struct {
	Procedure string `json:"procedure"`
	Status    string `json:"status"`
	Paid      int64  `json:"paid"`
}

// RemittanceView is an 835, as the slides draw it.
type RemittanceView struct {
	Payee   string            `json:"payee"`
	Payer   string            `json:"payer"`
	Trace   string            `json:"trace"`
	Amount  int64             `json:"amount"`
	Entries []RemittanceEntry `json:"entries"`
	// Why it could not be built.
	Error string `json:"error,omitempty"`
}

type RemittanceEntry struct {
	Number string `json:"number"`
	// CLP02: 1 processed, 4 denied, 22 reversal.
	Status   string `json:"status"`
	Paid     int64  `json:"paid"`
	Reversal bool   `json:"reversal"`
}

var record = protojson.MarshalOptions{UseProtoNames: true}

// BuildTour plays every example and gathers what the slides show.
func BuildTour(ctx context.Context) (*Tour, error) {
	svc, err := protodoc.Interface(serviceName, nil)
	if err != nil {
		return nil, err
	}
	t := &Tour{Service: svc, Examples: map[string]*Example{}}

	stories := []struct {
		id  string
		run func(*story)
	}{
		{"decide", decide}, {"outcomes", outcomes}, {"reopen", reopen}, {"providerBefore", providerBefore},
		{"ours", ours}, {"providerAfter", providerAfter}, {"ripple", ripple}, {"delta", deltas}, {"remit", remit},
	}
	for _, x := range stories {
		ex, err := play(ctx, x.run)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", x.id, err)
		}
		t.Examples[x.id] = ex
	}

	used := map[string][]string{}
	for _, x := range stories {
		seen := map[string]bool{}
		for _, c := range t.Examples[x.id].Calls {
			if c.Kind == "rpc" && !seen[c.Endpoint] {
				seen[c.Endpoint] = true
				used[c.Endpoint] = append(used[c.Endpoint], x.id)
			}
		}
	}
	docs := map[string]string{}
	for _, m := range svc.Methods {
		docs[m.Name] = m.Doc
	}
	impl := reflect.TypeOf(&claims.Service{})
	for _, g := range endpointGroups {
		out := EndpointGroup{Title: g.Title}
		for _, e := range g.Endpoints {
			_, e.Built = impl.MethodByName(e.Name)
			e.Doc, e.Examples = docs[e.Name], used[e.Name]
			out.Endpoints = append(out.Endpoints, e)
		}
		t.Endpoints = append(t.Endpoints, out)
	}

	claimDocs := protodoc.For("coreadmin.claims.v1.Claim").Messages["coreadmin.claims.v1.Claim"]
	for _, g := range modelGroups {
		out := FieldGroup{Title: g.title, By: g.by}
		for _, name := range g.fields {
			f := claimDocs[name]
			typ := f.Message
			if typ == "" {
				typ = f.Enum
			}
			out.Fields = append(out.Fields, Field{Name: name, Doc: f.Doc, Type: typ[strings.LastIndex(typ, ".")+1:]})
		}
		t.Model = append(t.Model, out)
	}

	for _, tb := range claimsdb.Tables {
		v := Table{Name: tb.Name, Holds: tableHolds[tb.Name], Indexes: []Index{}}
		for _, ix := range tb.Indexes {
			on := strings.Join(ix.Path, ".")
			if ix.GIN {
				on = "the whole document"
			}
			v.Indexes = append(v.Indexes, Index{Name: ix.Name, On: on, Why: ix.Why})
		}
		t.Tables = append(t.Tables, v)
	}
	t.Tables = append(t.Tables, Table{Name: "claim_number_blocks", Holds: tableHolds["claim_number_blocks"], Indexes: []Index{}})
	return t, nil
}

// ---------- playing a story ----------

// story is one example in progress: a sandbox and the calls made so far.
type story struct {
	ctx context.Context
	s   *sim.Sim
	ex  *Example
	b   *remittance.Builder
	// The first thing that didn't go as the story expects.
	err   error
	trace int
}

func play(ctx context.Context, run func(*story)) (*Example, error) {
	repos, drop, err := repo.NewSandbox(ctx)
	if err != nil {
		return nil, err
	}
	defer drop()
	s, err := sim.NewIn(repos)
	if err != nil {
		return nil, err
	}
	// Name claims by their claim numbers, as people see them.
	s.UseShortIDs(false)
	st := &story{ctx: ctx, s: s, ex: &Example{Facts: map[string]any{}}}
	st.b = &remittance.Builder{Repos: s.Repos, ContactName: "Provider Services", ContactPhone: "(937) 555-0100", Now: s.Claims.Now}
	run(st)
	return st.ex, st.err
}

// message is a request or response; nil for none.
type message interface {
	proto.Message
	comparable
}

// rpc calls a ClaimsService operation and records it. A refusal the story
// doesn't expect ends the story.
func rpc[Req, Resp message](st *story, label string, f func(context.Context, Req) (Resp, error), req Req) Resp {
	resp, err := record1(st, "rpc", label, f, req)
	if err != nil && st.err == nil {
		st.err = fmt.Errorf("%s: %w", label, err)
	}
	return resp
}

// refused calls an operation the story expects to be refused.
func refused[Req, Resp message](st *story, label string, f func(context.Context, Req) (Resp, error), req Req) {
	if _, err := record1(st, "rpc", label, f, req); err == nil && st.err == nil {
		st.err = fmt.Errorf("%s: expected a refusal", label)
	}
}

func record1[Req, Resp message](st *story, kind, label string, f func(context.Context, Req) (Resp, error), req Req) (Resp, error) {
	var resp, none Resp
	if st.err != nil {
		return none, nil
	}
	// The method's name, from the request type: ProcessClaimRequest.
	name := strings.TrimSuffix(string(req.ProtoReflect().Descriptor().Name()), "Request")
	c := &Call{Endpoint: name, Kind: kind, Label: label}
	c.Request, _ = record.Marshal(req)
	c.ReadOnly = strings.HasPrefix(name, "Estimate") || strings.HasPrefix(name, "Preview")
	var err error
	res := st.s.Do("examiner", name, func(ctx context.Context) error {
		resp, err = f(ctx, req)
		return err
	})
	c.Log = res.Lines
	if err != nil {
		if !errors.Is(err, claims.ErrRejected) {
			return none, err
		}
		c.Rejected = strings.TrimPrefix(err.Error(), claims.ErrRejected.Error()+": ")
	} else {
		c.Response, _ = record.Marshal(resp)
		var d *claimsv1.AdjustmentDelta
		switch r := any(resp).(type) {
		case *claimsv1.AdjustClaimResponse:
			d = r.GetDelta()
		case *claimsv1.PreviewAdjustmentResponse:
			d = r.GetDelta()
		}
		if d != nil {
			c.Delta = &DeltaView{Basis: strings.TrimPrefix(d.GetBasis().String(), "DELTA_BASIS_"), Held: d.GetOriginalPaid().GetCents(),
				Projected: d.GetProjectedPaid().GetCents(), Delta: d.GetDelta().GetCents()}
		}
	}
	st.snapshot(c)
	return resp, err
}

// other records something that isn't a ClaimsService operation.
func (st *story) other(kind, endpoint, label string, f func(ctx context.Context) error) {
	if st.err != nil {
		return
	}
	c := &Call{Endpoint: endpoint, Kind: kind, Label: label}
	res := st.s.Do("payments", endpoint, f)
	c.Log = res.Lines
	if res.Error != "" {
		c.Rejected = strings.TrimPrefix(res.Error, claims.ErrRejected.Error()+": ")
	}
	st.snapshot(c)
}

// The stand-in for the payment system.
func (st *story) selectForPayment(label string, ids ...string) {
	st.other("standin", "SelectForPayment", label, func(ctx context.Context) error {
		_, err := st.s.Claims.SelectForPayment(ctx, ids)
		return err
	})
}

func (st *story) releaseRun(label string) {
	st.other("standin", "ReleasePaymentRun", label, func(ctx context.Context) error {
		_, err := st.s.Claims.ReleasePaymentRun(ctx)
		return err
	})
}

func (st *story) bank(label, id, event string) {
	st.other("standin", "RecordPaymentEvent", label, func(ctx context.Context) error {
		_, err := st.s.Claims.RecordPaymentEvent(ctx, id, event)
		return err
	})
}

// snapshot adds the call with every claim's state after it.
func (st *story) snapshot(c *Call) {
	c.Claims = []*ClaimView{}
	if c.Log == nil {
		c.Log = []string{}
	}
	all, _, err := st.s.Repos.Claims.List(st.ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1000})
	if err != nil {
		st.err = err
		return
	}
	// UUIDv7 ids sort in the order received.
	sort.Slice(all, func(i, j int) bool { return all[i].GetId() < all[j].GetId() })
	byID := map[string]*claimsv1.Claim{}
	for _, x := range all {
		byID[x.GetId()] = x
	}
	for _, x := range all {
		adj := x.GetAdjudication()
		v := &ClaimView{
			ID: x.GetId(), Number: claims.DisplayNumber(x), Version: x.GetClaimVersion(),
			Kind:   map[claimsv1.ClaimFrequency]string{7: "corrected", 8: "void"}[x.GetFrequency()],
			Source: strings.TrimPrefix(x.GetSource().String(), "CLAIM_SOURCE_"),
			Status: strings.TrimPrefix(x.GetStatus().String(), "CLAIM_STATUS_"),
			Pends:  []string{},
			Pays:   adj.GetTotalPaid().GetCents(), Owes: adj.GetTotalMemberResponsibility().GetCents(),
			Quoted:    x.GetPayerClaimControlNumber(),
			Trace:     x.GetPayment().GetPaymentTraceNumber(),
			Runs:      len(x.GetAdjudicationHistory()),
			Revisions: int(x.GetAudit().GetRevision()),
		}
		if adj != nil {
			v.Runs++
		}
		if o := adj.GetOutcome(); o != claimsv1.ClaimOutcome_CLAIM_OUTCOME_UNSPECIFIED {
			v.Outcome = strings.TrimPrefix(o.String(), "CLAIM_OUTCOME_")
		}
		if by := x.GetSupersededBy(); by != nil {
			v.Superseded = strings.TrimPrefix(by.GetFrequency().String(), "CLAIM_FREQUENCY_")
		}
		if v.Kind == "" {
			v.Kind = "original"
		}
		if len(x.GetLines()) > 0 {
			v.Procedure = x.GetLines()[0].GetProcedureCode()
		}
		if adjudication.ClosedByProvider(x) {
			v.Denial = x.GetLines()[0].GetAdjudication().GetDenial().GetCode()
		}
		v.Lines = []LineView{}
		for _, l := range x.GetLines() {
			if la := l.GetAdjudication(); la != nil {
				v.Lines = append(v.Lines, LineView{Procedure: l.GetProcedureCode(),
					Status: strings.TrimPrefix(la.GetStatus().String(), "LINE_STATUS_"), Paid: la.GetPaid().GetCents()})
			}
		}
		for _, p := range adj.GetPends() {
			if p.GetResolvedTime() == nil {
				v.Pends = append(v.Pends, p.GetCode())
			}
		}
		if o := byID[x.GetOriginalClaimId()]; o != nil {
			v.Replaces = claims.DisplayNumber(o)
			v.Match = strings.TrimPrefix(adj.GetOriginalMatch().String(), "ORIGINAL_MATCH_")
		}
		if p := x.GetPayment(); p != nil {
			v.Payment = strings.TrimPrefix(p.GetState().String(), "PAYMENT_STATE_")
		}
		c.Claims = append(c.Claims, v)
		// Refusals name claims by id; people know them by number.
		c.Rejected = strings.ReplaceAll(c.Rejected, x.GetId(), v.Number)
	}
	st.ex.Calls = append(st.ex.Calls, c)
}

// ---------- claims, as submitted ----------

const (
	jordan = "ACM100101" // subscriber
	sam    = "ACM100102"
)

// submitted is a one-line professional claim from Dr. Chen's practice, as
// an 837 carries it: the member id as sent, nothing matched.
func submitted(pcn, card, procedure string, dollars int64, day int32) *claimsv1.Claim {
	dos := &commonv1.DateRange{Start: &commonv1.Date{Year: 2026, Month: 3, Day: day}, End: &commonv1.Date{Year: 2026, Month: 3, Day: day}}
	return &claimsv1.Claim{
		Type: claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL, Frequency: claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL,
		Source: claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837, PatientControlNumber: pcn, MemberCardId: card,
		BillingProvider: &claimsv1.ProviderRef{Npi: "1234567893", Tin: "311234567", Name: "Dayton Family Medicine LLC"},
		ServiceDates:    dos, TotalCharge: &commonv1.Money{Cents: dollars * 100},
		Lines: []*claimsv1.ClaimLine{{LineNumber: 1, ProcedureCode: procedure, PlaceOfServiceCode: "11", UnitsMilli: 1000,
			ServiceDates: dos, Charge: &commonv1.Money{Cents: dollars * 100}, DiagnosisPointers: []int32{1}}},
		DiagnosisCodes: []string{"M2551"},
	}
}

// recoded is a claim's content with another procedure and charge: what an
// examiner keys as the corrected claim.
func recoded(c *claimsv1.Claim, procedure string, dollars int64) *claimsv1.Claim {
	out := proto.Clone(c).(*claimsv1.Claim)
	out.Lines[0].ProcedureCode = procedure
	out.Lines[0].Charge = &commonv1.Money{Cents: dollars * 100}
	out.TotalCharge = &commonv1.Money{Cents: dollars * 100}
	return out
}

func (st *story) process(label string, c *claimsv1.Claim) *claimsv1.Claim {
	return rpc(st, label, st.s.Claims.ProcessClaim, &claimsv1.ProcessClaimRequest{Claim: c}).GetClaim()
}

// ---------- the stories ----------

// decide: the two endpoints that decide a claim.
func decide(st *story) {
	visit := st.process("Office visit arrives", submitted("RIV-88213", jordan, "99213", 180, 12))
	mri := st.process("MRI arrives with no authorization", submitted("PAT-40711", sam, "70551", 900, 11))
	auth := []*claimsv1.PendResolution{{Code: "PRIOR_AUTH_REQUIRED", LineNumber: 1, Note: "PA-7731"}}
	rpc(st, "What would it be with the authorization confirmed?", st.s.Claims.PreviewReadjudication, &claimsv1.PreviewReadjudicationRequest{ClaimId: mri.GetId(), Resolutions: auth})
	rpc(st, "Examiner confirms the authorization", st.s.Claims.ReprocessClaim, &claimsv1.ReprocessClaimRequest{
		ClaimId: mri.GetId(), Reason: "Auth PA-7731 confirmed by phone",
		Resolutions: auth,
	})
	rpc(st, "Estimate: what would this visit pay?", st.s.Claims.EstimateClaim, &claimsv1.EstimateClaimRequest{
		Claim: submitted("EST-1", jordan, "99214", 250, 13)})
	refused(st, "Re-run an adjudicated claim", st.s.Claims.ReprocessClaim, &claimsv1.ReprocessClaimRequest{ClaimId: visit.GetId(), Reason: "Run it again"})
}

// outcomes: what an adjudicated claim decided follows from its lines.
func outcomes(st *story) {
	st.denyUncovered()
	st.process("Office visit: its one line is payable", submitted("RIV-88301", jordan, "99213", 180, 3))
	two := submitted("RIV-88302", jordan, "99214", 370, 5)
	two.Lines = append(two.Lines, proto.Clone(two.Lines[0]).(*claimsv1.ClaimLine))
	two.Lines[0].Charge = &commonv1.Money{Cents: 25000}
	two.Lines[1].LineNumber, two.Lines[1].ProcedureCode, two.Lines[1].Charge = 2, "99499", &commonv1.Money{Cents: 12000}
	st.process("Two lines: one payable, one not covered", two)
	st.process("One line, not covered", submitted("RIV-88303", jordan, "99499", 120, 9))
}

// denyUncovered makes the plan deny services no benefit covers, instead
// of pending them for review.
func (st *story) denyUncovered() {
	st.other("setup", "Plan Design", "Plan set to deny uncovered services", func(ctx context.Context) error {
		plan, err := st.s.Repos.Plans.Get(ctx, sim.PlanID)
		if err != nil {
			return err
		}
		for _, v := range plan.GetVersions() {
			v.DefaultBehavior = planv1.DefaultBenefitBehavior_DEFAULT_BENEFIT_BEHAVIOR_DENY
		}
		_, err = st.s.Repos.Plans.Update(ctx, plan)
		return err
	})
}

// pay runs a payment run to release: the point at which the claims'
// results are reported.
func (st *story) pay(what string) {
	st.selectForPayment("Payment run selects " + what)
	st.releaseRun("Run released: reported and paid")
}

// reopen: before a claim's payment is released nothing has been reported,
// so it is reopened and re-run in place.
func reopen(st *story) {
	c := st.process("Office visit arrives", submitted("RIV-88213", jordan, "99214", 250, 12))
	st.selectForPayment("Payment run selects it", c.GetId())
	refused(st, "Adjust it: nothing reported yet", st.s.Claims.AdjustClaim, &claimsv1.AdjustClaimRequest{OriginalClaimId: c.GetId(), Reason: "Visit level in question"})
	refused(st, "Preview an adjustment: nothing reported yet", st.s.Claims.PreviewAdjustment, &claimsv1.PreviewAdjustmentRequest{OriginalClaimId: c.GetId()})
	rpc(st, "Preview running it again", st.s.Claims.PreviewReadjudication, &claimsv1.PreviewReadjudicationRequest{ClaimId: c.GetId()})
	rpc(st, "Reopen it: out of the run, back to pended", st.s.Claims.ReopenClaim, &claimsv1.ReopenClaimRequest{ClaimId: c.GetId(), Reason: "Visit level in question"})
	rpc(st, "Records confirm it: reprocess", st.s.Claims.ReprocessClaim, &claimsv1.ReprocessClaimRequest{ClaimId: c.GetId(), Reason: "Records confirm the visit level"})
	st.selectForPayment("Payment run selects it", c.GetId())
	st.releaseRun("Run released: reported and paid")
	refused(st, "Reopen it now: already reported", st.s.Claims.ReopenClaim, &claimsv1.ReopenClaimRequest{ClaimId: c.GetId(), Reason: "Second thoughts"})
	st.bank("Bank reports it cleared", c.GetId(), "clear")
}

// corrects is a provider's corrected claim (frequency 7) or void
// (frequency 8): a new submission quoting the number of the claim it
// replaces.
func corrects(c *claimsv1.Claim, f claimsv1.ClaimFrequency, quoted string) *claimsv1.Claim {
	c.Frequency, c.PayerClaimControlNumber = f, quoted
	return c
}

const (
	replacement = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	void        = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
)

// providerBefore: a provider's corrected claim arrives before the
// original's result was reported. The original gets an answer of its own.
func providerBefore(st *story) {
	orig := st.process("Office visit arrives", submitted("RIV-88213", jordan, "99213", 180, 12))
	st.process("Provider sends a corrected claim (frequency 7)", corrects(submitted("RIV-88213", jordan, "99214", 250, 12), replacement, orig.GetClaimNumber()))
	st.process("A correction quoting a number we never issued", corrects(submitted("PAT-39002", sam, "99213", 180, 9), replacement, "26040-E-00417"))
	st.pay("both answers")
}

// providerAfter: a provider's corrected claim and void arrive after the
// result was reported. They are new submissions that supersede it.
func providerAfter(st *story) {
	orig := st.process("Office visit arrives", submitted("RIV-88213", jordan, "99213", 180, 12))
	st.pay("it")
	corrected := st.process("Provider sends a corrected claim (frequency 7)", corrects(submitted("RIV-88213", jordan, "99214", 250, 12), replacement, orig.GetClaimNumber()))
	st.pay("the corrected claim")
	st.process("Provider sends a void (frequency 8)", corrects(submitted("RIV-88213", jordan, "99214", 250, 12), void, corrected.GetClaimNumber()))
}

// ours: an adjustment and a void we make, after the result was reported,
// are versions of one number.
func ours(st *story) {
	in := submitted("RIV-88213", jordan, "99214", 250, 12)
	c := st.process("Long office visit arrives", in)
	st.pay("it")
	fix := recoded(in, "99213", 180)
	rpc(st, "Measure the adjustment first", st.s.Claims.PreviewAdjustment, &claimsv1.PreviewAdjustmentRequest{
		OriginalClaimId: c.GetId(), CorrectedClaim: fix})
	adj := rpc(st, "Adjust: it was a shorter visit", st.s.Claims.AdjustClaim, &claimsv1.AdjustClaimRequest{
		OriginalClaimId: c.GetId(), CorrectedClaim: fix, Reason: "Wrong visit level"})
	st.pay("the replacement")
	rpc(st, "Void: the visit never happened", st.s.Claims.VoidClaim, &claimsv1.VoidClaimRequest{
		ClaimId: adj.GetClaim().GetId(), Reason: "Billed in error: service not rendered"})
}

// ripple: an adjustment changes what a later claim should have paid.
// The later claim is left as it is, and nothing notices; the story ends by
// measuring the gap with a preview.
func ripple(st *story) {
	in := submitted("RIV-88001", jordan, "29881", 2500, 10)
	surgery := st.process("Surgery: $1,200 to the deductible", in)
	withAuth := submitted("RIV-88002", jordan, "70551", 900, 12)
	withAuth.AuthorizationNumbers = []string{"PA-1001"}
	mri := st.process("MRI, two days later: the deductible is partly met", withAuth)
	st.pay("both")
	rpc(st, "The surgery was really an office visit", st.s.Claims.AdjustClaim, &claimsv1.AdjustClaimRequest{
		OriginalClaimId: surgery.GetId(), CorrectedClaim: recoded(in, "99213", 180), Reason: "Wrong procedure code"})
	rpc(st, "What would the MRI pay if decided today?", st.s.Claims.PreviewAdjustment, &claimsv1.PreviewAdjustmentRequest{
		OriginalClaimId: mri.GetId()})
	if st.err == nil {
		st.ex.Facts["later"] = st.ex.Calls[len(st.ex.Calls)-1].Delta
		st.ex.Facts["laterNumber"] = claims.DisplayNumber(mri)
	}
}

// deltas: what the payee holds for a reported original, in each state its
// payment can be in.
func deltas(st *story) {
	measure := func(key, label string, c *claimsv1.Claim, fix *claimsv1.Claim) {
		rpc(st, label, st.s.Claims.PreviewAdjustment, &claimsv1.PreviewAdjustmentRequest{
			OriginalClaimId: c.GetId(), CorrectedClaim: fix})
		if st.err == nil {
			st.ex.Facts[key] = st.ex.Calls[len(st.ex.Calls)-1].Delta
		}
	}
	a := submitted("RIV-88101", jordan, "99214", 250, 3)
	paid := st.process("Visit A arrives", a)
	b := submitted("RIV-88102", jordan, "99214", 250, 5)
	bounced := st.process("Visit B arrives", b)
	// Denied: the plan denies what no benefit covers.
	st.denyUncovered()
	d := submitted("RIV-88104", jordan, "99499", 120, 11)
	denied := st.process("Visit D arrives with an uncovered code", d)
	st.pay("all three")
	st.bank("Bank returns B's payment", bounced.GetId(), "return")

	measure("PAID", "A: paid", paid, recoded(a, "99213", 180))
	measure("RETURNED", "B: payment returned", bounced, recoded(b, "99213", 180))
	measure("DENIED", "D: denied, reported with nothing paid", denied, recoded(d, "99213", 180))
}

// remit: what the provider is told. An 835 reverses what an earlier one
// reported and pays the net.
func remit(st *story) {
	in := submitted("RIV-88213", jordan, "99213", 180, 12)
	orig := st.process("Office visit arrives", in)
	st.pay("it")
	st.remittance("835 reports and pays it", true, orig)
	adj := rpc(st, "Adjust to a longer visit", st.s.Claims.AdjustClaim, &claimsv1.AdjustClaimRequest{
		OriginalClaimId: orig.GetId(), CorrectedClaim: recoded(in, "99214", 250), Reason: "Corrected visit level"})
	st.pay("the replacement")
	st.remittance("Next 835: reversal and replacement, netted", true, adj.GetClaim())
	v := rpc(st, "Void it", st.s.Claims.VoidClaim, &claimsv1.VoidClaimRequest{ClaimId: adj.GetClaim().GetId(), Reason: "Billed in error"})
	st.remittance("835 for the void alone", false, v.GetVoidClaim())
}

// remittance builds the 835 for claims, saving it when it is one a later
// 835 will reverse.
func (st *story) remittance(label string, save bool, cs ...*claimsv1.Claim) {
	st.other("remittance", "remittance.Build", label, func(ctx context.Context) error {
		st.trace++
		pay := &remittancev1.RemittancePayment{Method: remittancev1.PaymentMethod_PAYMENT_METHOD_CHECK,
			TraceNumber: fmt.Sprintf("1002%02d", st.trace), Date: dates.FromTime(st.b.Now())}
		rem, err := st.b.Build(ctx, cs, pay)
		if errors.Is(err, remittance.ErrNetNegative) {
			st.ex.Remittances = append(st.ex.Remittances, &RemittanceView{Error: "NET_NEGATIVE", Entries: []RemittanceEntry{}})
			return nil
		}
		if err != nil {
			return err
		}
		if save {
			if rem, err = st.b.Save(ctx, rem); err != nil {
				return err
			}
		}
		v := &RemittanceView{Payee: rem.GetPayee().GetName(), Payer: rem.GetPayer().GetName(),
			Trace: rem.GetPayment().GetTraceNumber(), Amount: rem.GetPayment().GetAmount().GetCents()}
		for _, e := range rem.GetClaims() {
			v.Entries = append(v.Entries, RemittanceEntry{Number: e.GetClaimNumber(),
				Status: strings.TrimPrefix(e.GetStatus().String(), "REMITTANCE_CLAIM_STATUS_"),
				Paid:   e.GetPaid().GetCents(), Reversal: e.GetReversesRemittanceId() != ""})
		}
		st.ex.Remittances = append(st.ex.Remittances, v)
		return nil
	})
	// A failure here is the story going wrong, not a refusal to show.
	if st.err == nil {
		if last := st.ex.Calls[len(st.ex.Calls)-1]; last.Rejected != "" {
			st.err = fmt.Errorf("%s: %s", label, last.Rejected)
		}
	}
}
