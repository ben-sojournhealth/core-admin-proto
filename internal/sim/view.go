package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/adjudication"
	"coreadmin/internal/claims"
	"coreadmin/internal/dates"
	"coreadmin/internal/store"
)

// State is everything the UI shows, flattened for rendering. Amounts are
// integer cents.
type State struct {
	Members      []MemberView     `json:"members"`
	Providers    []ProviderView   `json:"providers"`
	Accumulators AccumulatorsView `json:"accumulators"`
	Claims       []*ClaimView     `json:"claims"`
	Log          []*LogEntry      `json:"log"`
}

type MemberView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
	Coverage     string `json:"coverage"`
}

type ProviderView struct {
	NPI     string `json:"npi"`
	Name    string `json:"name"`
	Network string `json:"network"`
}

type AccumulatorsView struct {
	ID       string          `json:"id"`
	Revision int64           `json:"revision"`
	Columns  []string        `json:"columns"`
	Rows     []AccumRow      `json:"rows"`
	Entries  []AccumEntry    `json:"entries"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type AccumRow struct {
	Owner string       `json:"owner"`
	Cells []*AccumCell `json:"cells"` // nil where the plan has no such accumulator
}

type AccumCell struct {
	AccumulatorID string `json:"accumulatorId"`
	Applied       int64  `json:"applied"`
	Limit         int64  `json:"limit"`
}

type AccumEntry struct {
	Claim       string `json:"claim"`
	Accumulator string `json:"accumulator"`
	Owner       string `json:"owner"`
	Amount      int64  `json:"amount"`
}

type ClaimView struct {
	ID       string `json:"id"`
	MemberID string `json:"memberId"`
	// Number is the claim number people see, with its version suffix.
	Number string `json:"number"`
	// Label is how this sandbox names the claim (short name or number).
	Label         string `json:"label"`
	OriginalLabel string `json:"originalLabel,omitempty"`
	Received      string `json:"received"`
	Member        string `json:"member"`
	Provider      string `json:"provider"`
	DOS           string `json:"dos"`
	Procedure     string `json:"procedure"`
	Status        string `json:"status"`
	Frequency     string `json:"frequency"`
	OriginalID    string `json:"originalId,omitempty"`
	Run           int32  `json:"run"`
	Reason        string `json:"reason,omitempty"`
	Tier          string `json:"tier,omitempty"`

	Charge  int64 `json:"charge"`
	Allowed int64 `json:"allowed"`
	Paid    int64 `json:"paid"`
	Member_ int64 `json:"memberOwes"`

	Pends   []PendView    `json:"pends"`
	Lines   []LineView    `json:"lines"`
	Payment *PaymentView  `json:"payment,omitempty"`
	Hold    string        `json:"hold,omitempty"`
	Flags   []FlagView    `json:"flags"`
	History []HistoryView `json:"history"`
	// How the claim arrived, for claims loaded from an 837.
	Intake *IntakeView `json:"intake,omitempty"`
	// For a replacement or void: what it corrects, or that it isn't linked.
	Correction *CorrectionView `json:"correction,omitempty"`
	// Buttons the UI should offer for this claim.
	Actions []string        `json:"actions"`
	Raw     json.RawMessage `json:"raw,omitempty"`
}

// CorrectionView is the badge on a replacement or void.
type CorrectionView struct {
	// "corrected" or "void".
	Kind string `json:"kind"`
	// The original's number and id, when matched.
	Original   string `json:"original,omitempty"`
	OriginalID string `json:"originalId,omitempty"`
	// How it was matched: "linked" or "quoted".
	Match string `json:"match,omitempty"`
	// The claim number the provider quoted, if any.
	Quoted string `json:"quoted,omitempty"`
	// True when the claim's number isn't a version of the original's,
	// because the link was made after the claim was numbered.
	SeparateNumber bool `json:"separateNumber,omitempty"`
}

type IntakeView struct {
	PCN           string `json:"pcn"`
	JobID         string `json:"jobId"`
	Filename      string `json:"filename"`
	Set           string `json:"set"`
	Clearinghouse string `json:"clearinghouse"`
	// When the clearinghouse accepted the claim.
	ClearinghouseReceived string `json:"clearinghouseReceived,omitempty"`
	Trace                 string `json:"trace,omitempty"`
	// The claim number the provider quoted on a corrected claim or void.
	Quoted    string `json:"quoted,omitempty"`
	Patient   string `json:"patient"`
	Born      string `json:"born"`
	Card      string `json:"card"`
	Dependent bool   `json:"dependent"`
}

type PendView struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Line        int32  `json:"line"`
	Overridable bool   `json:"overridable"`
}

type LineView struct {
	Number    int32    `json:"number"`
	Procedure string   `json:"procedure"`
	POS       string   `json:"pos"`
	Status    string   `json:"status"`
	Denial    string   `json:"denial,omitempty"`
	Charge    int64    `json:"charge"`
	Allowed   int64    `json:"allowed"`
	Pricing   string   `json:"pricing,omitempty"`
	Benefit   string   `json:"benefit,omitempty"`
	Shares    []Share  `json:"shares"`
	Paid      int64    `json:"paid"`
	CAS       []string `json:"cas"`
}

type Share struct {
	Type         string   `json:"type"`
	Amount       int64    `json:"amount"`
	Accumulators []string `json:"accumulators"`
}

type PaymentView struct {
	State string `json:"state"`
	Paid  int64  `json:"paid"`
	Date  string `json:"date,omitempty"`
	Remit string `json:"remit,omitempty"`
	Trace string `json:"trace,omitempty"`
}

type FlagView struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Open    bool   `json:"open"`
	Note    string `json:"note,omitempty"`
}

type HistoryView struct {
	Run     int32  `json:"run"`
	Outcome string `json:"outcome"`
}

type DeltaView struct {
	Basis     string   `json:"basis"`
	Original  int64    `json:"original"`
	Projected int64    `json:"projected"`
	Delta     int64    `json:"delta"`
	Flagged   []string `json:"flagged"`
}

func trimEnum(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

func (s *Sim) deltaView(r *claimsv1.AdjustClaimResponse) *DeltaView {
	d := r.GetDelta()
	return &DeltaView{
		Basis:     trimEnum(d.GetBasis().String(), "DELTA_BASIS_"),
		Original:  d.GetOriginalPaid().GetCents(),
		Projected: d.GetProjectedPaid().GetCents(),
		Delta:     d.GetDelta().GetCents(),
		Flagged:   s.labels(r.GetFlaggedClaimIds()),
	}
}

// State builds the full view.
func (s *Sim) State() (*State, error) {
	ctx := context.Background()
	st := &State{Log: s.log, Claims: []*ClaimView{}}
	if st.Log == nil {
		st.Log = []*LogEntry{} // an empty log, not null, for the UI
	}

	members, _, err := s.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		cov := "no coverage"
		for _, sp := range m.GetCoverage() {
			if sp.GetLine() == eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL {
				cov = "covered from " + dates.Format(sp.GetEffective().GetStart())
				if end := sp.GetEffective().GetEnd(); end != nil {
					cov = "covered " + dates.Format(sp.GetEffective().GetStart()) + " to " + dates.Format(end)
				}
			}
		}
		st.Members = append(st.Members, MemberView{
			ID: m.GetId(), Name: m.GetName().GetFirst() + " " + m.GetName().GetLast(),
			Relationship: strings.ToLower(trimEnum(m.GetRelationship().String(), "RELATIONSHIP_")), Coverage: cov,
		})
	}

	providers, _, err := s.Repos.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		network := "out-of-network"
		if len(p.GetParticipations()) > 0 {
			network = "in-network"
		}
		st.Providers = append(st.Providers, ProviderView{NPI: p.GetNpi(), Name: providerName(p), Network: network})
	}

	if st.Accumulators, err = s.accumulatorsView(ctx, SubscriberID, members); err != nil {
		return nil, err
	}

	all, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})
	if err != nil {
		return nil, err
	}
	// Oldest first, by when the claim was received.
	sort.SliceStable(all, func(i, j int) bool {
		return all[i].GetReceivedTime().AsTime().Before(all[j].GetReceivedTime().AsTime())
	})
	for _, c := range all {
		st.Claims = append(st.Claims, s.claimView(c))
	}
	return st, nil
}

// FamilyAccumulators returns the accumulator view for the family a member
// belongs to.
func (s *Sim) FamilyAccumulators(memberID string) (AccumulatorsView, error) {
	ctx := context.Background()
	m, err := s.Repos.Members.Get(ctx, memberID)
	if err != nil {
		return AccumulatorsView{}, err
	}
	family, _, err := s.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 100, Where: []store.Cond{store.Eq(m.GetSubscriberId(), "subscriberId")}})
	if err != nil {
		return AccumulatorsView{}, err
	}
	return s.accumulatorsView(ctx, m.GetSubscriberId(), family)
}

func (s *Sim) accumulatorsView(ctx context.Context, subscriberID string, members []*eligibilityv1.Member) (AccumulatorsView, error) {
	plan, err := s.Repos.Plans.Get(ctx, PlanID)
	if err != nil {
		return AccumulatorsView{}, err
	}
	version := plan.GetVersions()[0]
	id := adjudication.FamilyAccumulatorsID(subscriberID, PlanID, plan.GetBenefitPeriod().GetStart())
	v := AccumulatorsView{ID: id, Entries: []AccumEntry{}}
	rec, err := s.Repos.FamilyAccumulators.Get(ctx, id)
	if err == nil {
		v.Revision = rec.GetAudit().GetRevision()
		v.Raw, _ = protojson.Marshal(rec)
	}

	// One column per (tier, type); each has an individual and/or a family
	// accumulator.
	type column struct {
		label    string
		ind, fam *planv1.Accumulator
	}
	var cols []*column
	byKey := map[string]*column{}
	for _, a := range version.GetAccumulators() {
		tier := strings.Join(a.GetTierIds(), "+")
		typ := "deductible"
		if a.GetType() == planv1.AccumulatorType_ACCUMULATOR_TYPE_OUT_OF_POCKET {
			typ = "out-of-pocket max"
		}
		key := tier + "|" + typ
		col := byKey[key]
		if col == nil {
			tierName := map[string]string{"INN": "In-network", "OON": "Out-of-network"}[tier]
			if tierName == "" {
				tierName = tier
			}
			col = &column{label: tierName + " " + typ}
			byKey[key] = col
			cols = append(cols, col)
		}
		if a.GetLevel() == planv1.AccumulatorLevel_ACCUMULATOR_LEVEL_FAMILY {
			col.fam = a
		} else {
			col.ind = a
		}
	}
	cell := func(a *planv1.Accumulator, owner string) *AccumCell {
		if a == nil {
			return nil
		}
		return &AccumCell{AccumulatorID: a.GetId(), Applied: adjudication.Applied(rec, a.GetId(), owner), Limit: a.GetLimit().GetCents()}
	}
	family := AccumRow{Owner: "Family"}
	for _, col := range cols {
		v.Columns = append(v.Columns, col.label)
		family.Cells = append(family.Cells, cell(col.fam, subscriberID))
	}
	v.Rows = append(v.Rows, family)
	for _, m := range members {
		row := AccumRow{Owner: m.GetName().GetFirst()}
		for _, col := range cols {
			row.Cells = append(row.Cells, cell(col.ind, m.GetId()))
		}
		v.Rows = append(v.Rows, row)
	}
	for _, e := range rec.GetEntries() {
		v.Entries = append(v.Entries, AccumEntry{
			Claim: e.GetClaimId(), Accumulator: e.GetAccumulatorId(),
			Owner: s.memberName(e.GetOwnerId()), Amount: e.GetAmount().GetCents(),
		})
	}
	return v, nil
}

func outcome(c *claimsv1.Claim, adj *claimsv1.Adjudication, status string) string {
	if len(adj.GetPends()) > 0 {
		var codes []string
		for _, p := range adj.GetPends() {
			codes = append(codes, p.GetCode())
		}
		return "PENDED: " + strings.Join(codes, ", ")
	}
	return status
}

// ClaimView flattens a stored claim for display.
func (s *Sim) ClaimView(c *claimsv1.Claim) *ClaimView { return s.claimView(c) }

func (s *Sim) claimView(c *claimsv1.Claim) *ClaimView {
	adj := c.GetAdjudication()
	v := &ClaimView{
		ID:         c.GetId(),
		MemberID:   c.GetMemberId(),
		Number:     claims.DisplayNumber(c),
		Label:      s.Claims.Label(c),
		Received:   c.GetReceivedTime().AsTime().Format(time.RFC3339),
		Member:     s.memberName(c.GetMemberId()),
		Provider:   c.GetBillingProvider().GetName(),
		DOS:        dates.Format(c.GetServiceDates().GetStart()),
		Status:     trimEnum(c.GetStatus().String(), "CLAIM_STATUS_"),
		Frequency:  trimEnum(c.GetFrequency().String(), "CLAIM_FREQUENCY_"),
		OriginalID: c.GetOriginalClaimId(),
		Run:        adj.GetRunNumber(),
		Reason:     adj.GetReason(),
		Tier:       adj.GetNetworkTierId(),
		Charge:     c.GetTotalCharge().GetCents(),
		Allowed:    adj.GetTotalAllowed().GetCents(),
		Paid:       adj.GetTotalPaid().GetCents(),
		Member_:    adj.GetTotalMemberResponsibility().GetCents(),
		Pends:      []PendView{},
		Lines:      []LineView{},
		Flags:      []FlagView{},
		History:    []HistoryView{},
		Actions:    []string{},
		Hold:       c.GetPaymentHold().GetReason(),
	}
	if len(c.GetLines()) > 0 {
		v.Procedure = c.GetLines()[0].GetProcedureCode()
	}
	if c.GetOriginalClaimId() != "" {
		v.OriginalLabel = s.label(c.GetOriginalClaimId())
	}
	if f := c.GetFrequency(); f == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT || f == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
		cv := &CorrectionView{Kind: "corrected", Quoted: c.GetPayerClaimControlNumber()}
		if f == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
			cv.Kind = "void"
		}
		if id := c.GetOriginalClaimId(); id != "" {
			cv.OriginalID, cv.Original = id, s.label(id)
			if o, err := s.Repos.Claims.Get(context.Background(), id); err == nil {
				cv.SeparateNumber = o.GetClaimNumber() != c.GetClaimNumber()
			}
			cv.Match = map[claimsv1.OriginalMatch]string{
				claimsv1.OriginalMatch_ORIGINAL_MATCH_LINKED:        "linked",
				claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER: "quoted",
			}[adj.GetOriginalMatch()]
		}
		v.Correction = cv
	}
	if src := c.GetSourceRecord(); src != nil {
		pt := c.GetSubmittedPatient()
		in := &IntakeView{
			PCN: c.GetPatientControlNumber(), JobID: src.GetIngestionJobId(), Set: src.GetTransactionSetControlNumber(),
			Clearinghouse: c.GetClearinghouseId(), Trace: c.GetClearinghouseTraceNumber(), Quoted: c.GetPayerClaimControlNumber(),
			Patient: strings.TrimSpace(pt.GetName().GetFirst() + " " + pt.GetName().GetLast()),
			Born:    dates.Format(pt.GetBirthDate()), Card: pt.GetCardId(), Dependent: pt.GetDependentLoop(),
		}
		if t := c.GetClearinghouseReceivedTime(); t != nil {
			in.ClearinghouseReceived = t.AsTime().Format(time.RFC3339)
		}
		if j, err := s.Repos.IngestionJobs.Get(context.Background(), src.GetIngestionJobId()); err == nil {
			in.Filename = j.GetOriginalFilename()
		}
		v.Intake = in
		in.Patient = titleCase(in.Patient)
		if v.Member == "" {
			v.Member = in.Patient + " (as sent)"
		}
		// An 837 names the provider in capitals; once matched, use the
		// provider directory's name.
		v.Provider = titleCase(v.Provider)
		if id := c.GetBillingProvider().GetProviderId(); id != "" {
			if p, err := s.Repos.Providers.Get(context.Background(), id); err == nil {
				v.Provider = providerName(p)
			}
		}
	}
	v.Raw, _ = protojson.Marshal(c)

	for _, p := range adj.GetPends() {
		v.Pends = append(v.Pends, PendView{Code: p.GetCode(), Message: p.GetMessage(), Line: p.GetLineNumber(), Overridable: adjudication.Overridable(p.GetCode())})
	}
	for _, h := range c.GetAdjudicationHistory() {
		v.History = append(v.History, HistoryView{Run: h.GetRunNumber(), Outcome: outcome(c, h, "decided")})
	}
	for _, l := range c.GetLines() {
		la := l.GetAdjudication()
		lv := LineView{
			Number: l.GetLineNumber(), Procedure: l.GetProcedureCode(), POS: l.GetPlaceOfServiceCode(),
			Status: trimEnum(la.GetStatus().String(), "LINE_STATUS_"),
			Charge: l.GetCharge().GetCents(), Allowed: la.GetPricing().GetAllowed().GetCents(), Paid: la.GetPaid().GetCents(),
			Shares: []Share{}, CAS: []string{},
		}
		if d := la.GetDenial(); d != nil {
			lv.Denial = d.GetCode() + ": " + d.GetMessage()
		}
		if p := la.GetPricing(); p != nil {
			switch p.GetSource() {
			case claimsv1.PricingSource_PRICING_SOURCE_CONTRACT:
				lv.Pricing = fmt.Sprintf("contract %s, term %d (%s)", p.GetContractId(), p.GetContractTermIndex()+1,
					strings.ToLower(strings.ReplaceAll(trimEnum(p.GetContractMethod().String(), "CONTRACT_METHOD_"), "_", " ")))
				if p.GetFeeScheduleId() != "" {
					lv.Pricing += ", " + p.GetFeeScheduleId()
				}
			case claimsv1.PricingSource_PRICING_SOURCE_NON_CONTRACTED:
				b := p.GetNonContractedBasis()
				lv.Pricing = fmt.Sprintf("no contract: %d%% of %s", b.GetPercentBps()/100,
					strings.ToLower(strings.TrimPrefix(trimEnum(b.GetMethod().String(), "REIMBURSEMENT_METHOD_"), "PERCENT_OF_")))
			}
		}
		if b := la.GetBenefit(); b != nil {
			lv.Benefit = b.GetServiceCategoryId() + " / " + b.GetNetworkTierId()
		}
		for _, cs := range la.GetCostShares() {
			lv.Shares = append(lv.Shares, Share{
				Type:         strings.ToLower(trimEnum(cs.GetType().String(), "COST_SHARE_TYPE_")),
				Amount:       cs.GetAmount().GetCents(),
				Accumulators: slices.Clone(cs.GetAccumulatorIds()),
			})
		}
		groups := map[claimsv1.CasGroup]string{
			claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL:            "CO",
			claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY: "PR",
			claimsv1.CasGroup_CAS_GROUP_OTHER:                  "OA",
			claimsv1.CasGroup_CAS_GROUP_PAYER_INITIATED:        "PI",
		}
		for _, a := range la.GetCasAdjustments() {
			lv.CAS = append(lv.CAS, fmt.Sprintf("%s-%s %s", groups[a.GetGroup()], a.GetReasonCode(), claims.USD(a.GetAmount().GetCents())))
		}
		v.Lines = append(v.Lines, lv)
	}

	if p := c.GetPayment(); p != nil {
		v.Payment = &PaymentView{
			State: trimEnum(p.GetState().String(), "PAYMENT_STATE_"), Paid: p.GetPaid().GetCents(),
			Date: dates.Format(p.GetPaidDate()), Remit: p.GetRemittanceId(), Trace: p.GetPaymentTraceNumber(),
		}
	}
	openFlag := false
	for _, f := range c.GetReviewFlags() {
		open := f.GetClearedTime() == nil
		openFlag = openFlag || open
		v.Flags = append(v.Flags, FlagView{Code: f.GetCode(), Message: f.GetMessage(), Source: f.GetSourceClaimId(), Open: open, Note: f.GetClearNote()})
	}

	final := c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED || c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_DENIED
	state := c.GetPayment().GetState()
	switch {
	case c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_PENDED:
		v.Actions = append(v.Actions, "reprocess")
	case final && c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		// A void claim only carries the reversal; it is not adjusted.
	case final:
		v.Actions = append(v.Actions, "adjust", "void")
		if c.GetPayment() == nil && c.GetPaymentHold() == nil {
			v.Actions = append(v.Actions, "select")
		}
		if c.GetPaymentHold() != nil {
			v.Actions = append(v.Actions, "releaseHold")
		}
		if openFlag {
			v.Actions = append(v.Actions, "clearFlag")
		}
	}
	switch state {
	case claimsv1.PaymentState_PAYMENT_STATE_SELECTED:
		v.Actions = append(v.Actions, "pull")
	case claimsv1.PaymentState_PAYMENT_STATE_ISSUED:
		v.Actions = append(v.Actions, "clear", "return")
	case claimsv1.PaymentState_PAYMENT_STATE_RETURNED:
		if final {
			v.Actions = append(v.Actions, "reissue")
		}
	}
	return v
}

// titleCase turns "TAYLOR NGUYEN" into "Taylor Nguyen".
func titleCase(name string) string {
	words := strings.Fields(strings.ToLower(name))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
