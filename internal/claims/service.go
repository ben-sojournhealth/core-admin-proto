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
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/ids"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// ErrRejected marks a request the service refuses because of the claim's
// state (a failed precondition), as opposed to a system failure.
var ErrRejected = errors.New("rejected")

func rejectf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

const (
	maxAttempts = 5
)

type Service struct {
	Repos  *repo.Repos
	Engine *adjudication.DefaultEngine
	Ledger adjudication.AccumulatorLedger
	Now    func() time.Time
	// Logf receives a plain-language line for each thing that happens.
	Logf func(format string, args ...any)
	// Ref names a claim in log lines. Defaults to the claim number.
	Ref func(*claimsv1.Claim) string

	remittances int
}

type logKey struct{}

// logf records a line for the activity log. Inside a transaction the line
// waits for the commit, so the log never reports something that rolled
// back.
func (s *Service) logf(ctx context.Context, format string, args ...any) {
	if s.Logf == nil {
		return
	}
	if buf, ok := ctx.Value(logKey{}).(*[]string); ok {
		*buf = append(*buf, fmt.Sprintf(format, args...))
		return
	}
	s.Logf(format, args...)
}

// inTx runs f as one transaction: every claim, accumulator and other
// record it writes commits together, or none of it does. Called inside a
// transaction, it joins it.
func (s *Service) inTx(ctx context.Context, f func(ctx context.Context) error) error {
	if _, ok := ctx.Value(logKey{}).(*[]string); ok {
		return f(ctx)
	}
	var lines []string
	if err := s.Repos.InTx(context.WithValue(ctx, logKey{}, &lines), f); err != nil {
		return err
	}
	for _, l := range lines {
		s.logf(ctx, "%s", l)
	}
	return nil
}

// stale reports whether a transaction failed only because something it
// read changed before it committed, so running it again from the start,
// re-reading, can succeed. A stale accumulator revision is caught either
// while writing (ErrBalancesChanged) or at commit (ErrConflict, or
// ErrAlreadyExists when two claims open a family's first record).
func stale(err error) bool {
	return errors.Is(err, adjudication.ErrBalancesChanged) || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrAlreadyExists)
}

// logStale says why an attempt is being run again.
func (s *Service) logStale(ctx context.Context, c *claimsv1.Claim, calc *adjudication.Calculation, err error) {
	if errors.Is(err, adjudication.ErrBalancesChanged) && calc != nil {
		s.logf(ctx, "%s: the family's accumulators changed after cost sharing was calculated (revision %d is stale). Recalculating.",
			s.ref(c), calc.Revision)
		return
	}
	s.logf(ctx, "%s: a record it read changed before it could be saved (%v). Recalculating.", s.ref(c), err)
}

// exists reports whether a claim with this id is committed.
func (s *Service) exists(ctx context.Context, id string) bool {
	_, err := s.Repos.Claims.Get(ctx, id)
	return err == nil
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

func adjudicated(st claimsv1.ClaimStatus) bool {
	return st == claimsv1.ClaimStatus_CLAIM_STATUS_ADJUDICATED
}

// current reports whether a claim is adjudicated and still the version in
// force: no replacement or void has superseded it. Only a current claim
// can be adjusted, voided or paid.
func current(c *claimsv1.Claim) bool {
	return adjudicated(c.GetStatus()) && c.GetSupersededBy() == nil
}

// released reports whether a claim's payment run has been released: its
// result has been reported on an 835, so from here it changes only
// through a replacement or void. Before that it is reopened in place.
func released(c *claimsv1.Claim) bool {
	switch c.GetPayment().GetState() {
	case claimsv1.PaymentState_PAYMENT_STATE_ISSUED, claimsv1.PaymentState_PAYMENT_STATE_CLEARED,
		claimsv1.PaymentState_PAYMENT_STATE_RETURNED, claimsv1.PaymentState_PAYMENT_STATE_VOIDED:
		return true
	}
	return false
}

// standing says where a claim stands, for a refusal: its status, or what
// superseded it.
func standing(c *claimsv1.Claim) string {
	switch c.GetSupersededBy().GetFrequency() {
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT:
		return "superseded by a replacement"
	case claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		return "superseded by a void"
	}
	return statusName(c.GetStatus())
}

func outcomeName(o claimsv1.ClaimOutcome) string {
	return o.String()[len("CLAIM_OUTCOME_"):]
}

// supersede marks orig as superseded by c, a replacement or void that has
// just been adjudicated. orig's status and outcome stay as they were.
func (s *Service) supersede(orig, c *claimsv1.Claim) {
	orig.SupersededBy = &claimsv1.SupersededBy{ClaimId: c.GetId(), Frequency: c.GetFrequency(), Time: c.GetAdjudication().GetAdjudicatedTime()}
}

func statusName(st claimsv1.ClaimStatus) string {
	return st.String()[len("CLAIM_STATUS_"):]
}

// EstimateClaim adjudicates a claim that isn't in the system and saves
// nothing.
func (s *Service) EstimateClaim(ctx context.Context, req *claimsv1.EstimateClaimRequest) (*claimsv1.EstimateClaimResponse, error) {
	c := proto.Clone(req.GetClaim()).(*claimsv1.Claim)
	if c.GetId() == "" {
		c.Id = ids.NewV7(s.Now())
	}
	s.prepareNew(c)
	calc, err := s.Engine.Calculate(ctx, c, "")
	if err != nil {
		return nil, err
	}
	s.logf(ctx, "Estimate: would be %s. Nothing saved.", describe(calc.Claim))
	return &claimsv1.EstimateClaimResponse{Claim: calc.Claim}, nil
}

// PreviewReadjudication runs an existing claim whose result has not been
// reported again, as it would be decided now, and saves nothing.
func (s *Service) PreviewReadjudication(ctx context.Context, req *claimsv1.PreviewReadjudicationRequest) (*claimsv1.PreviewReadjudicationResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	switch {
	case c.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_PENDED:
	case !current(c):
		return nil, rejectf("%s is %s; there is nothing to run again", s.ref(c), standing(c))
	case released(c):
		return nil, rejectf("%s has been released for payment and reported; preview an adjustment instead", s.ref(c))
	case c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID || adjudication.ClosedByProvider(c):
		return nil, rejectf("%s was closed by a void or the provider's correction; there is nothing to run again", s.ref(c))
	}
	for _, r := range req.GetResolutions() {
		c.PendOverrides = append(c.PendOverrides, &claimsv1.PendOverride{Code: r.GetCode(), LineNumber: r.GetLineNumber(), Note: r.GetNote()})
	}
	c.AdjudicationHistory = append(c.AdjudicationHistory, c.GetAdjudication())
	c.Adjudication = nil
	// The engine leaves the claim's own postings out of the balances it
	// calculates against, so an adjudicated claim is run as if reopened.
	calc, err := s.Engine.Calculate(ctx, c, "preview")
	if err != nil {
		return nil, err
	}
	s.logf(ctx, "Preview of readjudicating %s: would be %s. Nothing saved.", s.ref(c), describe(calc.Claim))
	return &claimsv1.PreviewReadjudicationResponse{Claim: calc.Claim}, nil
}

// PreviewAdjustment calculates what AdjustClaim would do to a reported
// claim and saves nothing.
func (s *Service) PreviewAdjustment(ctx context.Context, req *claimsv1.PreviewAdjustmentRequest) (*claimsv1.PreviewAdjustmentResponse, error) {
	orig, err := s.adjustable(ctx, req.GetOriginalClaimId())
	if err != nil {
		return nil, err
	}
	repl := s.replacementOf(ctx, orig, req.GetCorrectedClaim(), "preview")
	calc, err := s.Engine.Calculate(ctx, repl, "preview")
	if err != nil {
		return nil, err
	}
	if err := s.nextVersion(ctx, orig, calc.Claim); err != nil {
		return nil, err
	}
	d := delta(orig, calc.Claim)
	s.logf(ctx, "Preview of adjusting %s: payee holds %s (%s), replacement pays %s, delta %s. Nothing saved.",
		s.ref(orig), USD(d.GetOriginalPaid().GetCents()), basisText(d.GetBasis()), USD(d.GetProjectedPaid().GetCents()), USD(d.GetDelta().GetCents()))
	return &claimsv1.PreviewAdjustmentResponse{Claim: calc.Claim, Delta: d}, nil
}

// adjustable returns a claim that can be adjusted: adjudicated, not
// superseded, and reported.
func (s *Service) adjustable(ctx context.Context, id string) (*claimsv1.Claim, error) {
	orig, err := s.get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !current(orig) {
		return nil, rejectf("only an adjudicated claim that has not been superseded can be adjusted; %s is %s", s.ref(orig), standing(orig))
	}
	if !released(orig) {
		return nil, rejectf("%s has not been released for payment, so nothing has been reported to reverse; reopen it instead", s.ref(orig))
	}
	return orig, nil
}

// replacementOf builds the replacement of orig: the corrected claim, or
// the original's content unchanged to be re-adjudicated against current
// data. It is a new claim with its own id; its link to the original is
// original_claim_id and the shared claim number.
func (s *Service) replacementOf(ctx context.Context, orig, corrected *claimsv1.Claim, reason string) *claimsv1.Claim {
	src := corrected
	if src == nil {
		src = orig
	}
	repl := proto.Clone(src).(*claimsv1.Claim)
	if repl.GetId() == "" || repl.GetId() == orig.GetId() {
		repl.Id = ids.NewV7(s.Now())
	}
	repl.ReceivedTime = nil
	s.prepareNew(repl)
	repl.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	repl.OriginalLink = &claimsv1.ClaimLink{ClaimId: orig.GetId(), LinkedBy: store.Actor(ctx), LinkedTime: timestamppb.New(s.Now()), Note: reason}
	return repl
}

func (s *Service) ProcessClaim(ctx context.Context, req *claimsv1.ProcessClaimRequest) (*claimsv1.ProcessClaimResponse, error) {
	c := proto.Clone(req.GetClaim()).(*claimsv1.Claim)
	if c.GetId() == "" {
		c.Id = ids.NewV7(s.Now())
	}
	// An API submitter that names the original of a replacement or void
	// links it; the link is an input that adjudication honors.
	if c.GetOriginalClaimId() != "" && c.GetOriginalLink() == nil {
		c.OriginalLink = &claimsv1.ClaimLink{ClaimId: c.GetOriginalClaimId(), LinkedBy: store.Actor(ctx), LinkedTime: timestamppb.New(s.Now())}
	}
	s.prepareNew(c)
	out, err := s.register(ctx, c, "")
	if err != nil {
		return nil, err
	}
	return &claimsv1.ProcessClaimResponse{Claim: out}, nil
}

// register adjudicates a new claim for the first time and saves it. The
// claim number is assigned here, from what this first run matched: a
// replacement or void matched to its original takes the original's number
// and the next version, anything else a new number. The number never
// changes after this, even if a later run matches a different original.
// No claim is saved without a number.
func (s *Service) register(ctx context.Context, c *claimsv1.Claim, reason string) (*claimsv1.Claim, error) {
	for attempt := 1; ; attempt++ {
		var saved *claimsv1.Claim
		var calc *adjudication.Calculation
		err := s.inTx(ctx, func(ctx context.Context) error {
			var err error
			if calc, err = s.Engine.Calculate(ctx, c, reason); err != nil {
				return err
			}
			out := calc.Claim
			orig, err := s.number(ctx, out)
			if err != nil {
				return err
			}
			// A retry keeps the number from the first attempt.
			c.ClaimNumber, c.ClaimVersion = out.GetClaimNumber(), out.GetClaimVersion()
			// A correction of an original nobody has been told about
			// closes it, and is then decided as a claim of its own.
			if closed, err := s.closeUnreported(ctx, out, orig); err != nil {
				return err
			} else if closed {
				if calc, err = s.Engine.Calculate(ctx, c, reason); err != nil {
					return err
				}
				out = calc.Claim
			}
			s.logReceived(ctx, out, orig)
			if out.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID && out.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_PENDED {
				r, err := s.finishVoid(ctx, orig, out, voidReason(out, reason), true)
				saved = r.GetVoidClaim()
				return err
			}
			saved, err = s.commit(ctx, calc, true)
			return err
		})
		if errors.Is(err, store.ErrAlreadyExists) && s.exists(ctx, c.GetId()) {
			return nil, rejectf("claim %s already exists", c.GetId())
		}
		if stale(err) && attempt < maxAttempts {
			s.logStale(ctx, c, calc, err)
			continue
		}
		return saved, err
	}
}

// number makes sure a claim has its number before it is first saved, and
// returns the original its first run matched, if any.
//
// A provider's submission (837, API, portal, paper), corrections and
// voids included, is a new claim with a new number: intake reserved it
// with the rest of the file, or one is reserved now. Only adjustments
// and voids we create are versions of an existing number, and they
// arrive numbered (AdjustClaim, VoidClaim).
func (s *Service) number(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error) {
	var orig *claimsv1.Claim
	if id := c.GetOriginalClaimId(); id != "" {
		var err error
		if orig, err = s.get(ctx, id); err != nil {
			return nil, err
		}
	}
	if c.GetClaimNumber() != "" {
		return orig, nil
	}
	return orig, s.assignNumber(ctx, c)
}

func voidReason(c *claimsv1.Claim, reason string) string {
	if reason != "" {
		return reason
	}
	if q := c.GetPayerClaimControlNumber(); q != "" {
		return "provider submitted a void for " + q
	}
	return "provider submitted a void"
}

// logReceived says how a new claim was numbered.
func (s *Service) logReceived(ctx context.Context, c, orig *claimsv1.Claim) {
	adj := c.GetAdjudication()
	switch {
	case c.GetFrequency() != claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT && c.GetFrequency() != claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		if c.GetSource() == claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837 {
			s.logf(ctx, "%s received (patient control number %s).", s.ref(c), c.GetPatientControlNumber())
		}
	case orig == nil:
		s.logf(ctx, "%s received: a %s whose original isn't matched. Numbered as a new claim.", s.ref(c), kind(c))
	case adj.GetOriginalMatch() == claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER:
		line := fmt.Sprintf("%s received: a %s of %s, matched from the claim number the provider quoted", s.ref(c), kind(c), s.ref(orig))
		if q := c.GetPayerClaimControlNumber(); q != DisplayNumber(orig) {
			line += fmt.Sprintf(" (%s; the current version is %s)", q, s.ref(orig))
		}
		s.logf(ctx, "%s.", line)
	}
}

func kind(c *claimsv1.Claim) string {
	if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
		return "void"
	}
	return "corrected claim"
}

// prepareNew resets everything on an incoming claim that only the system
// may set.
func (s *Service) prepareNew(c *claimsv1.Claim) {
	if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_UNSPECIFIED {
		c.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL
	}
	c.Status = claimsv1.ClaimStatus_CLAIM_STATUS_RECEIVED
	c.Adjudication, c.AdjudicationHistory = nil, nil
	c.Payment, c.PendOverrides, c.Audit = nil, nil, nil
	c.ClaimNumber, c.ClaimVersion = "", 0
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
		var out *claimsv1.Claim
		var calc *adjudication.Calculation
		err := s.inTx(ctx, func(ctx context.Context) error {
			var err error
			if calc, err = s.Engine.Calculate(ctx, c, reason); err != nil {
				return err
			}
			var orig *claimsv1.Claim
			if id := calc.Claim.GetOriginalClaimId(); id != "" {
				if orig, err = s.get(ctx, id); err != nil {
					return err
				}
			}
			if closed, err := s.closeUnreported(ctx, calc.Claim, orig); err != nil {
				return err
			} else if closed {
				if calc, err = s.Engine.Calculate(ctx, c, reason); err != nil {
					return err
				}
			}
			// A pended void is never priced: once its original can be
			// voided, this run adjudicates it.
			if v := calc.Claim; v.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID && v.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_PENDED {
				r, err := s.finishVoid(ctx, orig, v, voidReason(v, reason), false)
				out = r.GetVoidClaim()
				return err
			}
			out, err = s.commit(ctx, calc, false)
			return err
		})
		if stale(err) && attempt < maxAttempts {
			s.logStale(ctx, c, calc, err)
			continue
		}
		return out, err
	}
}

// commit makes a calculation real: posts accumulators if the claim is
// adjudicated, saves the claim (creating it on its first run), and for a
// replacement claim retires the original.
func (s *Service) commit(ctx context.Context, calc *adjudication.Calculation, create bool) (*claimsv1.Claim, error) {
	c := calc.Claim
	adj := c.GetAdjudication()
	final := adjudicated(c.GetStatus())

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

	save := s.Repos.Claims.Update
	if create {
		save = s.Repos.Claims.Create
	}
	saved, err := save(ctx, c)
	if err != nil {
		return nil, err
	}
	s.logf(ctx, "%s adjudicated (run %d): %s.", s.ref(saved), adj.GetRunNumber(), describe(saved))
	if final && len(adj.GetAccumulatorPostings()) > 0 {
		s.logf(ctx, "%s posted %d accumulator amounts to %s.", s.ref(saved), len(adj.GetAccumulatorPostings()), calc.Family.GetId())
	}

	if orig == nil {
		return saved, nil
	}
	// Only a result that was reported is superseded. An original that
	// wasn't was closed before this claim was calculated (closeUnreported).
	if final && released(orig) {
		s.supersede(orig, saved)
		if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
			return nil, err
		}
		s.logf(ctx, "%s is superseded by its replacement %s; its accumulator postings were reversed and the replacement's posted.", s.ref(orig), s.ref(saved))
	}
	return saved, nil
}

func (s *Service) ReprocessClaim(ctx context.Context, req *claimsv1.ReprocessClaimRequest) (*claimsv1.ReprocessClaimResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	if c.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_PENDED {
		return nil, rejectf("only PENDED claims can be reprocessed; %s is %s. Reopen an adjudicated claim whose payment has not been released; adjust or void one that has",
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
			s.logf(ctx, "Note: %s is caused by data, not judgment. Resolving it has no effect unless the data has changed.", r.GetCode())
		}
	}
	// The previous run becomes history.
	c.AdjudicationHistory = append(c.AdjudicationHistory, c.GetAdjudication())
	c.Adjudication = nil

	out, err := s.adjudicate(ctx, c, req.GetReason())
	if err != nil {
		return nil, err
	}
	return &claimsv1.ReprocessClaimResponse{Claim: out}, nil
}

func (s *Service) AdjustClaim(ctx context.Context, req *claimsv1.AdjustClaimRequest) (*claimsv1.AdjustClaimResponse, error) {
	orig, err := s.adjustable(ctx, req.GetOriginalClaimId())
	if err != nil {
		return nil, err
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	// An early, plain answer for the examiner. What guarantees one open
	// replacement is the engine locking the original in the transaction.
	pending, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1, Where: []store.Cond{store.Eq(orig.GetId(), "originalClaimId")}, Filter: func(o *claimsv1.Claim) bool {
		return o.GetOriginalClaimId() == orig.GetId() && !adjudicated(o.GetStatus()) &&
			o.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT
	}})
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return nil, rejectf("%s already has a replacement in progress (%s)", s.ref(orig), s.ref(pending[0]))
	}

	// Our adjustment: the next version of the original's number.
	repl := s.replacementOf(ctx, orig, req.GetCorrectedClaim(), req.GetReason())
	if err := s.nextVersion(ctx, orig, repl); err != nil {
		return nil, err
	}
	resp := &claimsv1.AdjustClaimResponse{}
	if resp.Claim, err = s.register(ctx, repl, req.GetReason()); err != nil {
		return nil, err
	}
	s.logf(ctx, "Created replacement %s for %s (%s).", s.ref(resp.Claim), s.ref(orig), req.GetReason())
	resp.Delta = delta(orig, resp.GetClaim())
	d := resp.GetDelta()
	s.logf(ctx, "Adjustment %s: payee holds %s (%s), replacement pays %s, delta %s.",
		s.ref(orig), USD(d.GetOriginalPaid().GetCents()), basisText(d.GetBasis()),
		USD(d.GetProjectedPaid().GetCents()), USD(d.GetDelta().GetCents()))
	return resp, nil
}

func basisText(b claimsv1.DeltaBasis) string {
	switch b {
	case claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT:
		return "payment issued"
	case claimsv1.DeltaBasis_DELTA_BASIS_PAYMENT_NOT_HELD:
		return "payment returned or voided"
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
	if !current(orig) {
		return nil, rejectf("only an adjudicated claim that has not been superseded can be voided; %s is %s", s.ref(orig), standing(orig))
	}
	if req.GetReason() == "" {
		return nil, rejectf("a reason is required")
	}
	if !released(orig) {
		return nil, rejectf("%s has not been released for payment, so nothing has been reported to reverse; reopen it instead", s.ref(orig))
	}

	var resp *claimsv1.VoidClaimResponse
	err = s.inTx(ctx, func(ctx context.Context) error {
		void, err := s.newVoid(ctx, orig)
		if err != nil {
			return err
		}
		resp, err = s.finishVoid(ctx, orig, void, req.GetReason(), true)
		return err
	})
	return resp, err
}

// newVoid builds an examiner's void of orig: a copy of it, so the
// remittance can reverse it line by line, with the next version of its
// number.
func (s *Service) newVoid(ctx context.Context, orig *claimsv1.Claim) (*claimsv1.Claim, error) {
	void := proto.Clone(orig).(*claimsv1.Claim)
	void.Id = ids.NewV7(s.Now())
	void.ReceivedTime = nil
	s.prepareNew(void)
	void.Frequency = claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
	void.OriginalClaimId = orig.GetId()
	void.OriginalLink = &claimsv1.ClaimLink{ClaimId: orig.GetId(), LinkedBy: store.Actor(ctx), LinkedTime: timestamppb.New(s.Now())}
	if err := s.nextVersion(ctx, orig, void); err != nil {
		return nil, err
	}
	return void, nil
}

// finishVoid adjudicates a void and supersedes its original. A void's
// outcome is VOID with nothing payable, so the next remittance reports
// the reversal. create is false when the void was already saved (pended).
func (s *Service) finishVoid(ctx context.Context, orig, void *claimsv1.Claim, reason string, create bool) (*claimsv1.VoidClaimResponse, error) {
	void.Status = claimsv1.ClaimStatus_CLAIM_STATUS_ADJUDICATED
	// The run that cleared the original is the one that adjudicates the
	// void, keeping what it matched. An examiner's void has no run yet.
	adj := void.GetAdjudication()
	if adj == nil {
		adj = &claimsv1.Adjudication{
			RunNumber: int32(len(void.GetAdjudicationHistory()) + 1), EngineVersion: s.Engine.Version,
			OriginalClaimId: orig.GetId(), OriginalMatch: claimsv1.OriginalMatch_ORIGINAL_MATCH_LINKED,
		}
	}
	adj.AdjudicatedTime, adj.Pends = timestamppb.New(s.Now()), nil
	if adj.GetReason() == "" {
		adj.Reason = reason
	}
	adj.TotalAllowed, adj.TotalPaid, adj.TotalMemberResponsibility = cents(0), cents(0), cents(0)
	adj.Outcome = claimsv1.ClaimOutcome_CLAIM_OUTCOME_VOID
	void.Adjudication = adj
	for _, l := range void.GetLines() {
		l.Adjudication = nil
	}
	save := s.Repos.Claims.Update
	if create {
		save = s.Repos.Claims.Create
	}
	saved, err := save(ctx, void)
	if err != nil {
		return nil, err
	}

	// An original whose result was never reported was closed before the
	// void was calculated (closeUnreported): there is nothing to supersede.
	if !released(orig) {
		return &claimsv1.VoidClaimResponse{VoidClaim: saved}, nil
	}
	if fam := orig.GetAdjudication().GetFamilyAccumulatorsId(); fam != "" {
		if err := s.Ledger.Reverse(ctx, fam, orig.GetId()); err != nil {
			return nil, err
		}
	}
	s.supersede(orig, saved)
	if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
		return nil, err
	}
	s.logf(ctx, "%s is superseded by its void %[3]s (%[2]s); its accumulator postings were reversed.", s.ref(orig), reason, s.ref(saved))
	if held := heldAmount(orig); held > 0 {
		s.logf(ctx, "%s was paid on %s and is to be recovered on the next remittance.", USD(held), s.ref(orig))
	}

	return &claimsv1.VoidClaimResponse{VoidClaim: saved}, nil
}

// ReopenClaim puts an adjudicated claim whose payment has not been
// released back to PENDED, to be re-run in place.
func (s *Service) ReopenClaim(ctx context.Context, req *claimsv1.ReopenClaimRequest) (*claimsv1.ReopenClaimResponse, error) {
	c, err := s.get(ctx, req.GetClaimId())
	if err != nil {
		return nil, err
	}
	switch {
	case req.GetReason() == "":
		return nil, rejectf("a reason is required")
	case !current(c):
		return nil, rejectf("only an adjudicated claim that has not been superseded can be reopened; %s is %s", s.ref(c), standing(c))
	case released(c):
		return nil, rejectf("%s has been released for payment and reported; adjust or void it instead", s.ref(c))
	case c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID:
		return nil, rejectf("%s is a void; there is nothing to re-run", s.ref(c))
	case adjudication.ClosedByProvider(c):
		return nil, rejectf("%s was replaced or voided by the provider; work the claim that took its place", s.ref(c))
	}
	var saved *claimsv1.Claim
	err = s.inTx(ctx, func(ctx context.Context) error {
		selected := c.GetPayment() != nil
		prev, err := s.withdraw(ctx, c)
		if err != nil {
			return err
		}
		c.Status = claimsv1.ClaimStatus_CLAIM_STATUS_PENDED
		c.Adjudication = &claimsv1.Adjudication{
			AdjudicatedTime: timestamppb.New(s.Now()), RunNumber: prev.GetRunNumber() + 1, Reason: req.GetReason(), EngineVersion: s.Engine.Version,
			CustomerId: prev.GetCustomerId(), MemberId: prev.GetMemberId(), OriginalClaimId: prev.GetOriginalClaimId(), OriginalMatch: prev.GetOriginalMatch(),
			Pends: []*claimsv1.Pend{{Code: adjudication.PendReopened, Message: req.GetReason()}},
		}
		for _, l := range c.GetLines() {
			l.Adjudication = nil
		}
		if saved, err = s.Repos.Claims.Update(ctx, c); err != nil {
			return err
		}
		line := fmt.Sprintf("%s reopened (%s): back to PENDED, its accumulator postings reversed.", s.ref(c), req.GetReason())
		if selected {
			line += " Taken out of the payment run."
		}
		s.logf(ctx, "%s", line)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &claimsv1.ReopenClaimResponse{Claim: saved}, nil
}

// withdraw takes back an adjudicated result that has not been reported:
// its accumulator postings are reversed, it leaves any unreleased payment
// run, and the result moves to the history. It returns that result; the
// caller sets what the claim is now and saves it.
func (s *Service) withdraw(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Adjudication, error) {
	prev := c.GetAdjudication()
	if fam := prev.GetFamilyAccumulatorsId(); fam != "" {
		if err := s.Ledger.Reverse(ctx, fam, c.GetId()); err != nil {
			return nil, err
		}
	}
	c.AdjudicationHistory = append(c.AdjudicationHistory, prev)
	c.Payment = nil
	return prev, nil
}

// closeUnreported handles a provider's corrected claim or void (by) that
// matched an original whose result has not been reported. There is nothing
// to reverse on an 835, so the original is not superseded: it is given an
// answer of its own, denied because the provider replaced or voided it,
// and by is then decided as a claim in its own right. It reports whether
// it closed the original, in which case by must be recalculated: the
// family's balances have changed.
func (s *Service) closeUnreported(ctx context.Context, by, orig *claimsv1.Claim) (bool, error) {
	if orig == nil || !current(orig) || released(orig) || adjudication.ClosedByProvider(orig) {
		return false, nil
	}
	// The match has to stand: a correction pended over its original
	// leaves the original alone.
	for _, p := range by.GetAdjudication().GetPends() {
		switch p.GetCode() {
		case adjudication.PendOriginalNotFound, adjudication.PendOriginalNotFinalized, adjudication.PendOpenReplacement:
			return false, nil
		}
	}
	selected := orig.GetPayment() != nil
	prev, err := s.withdraw(ctx, orig)
	if err != nil {
		return false, err
	}
	code, what := adjudication.DenyReplacedByProvider, "replaced by the provider's corrected claim "
	if by.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
		code, what = adjudication.DenyVoidedByProvider, "voided by the provider's claim "
	}
	what += DisplayNumber(by)
	adjudication.DenyLines(orig, code, what)
	orig.Adjudication = &claimsv1.Adjudication{
		AdjudicatedTime: timestamppb.New(s.Now()), RunNumber: prev.GetRunNumber() + 1, Reason: what, EngineVersion: s.Engine.Version,
		CustomerId: prev.GetCustomerId(), MemberId: prev.GetMemberId(), OriginalClaimId: prev.GetOriginalClaimId(), OriginalMatch: prev.GetOriginalMatch(),
		PlanId: prev.GetPlanId(), PlanVersion: prev.GetPlanVersion(), CoverageSpanId: prev.GetCoverageSpanId(), NetworkTierId: prev.GetNetworkTierId(),
		TotalAllowed: cents(0), TotalPaid: cents(0), TotalMemberResponsibility: cents(0),
		Outcome: claimsv1.ClaimOutcome_CLAIM_OUTCOME_DENIED,
	}
	if _, err := s.Repos.Claims.Update(ctx, orig); err != nil {
		return false, err
	}
	line := fmt.Sprintf("%s was not yet reported, so it is closed as DENIED (%s) rather than superseded; its accumulator postings were reversed.", s.ref(orig), what)
	if selected {
		line += " Taken out of the payment run."
	}
	s.logf(ctx, "%s", line)
	return true, nil
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
	}
	switch adj.GetOutcome() {
	case claimsv1.ClaimOutcome_CLAIM_OUTCOME_VOID:
		return "VOID"
	case claimsv1.ClaimOutcome_CLAIM_OUTCOME_DENIED:
		reason := ""
		for _, l := range c.GetLines() {
			if d := l.GetAdjudication().GetDenial(); d != nil {
				reason = " (" + d.GetCode() + ")"
				break
			}
		}
		return "DENIED" + reason
	}
	return fmt.Sprintf("%s, allowed %s, plan pays %s, member owes %s", outcomeName(adj.GetOutcome()),
		USD(adj.GetTotalAllowed().GetCents()), USD(adj.GetTotalPaid().GetCents()), USD(adj.GetTotalMemberResponsibility().GetCents()))
}

func cents(c int64) *commonv1.Money { return &commonv1.Money{Cents: c} }

func cloneClaim(c *claimsv1.Claim) *claimsv1.Claim { return proto.Clone(c).(*claimsv1.Claim) }
