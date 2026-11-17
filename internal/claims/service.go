// Package claims implements the ClaimsService operations on top of the
// adjudication engine and accumulator ledger.
//
// Methods take and return the generated request/response messages, so a
// Connect handler is a thin wrapper around Service.
package claims

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/adjudication"
	"coreadmin/internal/dates"
	"coreadmin/internal/repo"
	"coreadmin/internal/store"
)

// ErrRejected marks a request the service refuses because of the claim's
// state (a failed precondition), as opposed to a system failure.
var ErrRejected = errors.New("rejected")

func rejectf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

const (
	FlagAccumulatorChange = "ACCUMULATOR_CHANGE"
	maxAttempts           = 5
)

type Service struct {
	Repos  *repo.Repos
	Engine *adjudication.DefaultEngine
	Ledger adjudication.AccumulatorLedger
	Now    func() time.Time
	// Logf receives a plain-language line for each thing that happens.
	Logf func(format string, args ...any)

	remittances int
}

func (s *Service) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// USD formats cents as dollars, e.g. -$1,234.50.
func USD(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	dollars := fmt.Sprint(cents / 100)
	for i := len(dollars) - 3; i > 0; i -= 3 {
		dollars = dollars[:i] + "," + dollars[i:]
	}
	return fmt.Sprintf("%s$%s.%02d", sign, dollars, cents%100)
}

func finalized(st claimsv1.ClaimStatus) bool {
	return st == claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED || st == claimsv1.ClaimStatus_CLAIM_STATUS_DENIED
}

func statusName(st claimsv1.ClaimStatus) string {
	return st.String()[len("CLAIM_STATUS_"):]
}

func (s *Service) ProcessClaim(ctx context.Context, req *claimsv1.ProcessClaimRequest) (*claimsv1.ProcessClaimResponse, error) {
	c := proto.Clone(req.GetClaim()).(*claimsv1.Claim)
	if c.GetId() == "" {
		return nil, rejectf("claim id is required")
	}
	// A provider's corrected claim or void is an adjustment of the claim
	// it references.
	switch c.GetFrequency() {
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT:
		r, err := s.AdjustClaim(ctx, &claimsv1.AdjustClaimRequest{
			OriginalClaimId: c.GetOriginalClaimId(), CorrectedClaim: c,
			Reason: "provider submitted a corrected claim", DryRun: req.GetDryRun(),
		})
		if err != nil {
			return nil, err
		}
		return &claimsv1.ProcessClaimResponse{Claim: r.GetClaim()}, nil
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		r, err := s.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: c.GetOriginalClaimId(), Reason: "provider submitted a void"})
		if err != nil {
			return nil, err
		}
		return &claimsv1.ProcessClaimResponse{Claim: r.GetVoidClaim()}, nil
	}

	s.prepareNew(c)
	if req.GetDryRun() {
		calc, err := s.Engine.Calculate(ctx, c, "")
		if err != nil {
			return nil, err
		}
		s.logf("Dry run of %s: would be %s. Nothing saved.", c.GetId(), describe(calc.Claim))
		return &claimsv1.ProcessClaimResponse{Claim: calc.Claim}, nil
	}
	saved, err := s.Repos.Claims.Create(ctx, c)
	if errors.Is(err, store.ErrAlreadyExists) {
		return nil, rejectf("claim %s already exists", c.GetId())
	} else if err != nil {
		return nil, err
	}
	out, err := s.adjudicate(ctx, saved, "")
	if err != nil {
		return nil, err
	}
	return &claimsv1.ProcessClaimResponse{Claim: out}, nil
}

// prepareNew resets everything on an incoming claim that only the system
// may set.
func (s *Service) prepareNew(c *claimsv1.Claim) {
	if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_UNSPECIFIED {
		c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL
	}
	c.Status = claimsv1.ClaimStatus_CLAIM_STATUS_RECEIVED
	c.Adjudication, c.AdjudicationHistory = nil, nil
	c.Payment, c.PaymentHold, c.ReviewFlags, c.PendOverrides, c.Audit = nil, nil, nil, nil, nil
	for _, l := range c.GetLines() {
		l.Adjudication = nil
	}
	if c.GetReceivedTime() == nil {
		c.ReceivedTime = timestamppb.New(s.Now())
	}
	if c.GetTimelyFilingDate() == nil {
		t := c.GetReceivedTime().AsTime()
		if c.GetClearinghouseReceivedTime() != nil {
			t = c.GetClearinghouseReceivedTime().AsTime()
		}
		c.TimelyFilingDate = dates.FromTime(t)
	}
}

// adjudicate calculates and commits a stored claim. If another claim for
// the same family posted to the accumulators in between, the commit is
// rejected and the claim is recalculated against the new balances.
func (s *Service) adjudicate(ctx context.Context, c *claimsv1.Claim, reason string) (*claimsv1.Claim, error) {
	for attempt := 1; ; attempt++ {
		calc, err := s.Engine.Calculate(ctx, c, reason)
		if err != nil {
			return nil, err
		}
		out, err := s.commit(ctx, calc)
		if errors.Is(err, adjudication.ErrBalancesChanged) && attempt < maxAttempts {
			s.logf("%s: the family's accumulators changed after cost sharing was calculated (revision %d is stale). Recalculating.",
				c.GetId(), calc.Revision)
			continue
		}
		return out, err
	}
}

// commit makes a calculation real: posts accumulators if the claim is
// finalized, saves the claim, and for a replacement claim retires the
// original.
func (s *Service) commit(ctx context.Context, calc *adjudication.Calculation) (*claimsv1.Claim, error) {
	c := calc.Claim
	adj := c.GetAdjudication()
	final := finalized(c.GetStatus())

	var orig *claimsv1.Claim
	if calc.ReverseClaimID != "" {
		var err error
		if orig, err = s.Repos.Claims.Get(ctx, calc.ReverseClaimID); err != nil {
			return nil, err
		}
	}

	if final {
		reverseID := ""
		if orig != nil {
			switch origFam := orig.GetAdjudication().GetFamilyAccumulatorsId(); {
			case origFam == "":
			case calc.Family != nil && origFam == calc.Family.GetId():
				// Same record: reverse and post in one write.
				reverseID = orig.GetId()
			default:
				if err := s.Ledger.Reverse(ctx, origFam, orig.GetId()); err != nil {
					return nil, err
				}
			}
		}
		if calc.Family != nil {
			err := s.Ledger.Post(ctx, adjudication.PostRequest{
				FamilyAccumulatorsID: calc.Family.GetId(),
				Revision:             calc.Revision,
				New:                  calc.Family,
				Limits:               calc.Limits,
				ClaimID:              c.GetId(),
				ServiceDate:          c.GetServiceDates().GetStart(),
				Postings:             adj.GetAccumulatorPostings(),
				ReverseClaimID:       reverseID,
			})
			if err != nil {
				return nil, err
			}
		}
	}

	saved, err := s.Repos.Claims.Update(ctx, c)
	if err != nil {
		return nil, err
	}
	s.logf("%s adjudicated (run %d): %s.", saved.GetId(), adj.GetRunNumber(), describe(saved))
	if final && len(adj.GetAccumulatorPostings()) > 0 {
		s.logf("%s finalized: posted %d accumulator amounts to %s.", saved.GetId(), len(adj.GetAccumulatorPostings()), calc.Family.GetId())
	}

	if orig == nil {
		return saved, nil
	}
	if final {
		orig.Status = claimsv1.ClaimStatus_CLAIM_STATUS_ADJUSTED
		s.retire(orig, "claim adjusted")
		if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
			return nil, err
		}
		s.logf("%s is now ADJUSTED; its accumulator postings were reversed and replaced by %s.", orig.GetId(), saved.GetId())
		if adjudication.PostingsDiffer(orig.GetAdjudication().GetAccumulatorPostings(), adj.GetAccumulatorPostings()) {
			if _, err := s.flagLaterClaims(ctx, orig, saved.GetId(), true); err != nil {
				return nil, err
			}
		}
	} else if orig.GetPayment() == nil && orig.GetPaymentHold() == nil {
		orig.PaymentHold = &claimsv1.PaymentHold{
			Reason:     fmt.Sprintf("Replacement %s is pended.", saved.GetId()),
			PlacedBy:   store.Actor(ctx),
			PlacedTime: timestamppb.New(s.Now()),
		}
		if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
			return nil, err
		}
		s.logf("%s put on payment hold until its replacement %s is resolved.", orig.GetId(), saved.GetId())
	}
	return saved, nil
}

// retire clears what no longer applies once a claim is adjusted or voided.
func (s *Service) retire(c *claimsv1.Claim, note string) {
	c.PaymentHold = nil
	for _, f := range c.GetReviewFlags() {
		if f.GetClearedTime() == nil {
			f.ClearedTime, f.ClearedBy, f.ClearNote = timestamppb.New(s.Now()), "system", note
		}
	}
}

// laterClaims returns finalized claims for the same family and benefit
// period that were adjudicated after orig.
func (s *Service) laterClaims(ctx context.Context, orig *claimsv1.Claim, excludeID string) ([]*claimsv1.Claim, error) {
	fam := orig.GetAdjudication().GetFamilyAccumulatorsId()
	if fam == "" {
		return nil, nil
	}
	at := orig.GetAdjudication().GetAdjudicatedTime().AsTime()
	out, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{
		PageSize: 10_000,
		Filter: func(o *claimsv1.Claim) bool {
			return o.GetId() != orig.GetId() && o.GetId() != excludeID &&
				finalized(o.GetStatus()) &&
				o.GetAdjudication().GetFamilyAccumulatorsId() == fam &&
				o.GetAdjudication().GetAdjudicatedTime().AsTime().After(at)
		},
	})
	return out, err
}

// flagLaterClaims raises ACCUMULATOR_CHANGE on every later claim for the
// family. With write false it only reports which claims would be flagged.
func (s *Service) flagLaterClaims(ctx context.Context, orig *claimsv1.Claim, excludeID string, write bool) ([]string, error) {
	later, err := s.laterClaims(ctx, orig, excludeID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, o := range later {
		ids = append(ids, o.GetId())
		if !write {
			continue
		}
		o.ReviewFlags = append(o.ReviewFlags, &claimsv1.ReviewFlag{
			Code:          FlagAccumulatorChange,
			Message:       fmt.Sprintf("accumulator postings of earlier claim %s changed; this claim's cost sharing may no longer be right", orig.GetId()),
			SourceClaimId: orig.GetId(),
			RaisedTime:    timestamppb.New(s.Now()),
		})
		if _, err := s.Repos.Claims.Update(ctx, o); err != nil {
			return nil, err
		}
		s.logf("%s flagged for review: an earlier claim's accumulator postings changed.", o.GetId())
	}
	return ids, nil
}

func (s *Service) ReprocessClaim(ctx context.Context, req *claimsv1.ReprocessClaimRequest) (*claimsv1.ReprocessClaimResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_PENDED {
		return nil, rejectf("only PENDED claims can be reprocessed; %s is %s. Use AdjustClaim or VoidClaim for a finalized claim",
			c.GetId(), statusName(c.GetStatus()))
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	now := timestamppb.New(s.Now())
	for _, r := range req.GetResolutions() {
		c.PendOverrides = append(c.PendOverrides, &claimsv1.PendOverride{
			Code: r.GetCode(), LineNumber: r.GetLineNumber(), Note: r.GetNote(),
			ResolvedBy: store.Actor(ctx), ResolvedTime: now,
		})
		for _, p := range c.GetAdjudication().GetPends() {
			if p.GetCode() == r.GetCode() && p.GetLineNumber() == r.GetLineNumber() {
				p.ResolvedTime, p.ResolvedBy, p.ResolutionNote = now, store.Actor(ctx), r.GetNote()
			}
		}
		if !adjudication.Overridable(r.GetCode()) {
			s.logf("Note: %s is caused by data, not judgment. Resolving it has no effect unless the data has changed.", r.GetCode())
		}
	}
	// The previous run becomes history.
	c.AdjudicationHistory = append(c.AdjudicationHistory, c.GetAdjudication())
	c.Adjudication = nil

	if req.GetDryRun() {
		calc, err := s.Engine.Calculate(ctx, c, req.GetReason())
		if err != nil {
			return nil, err
		}
		s.logf("Dry run of reprocessing %s: would be %s. Nothing saved.", c.GetId(), describe(calc.Claim))
		return &claimsv1.ReprocessClaimResponse{Claim: calc.Claim}, nil
	}
	out, err := s.adjudicate(ctx, c, req.GetReason())
	if err != nil {
		return nil, err
	}
	return &claimsv1.ReprocessClaimResponse{Claim: out}, nil
}

var replacementSuffix = regexp.MustCompile(`-R\d+$`)

func (s *Service) AdjustClaim(ctx context.Context, req *claimsv1.AdjustClaimRequest) (*claimsv1.AdjustClaimResponse, error) {
	orig, err := s.get(ctx, req.GetOriginalClaimId())
	if err != nil {
		return nil, err
	}
	if !finalized(orig.GetStatus()) {
		return nil, rejectf("only finalized claims (APPROVED or DENIED) can be adjusted; %s is %s", orig.GetId(), statusName(orig.GetStatus()))
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	if orig.GetPayment().GetState() == claimsv1.PaymentState_PAYMENT_STATE_SELECTED && !req.GetDryRun() {
		return nil, rejectf("%s is selected for a payment run that has not been released; pull it from the run first", orig.GetId())
	}
	pending, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1, Filter: func(o *claimsv1.Claim) bool {
		return o.GetOriginalClaimId() == orig.GetId() && !finalized(o.GetStatus()) &&
			o.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	}})
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 && !req.GetDryRun() {
		return nil, rejectf("%s already has a replacement in progress (%s)", orig.GetId(), pending[0].GetId())
	}

	// Build the replacement: the corrected claim, or the original's content
	// unchanged to be re-adjudicated against current data.
	src := req.GetCorrectedClaim()
	if src == nil {
		src = orig
	}
	repl := proto.Clone(src).(*claimsv1.Claim)
	if repl.GetId() == "" || repl.GetId() == orig.GetId() {
		base := replacementSuffix.ReplaceAllString(orig.GetId(), "")
		for n := 1; ; n++ {
			repl.Id = fmt.Sprintf("%s-R%d", base, n)
			if _, err := s.Repos.Claims.Get(ctx, repl.GetId()); errors.Is(err, store.ErrNotFound) {
				break
			} else if err != nil {
				return nil, err
			}
		}
	}
	repl.ReceivedTime = nil
	s.prepareNew(repl)
	repl.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	repl.OriginalClaimId = orig.GetId()
	repl.CustomerId = orig.GetCustomerId()

	resp := &claimsv1.AdjustClaimResponse{}
	if req.GetDryRun() {
		calc, err := s.Engine.Calculate(ctx, repl, req.GetReason())
		if err != nil {
			return nil, err
		}
		resp.Claim = calc.Claim
		if finalized(calc.Claim.GetStatus()) &&
			adjudication.PostingsDiffer(orig.GetAdjudication().GetAccumulatorPostings(), calc.Claim.GetAdjudication().GetAccumulatorPostings()) {
			if resp.FlaggedClaimIds, err = s.flagLaterClaims(ctx, orig, repl.GetId(), false); err != nil {
				return nil, err
			}
		}
	} else {
		saved, err := s.Repos.Claims.Create(ctx, repl)
		if err != nil {
			return nil, err
		}
		s.logf("Created replacement %s for %s (%s).", saved.GetId(), orig.GetId(), req.GetReason())
		if resp.Claim, err = s.adjudicate(ctx, saved, req.GetReason()); err != nil {
			return nil, err
		}
		flagged, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000, Filter: func(o *claimsv1.Claim) bool {
			for _, f := range o.GetReviewFlags() {
				if f.GetSourceClaimId() == orig.GetId() && f.GetClearedTime() == nil {
					return true
				}
			}
			return false
		}})
		if err != nil {
			return nil, err
		}
		for _, f := range flagged {
			resp.FlaggedClaimIds = append(resp.FlaggedClaimIds, f.GetId())
		}
	}
	resp.Delta = delta(orig, resp.GetClaim())
	d := resp.GetDelta()
	verb := "Adjustment"
	if req.GetDryRun() {
		verb = "Dry run of adjusting"
	}
	s.logf("%s %s: payee holds %s (%s), replacement pays %s, delta %s.%s",
		verb, orig.GetId(), USD(d.GetOriginalPaid().GetCents()), basisText(d.GetBasis()),
		USD(d.GetProjectedPaid().GetCents()), USD(d.GetDelta().GetCents()),
		map[bool]string{true: " Nothing saved.", false: ""}[req.GetDryRun()])
	return resp, nil
}

func basisText(b claimsv1.DeltaBasis) string {
	switch b {
	case claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT:
		return "payment issued"
	case claimsv1.DeltaBasis_DELTA_BASIS_APPROVED_UNPAID:
		return "approved, not yet paid"
	case claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT_NOT_HELD:
		return "payment returned or voided"
	case claimsv1.DeltaBasis_DELTA_BASIS_DENIED:
		return "denied"
	}
	return "unknown"
}

// delta compares a replacement with what the payee holds for the
// original.
func delta(orig, repl *claimsv1.Claim) *claimsv1.AdjustmentDelta {
	d := &claimsv1.AdjustmentDelta{}
	var held int64
	switch orig.GetPayment().GetState() {
	case claimsv1.PaymentState_PAYMENT_STATE_ISSUED, claimsv1.PaymentState_PAYMENT_STATE_CLEARED:
		d.Basis, held = claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT, orig.GetPayment().GetPaid().GetCents()
	case claimsv1.PaymentState_PAYMENT_STATE_RETURNED, claimsv1.PaymentState_PAYMENT_STATE_VOIDED:
		d.Basis = claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT_NOT_HELD
	default:
		if orig.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_DENIED {
			d.Basis = claimsv1.DeltaBasis_DELTA_BASIS_DENIED
		} else {
			d.Basis, held = claimsv1.DeltaBasis_DELTA_BASIS_APPROVED_UNPAID, orig.GetAdjudication().GetTotalPaid().GetCents()
		}
	}
	projected := repl.GetAdjudication().GetTotalPaid().GetCents()
	d.OriginalPaid, d.ProjectedPaid, d.Delta = cents(held), cents(projected), cents(projected-held)

	type pair struct{ orig, proj int64 }
	lines := map[int32]*pair{}
	var order []int32
	at := func(n int32) *pair {
		if lines[n] == nil {
			lines[n] = &pair{}
			order = append(order, n)
		}
		return lines[n]
	}
	for _, l := range orig.GetLines() {
		at(l.GetLineNumber()).orig = l.GetAdjudication().GetPaid().GetCents()
	}
	for _, l := range repl.GetLines() {
		at(l.GetLineNumber()).proj = l.GetAdjudication().GetPaid().GetCents()
	}
	for _, n := range order {
		p := lines[n]
		d.Lines = append(d.Lines, &claimsv1.LineDelta{
			LineNumber: n, OriginalPaid: cents(p.orig), ProjectedPaid: cents(p.proj), Delta: cents(p.proj - p.orig),
		})
	}
	return d
}

func (s *Service) VoidClaim(ctx context.Context, req *claimsv1.VoidClaimRequest) (*claimsv1.VoidClaimResponse, error) {
	orig, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	if !finalized(orig.GetStatus()) {
		return nil, rejectf("only finalized claims (APPROVED or DENIED) can be voided; %s is %s", orig.GetId(), statusName(orig.GetStatus()))
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	if orig.GetPayment().GetState() == claimsv1.PaymentState_PAYMENT_STATE_SELECTED {
		return nil, rejectf("%s is selected for a payment run that has not been released; pull it from the run first", orig.GetId())
	}

	void := proto.Clone(orig).(*claimsv1.Claim)
	void.Id = replacementSuffix.ReplaceAllString(orig.GetId(), "") + "-V"
	void.ReceivedTime = nil
	s.prepareNew(void)
	void.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
	void.OriginalClaimId = orig.GetId()
	// A void is finalized on creation with nothing payable, so the next
	// remittance reports the reversal.
	void.Status = claimsv1.ClaimStatus_CLAIM_STATUS_APPROVED
	void.Adjudication = &claimsv1.Adjudication{
		AdjudicatedTime: timestamppb.New(s.Now()), RunNumber: 1, Reason: req.GetReason(),
		TotalAllowed: cents(0), TotalPaid: cents(0), TotalMemberResponsibility: cents(0),
		EngineVersion: s.Engine.Version,
	}
	saved, err := s.Repos.Claims.Create(ctx, void)
	if errors.Is(err, store.ErrAlreadyExists) {
		return nil, rejectf("%s has already been voided", orig.GetId())
	} else if err != nil {
		return nil, err
	}

	if fam := orig.GetAdjudication().GetFamilyAccumulatorsId(); fam != "" {
		if err := s.Ledger.Reverse(ctx, fam, orig.GetId()); err != nil {
			return nil, err
		}
	}
	orig.Status = claimsv1.ClaimStatus_CLAIM_STATUS_VOIDED
	s.retire(orig, "claim voided")
	if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
		return nil, err
	}
	s.logf("%s is now VOIDED (%s); its accumulator postings were reversed. Void claim %s created.", orig.GetId(), req.GetReason(), saved.GetId())
	if held := heldAmount(orig); held > 0 {
		s.logf("%s was paid on %s and is to be recovered on the next remittance.", USD(held), orig.GetId())
	}

	resp := &claimsv1.VoidClaimResponse{VoidClaim: saved}
	if len(orig.GetAdjudication().GetAccumulatorPostings()) > 0 {
		if resp.FlaggedClaimIds, err = s.flagLaterClaims(ctx, orig, saved.GetId(), true); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func (s *Service) PullClaimFromPayment(ctx context.Context, req *claimsv1.PullClaimFromPaymentRequest) (*claimsv1.PullClaimFromPaymentResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	if c.GetPayment().GetState() != claimsv1.PaymentState_PAYMENT_STATE_SELECTED {
		return nil, rejectf("%s is not in an unreleased payment run", c.GetId())
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	c.Payment = nil
	c.PaymentHold = &claimsv1.PaymentHold{Reason: req.GetReason(), PlacedBy: store.Actor(ctx), PlacedTime: timestamppb.New(s.Now())}
	saved, err := s.Repos.Claims.Update(ctx, c)
	if err != nil {
		return nil, err
	}
	s.logf("%s pulled from the payment run and put on payment hold (%s).", c.GetId(), req.GetReason())
	return &claimsv1.PullClaimFromPaymentResponse{Claim: saved}, nil
}

func (s *Service) ReleasePaymentHold(ctx context.Context, req *claimsv1.ReleasePaymentHoldRequest) (*claimsv1.ReleasePaymentHoldResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	if c.GetPaymentHold() == nil {
		return nil, rejectf("%s has no payment hold", c.GetId())
	}
	c.PaymentHold = nil
	saved, err := s.Repos.Claims.Update(ctx, c)
	if err != nil {
		return nil, err
	}
	s.logf("Payment hold on %s released.", c.GetId())
	return &claimsv1.ReleasePaymentHoldResponse{Claim: saved}, nil
}

func (s *Service) ClearReviewFlag(ctx context.Context, req *claimsv1.ClearReviewFlagRequest) (*claimsv1.ClearReviewFlagResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	n := 0
	for _, f := range c.GetReviewFlags() {
		if f.GetClearedTime() != nil ||
			(req.GetCode() != "" && f.GetCode() != req.GetCode()) ||
			(req.GetSourceClaimId() != "" && f.GetSourceClaimId() != req.GetSourceClaimId()) {
			continue
		}
		f.ClearedTime, f.ClearedBy, f.ClearNote = timestamppb.New(s.Now()), store.Actor(ctx), req.GetNote()
		n++
	}
	if n == 0 {
		return nil, rejectf("%s has no matching open review flag", c.GetId())
	}
	saved, err := s.Repos.Claims.Update(ctx, c)
	if err != nil {
		return nil, err
	}
	s.logf("Cleared %d review flag(s) on %s without adjusting it (%s).", n, c.GetId(), req.GetNote())
	return &claimsv1.ClearReviewFlagResponse{Claim: saved}, nil
}

func (s *Service) get(ctx context.Context, id string) (*claimsv1.Claim, error) {
	c, err := s.Repos.Claims.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, rejectf("no claim %s", id)
	}
	return c, err
}

// heldAmount is what the payee currently holds for a claim.
func heldAmount(c *claimsv1.Claim) int64 {
	switch c.GetPayment().GetState() {
	case claimsv1.PaymentState_PAYMENT_STATE_ISSUED, claimsv1.PaymentState_PAYMENT_STATE_CLEARED:
		return c.GetPayment().GetPaid().GetCents()
	}
	return 0
}

// describe summarizes a claim's decision in one phrase.
func describe(c *claimsv1.Claim) string {
	adj := c.GetAdjudication()
	switch c.GetStatus() {
	case claimsv1.ClaimStatus_CLAIM_STATUS_PENDED:
		codes := ""
		for i, p := range adj.GetPends() {
			if i > 0 {
				codes += ", "
			}
			codes += p.GetCode()
		}
		return "PENDED (" + codes + ")"
	case claimsv1.ClaimStatus_CLAIM_STATUS_DENIED:
		reason := ""
		for _, l := range c.GetLines() {
			if d := l.GetAdjudication().GetDenial(); d != nil {
				reason = " (" + d.GetCode() + ")"
				break
			}
		}
		return "DENIED" + reason
	}
	return fmt.Sprintf("%s, allowed %s, plan pays %s, member owes %s", statusName(c.GetStatus()),
		USD(adj.GetTotalAllowed().GetCents()), USD(adj.GetTotalPaid().GetCents()), USD(adj.GetTotalMemberResponsibility().GetCents()))
}

func cents(c int64) *commonv1.Money { return &commonv1.Money{Cents: c} }

func cloneClaim(c *claimsv1.Claim) *claimsv1.Claim { return proto.Clone(c).(*claimsv1.Claim) }
