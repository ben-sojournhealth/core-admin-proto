package adjudication

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	employerv1 "coreadmin/gen/coreadmin/employer/v1"
	networkv1 "coreadmin/gen/coreadmin/network/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer"
	"coreadmin/internal/network"
)

// MedicareFeeScheduleID is the fee schedule used for "percent of Medicare"
// pricing.
const MedicareFeeScheduleID = "fs_medicare"

// Additional pend and denial codes used by DefaultEngine.
const (
	PendMemberNotFound    = "MEMBER_NOT_FOUND"
	PendNoPlanVersion     = "NO_PLAN_VERSION"
	PendNoTier            = "NO_TIER"
	PendPossibleDuplicate = "POSSIBLE_DUPLICATE"

	// A replacement or void whose original cannot be used yet.
	PendOriginalNotFound     = "ORIGINAL_NOT_FOUND"
	PendOriginalNotFinalized = "ORIGINAL_NOT_FINALIZED"
	PendOpenReplacement      = "ORIGINAL_HAS_OPEN_REPLACEMENT"
	// A line's CARC lacks a remark code it requires (see remarkRules).
	// Caused by our rules, not the claim: no examiner override clears it.
	PendRemarkCodeRequired = "REMARK_CODE_REQUIRED"

	DenyCoverageTerminated = "COVERAGE_TERMINATED"
	DenyProviderSanctioned = "PROVIDER_SANCTIONED"
	DenyNotCovered         = "NOT_COVERED"
)

// overridable lists the pends an examiner can clear by judgment. Every
// other pend is caused by data and returns until the data changes.
var overridable = map[string]bool{
	PendPriorAuthRequired: true,
	PendPossibleDuplicate: true,
}

// Overridable reports whether an examiner override clears this pend code.
func Overridable(code string) bool { return overridable[code] }

// DenyError denies a whole claim. How it is reported comes from denials.
type DenyError struct {
	Code    string
	Message string
}

// denialReport is how a denial reaches the provider on the 835.
type denialReport struct {
	// Claim adjustment reason code.
	CARC string
	// PR: the member may be billed for the denied charge. Otherwise CO:
	// the provider writes it off.
	MemberLiable bool
	// Remittance advice remark codes (RARCs): required by some CARCs, and
	// they say more than the CARC alone.
	Remarks []string
}

// denials maps each denial code to its 835 reporting. Every Deny*
// constant has an entry.
var denials = map[string]denialReport{
	// 96: non-covered charge(s); requires a remark code. N130: consult
	// plan benefit documents for restrictions on this service.
	DenyNotCovered: {CARC: "96", MemberLiable: true, Remarks: []string{"N130"}},
	// 27: expenses incurred after coverage terminated.
	DenyCoverageTerminated: {CARC: "27", MemberLiable: true},
	// B7: provider not certified or eligible to be paid for this service
	// on this date.
	DenyProviderSanctioned: {CARC: "B7"},
}

func (e *DenyError) Error() string { return fmt.Sprintf("deny %s: %s", e.Code, e.Message) }

// DefaultEngine is a working, deliberately simple implementation of the
// pipeline described in the package doc. Professional claims only; no
// coordination of benefits, benefit limits or bundling edits.
type DefaultEngine struct {
	Repos *repo.Repos
	// Employer and Network answer adjudication's questions about members,
	// coverage, plans, providers and contracts. Nil means the domains'
	// own implementations over Repos.
	Employer employer.API
	Network  network.API
	Ledger   AccumulatorLedger
	Now      func() time.Time
	Version  string
}

func (e *DefaultEngine) employer() employer.API {
	if e.Employer != nil {
		return e.Employer
	}
	return employer.New(e.Repos)
}

func (e *DefaultEngine) network() network.API {
	if e.Network != nil {
		return e.Network
	}
	return network.New(e.Repos)
}

// Calculation is an adjudication result that has not been committed.
type Calculation struct {
	// Claim with status, adjudication and line adjudications filled in.
	Claim *claimsv1.Claim
	// Header of the family accumulator record the result was calculated
	// against, and its revision. Nil when the claim never got as far as
	// accumulators (e.g. no eligibility).
	Family   *claimsv1.FamilyAccumulators
	Revision int64
	// Limits by accumulator id, from the plan version used.
	Limits map[string]*commonv1.Money
	// For a replacement claim: the original, whose postings are excluded
	// from the calculation and must be reversed when this is committed.
	ReverseClaimID string
}

// Calculate adjudicates a claim without saving anything or posting
// accumulators. The caller commits the result (see claims.Service), and
// on ErrBalancesChanged calls Calculate again.
func (e *DefaultEngine) Calculate(ctx context.Context, in *claimsv1.Claim, reason string) (*Calculation, error) {
	c := proto.Clone(in).(*claimsv1.Claim)
	adj := &claimsv1.Adjudication{
		AdjudicatedTime: timestamppb.New(e.Now()),
		RunNumber:       int32(len(c.GetAdjudicationHistory()) + 1),
		Reason:          reason,
		EngineVersion:   e.Version,
	}
	c.Adjudication = adj
	for _, l := range c.GetLines() {
		l.Adjudication = nil
	}
	calc := &Calculation{Claim: c}
	// Matches are made fresh on every run; the previous run's are output.
	c.CustomerId, c.MemberId, c.SubscriberId, c.OriginalClaimId = "", "", "", ""

	err := e.run(ctx, calc)
	var deny *DenyError
	if p, ok := AsPend(err); ok {
		adj.Pends = append(adj.Pends, &claimsv1.Pend{Code: p.Code, Message: p.Message})
		for _, l := range c.GetLines() {
			l.Adjudication = &claimsv1.LineAdjudication{
				Status: claimsv1.LineStatus_LINE_STATUS_PENDED,
				Charge: l.GetCharge(),
			}
		}
	} else if errors.As(err, &deny) {
		for _, l := range c.GetLines() {
			l.Adjudication = deniedLine(l, deny.Code, deny.Message)
		}
	} else if err != nil {
		return nil, err
	}
	checkRemarks(c)

	// Totals and status.
	var allowed, paid, member int64
	for _, l := range c.GetLines() {
		la := l.GetAdjudication()
		if la.GetStatus() != claimsv1.LineStatus_LINE_STATUS_PAYABLE {
			continue
		}
		allowed += la.GetPricing().GetAllowed().GetCents()
		paid += la.GetPaid().GetCents()
		member += la.GetMemberResponsibility().GetCents()
	}
	adj.TotalAllowed, adj.TotalPaid, adj.TotalMemberResponsibility = money(allowed), money(paid), money(member)
	switch {
	case len(adj.GetPends()) > 0:
		c.Status = claimsv1.ClaimStatus_CLAIM_STATUS_PENDED
		// Nothing posts for a pended claim.
		adj.AccumulatorPostings = nil
	default:
		// No open pends and every line decided.
		c.Status = claimsv1.ClaimStatus_CLAIM_STATUS_ADJUDICATED
		adj.Outcome = Outcome(c)
	}
	return calc, nil
}

func (e *DefaultEngine) run(ctx context.Context, calc *Calculation) error {
	c, adj := calc.Claim, calc.Claim.GetAdjudication()
	dos := c.GetServiceDates().GetStart()
	if dos == nil {
		return fmt.Errorf("adjudication: claim %s has no service date", c.GetId())
	}

	// Match the member and, for a replacement or void, the original. Both
	// are recorded whichever of them pends, so an examiner sees everything
	// that did match.
	var orig *claimsv1.Claim
	var origErr error
	correcting := c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT || c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID
	if correcting {
		orig, adj.OriginalMatch, origErr = e.MatchOriginal(ctx, c)
		if orig != nil {
			c.OriginalClaimId, adj.OriginalClaimId = orig.GetId(), orig.GetId()
			// Lock the original before deciding anything from it, then
			// read it again: of two corrections of the same claim in
			// flight at once, only one may find it free.
			var err error
			if orig, err = e.lockOriginal(ctx, orig.GetId()); err != nil {
				return err
			}
		}
		if _, ok := AsPend(origErr); origErr != nil && !ok {
			return origErr
		}
	}
	member, memberErr := e.resolveMember(ctx, c)
	if member != nil {
		c.MemberId, c.SubscriberId, c.CustomerId = member.GetId(), member.GetSubscriberId(), member.GetCustomerId()
		adj.MemberId, adj.CustomerId = member.GetId(), member.GetCustomerId()
	}
	if correcting {
		if origErr != nil {
			return origErr
		}
		if err := e.checkOriginal(ctx, c, orig); err != nil {
			return err
		}
		if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
			// A void is not priced; the claims service adjudicates it.
			return nil
		}
		calc.ReverseClaimID = orig.GetId()
	}

	// 1. Eligibility.
	if memberErr != nil {
		return memberErr
	}
	coverage, err := e.employer().GetMedicalCoverage(ctx, &employerv1.GetMedicalCoverageRequest{MemberId: member.GetId(), On: dos})
	if err != nil {
		return err
	}
	span, err := resolveCoverage(coverage, dos)
	if err != nil {
		return err
	}
	adj.CoverageSpanId = span.GetId()

	// 2. Plan version.
	pv, err := e.employer().GetPlanVersion(ctx, &employerv1.GetPlanVersionRequest{PlanId: span.GetPlanId(), On: dos})
	if employer.NotFound(err) {
		return Pend(PendNoPlanVersion, "plan %s not found", span.GetPlanId())
	} else if err != nil {
		return err
	}
	plan, version := pv.GetPlan(), pv.GetVersion()
	if version == nil {
		return Pend(PendNoPlanVersion, "plan %s has no version in force on %s", plan.GetId(), dates.Format(dos))
	}
	adj.PlanId, adj.PlanVersion = plan.GetId(), version.GetVersion()

	// Duplicate check.
	if dup, err := e.findDuplicate(ctx, c); err != nil {
		return err
	} else if dup != "" && !overridden(c, PendPossibleDuplicate, 0) {
		return Pend(PendPossibleDuplicate, "same member, provider, date of service and services as %s", dup)
	}

	// 3. Provider and 4. network tier.
	prov, tierID, part, err := e.resolveProvider(ctx, c.GetBillingProvider(), version.GetNetworkId(), dos)
	if network.NotFound(err) {
		return Pend(PendNoTier, "plan %s uses network %s, which is not found", plan.GetId(), version.GetNetworkId())
	} else if err != nil {
		return err
	}
	if prov != nil {
		c.BillingProvider.ProviderId = prov.GetId()
	}
	tier := selectTier(version, tierID)
	if tier == nil && tierID != "" {
		return Pend(PendNoTier, "plan %s has no tier %s of network %s", plan.GetId(), tierID, version.GetNetworkId())
	} else if tier == nil {
		return Pend(PendNoTier, "plan %s has no out-of-network tier", plan.GetId())
	}
	adj.NetworkTierId = tier.GetId()

	// Accumulator snapshot. This is the working copy: line cost sharing is
	// added to it so later lines see earlier lines.
	// Balances as if the claim this one replaces had never posted, or, with
	// none, as if this claim itself hadn't: a claim is never decided
	// against its own cost share (a preview of one already adjudicated).
	exclude := calc.ReverseClaimID
	if exclude == "" {
		exclude = c.GetId()
	}
	work, err := e.Ledger.Snapshot(ctx, SnapshotRequest{Plan: plan, Member: member, AsOf: dos, ExcludeClaimID: exclude})
	if err != nil {
		return err
	}
	calc.Revision = work.GetAudit().GetRevision()
	calc.Family = &claimsv1.FamilyAccumulators{
		Id: work.GetId(), CustomerId: work.GetCustomerId(), SubscriberId: work.GetSubscriberId(),
		PlanId: work.GetPlanId(), BenefitPeriod: work.GetBenefitPeriod(),
	}
	calc.Limits = map[string]*commonv1.Money{}
	for _, a := range version.GetAccumulators() {
		calc.Limits[a.GetId()] = a.GetLimit()
	}
	adj.FamilyAccumulatorsId, adj.FamilyAccumulatorsRevision = work.GetId(), calc.Revision

	// 5. Lines.
	for _, line := range c.GetLines() {
		la := &claimsv1.LineAdjudication{Charge: line.GetCharge()}
		line.Adjudication = la
		pendLine := func(code, format string, args ...any) {
			la.Status = claimsv1.LineStatus_LINE_STATUS_PENDED
			adj.Pends = append(adj.Pends, &claimsv1.Pend{
				Code: code, Message: fmt.Sprintf(format, args...), LineNumber: line.GetLineNumber(),
			})
		}

		cat, ben := matchBenefit(version, c, line)
		if cat == nil {
			if version.GetDefaultBehavior() == planv1.DefaultBenefitBehavior_DEFAULT_BENEFIT_BEHAVIOR_DENY {
				line.Adjudication = deniedLine(line, DenyNotCovered, "no benefit covers this service")
			} else {
				pendLine(PendNoBenefitMatch, "no service category on plan %s matches procedure %s", plan.GetId(), line.GetProcedureCode())
			}
			continue
		}
		var cs *planv1.CostShare
		for _, s := range ben.GetCostShares() {
			if s.GetTierId() == tier.GetId() {
				cs = s
			}
		}
		if cs == nil || !cs.GetCovered() {
			line.Adjudication = deniedLine(line, DenyNotCovered,
				fmt.Sprintf("%s is not covered in tier %s", cat.GetName(), tier.GetId()))
			continue
		}
		la.Benefit = &claimsv1.BenefitApplied{ServiceCategoryId: cat.GetId(), NetworkTierId: tier.GetId(), CostShare: cs}

		if ben.GetRequiresPriorAuth() && len(c.GetAuthorizationNumbers()) == 0 &&
			!overridden(c, PendPriorAuthRequired, line.GetLineNumber()) {
			pendLine(PendPriorAuthRequired, "%s requires prior authorization and none is on the claim", cat.GetName())
			continue
		}

		pricing, err := e.price(ctx, line, tier, part)
		if p, ok := AsPend(err); ok {
			pendLine(p.Code, "%s", p.Message)
			continue
		} else if err != nil {
			return err
		}
		la.Pricing = pricing

		postings := applyCostShare(la, cs, version, tier.GetId(), member, work, line.GetLineNumber())
		adj.AccumulatorPostings = append(adj.AccumulatorPostings, postings...)
		la.CasAdjustments = DeriveCAS(la)
		la.Status = claimsv1.LineStatus_LINE_STATUS_PAYABLE
		if err := CheckLineBalanced(la); err != nil {
			return fmt.Errorf("adjudication: claim %s line %d does not balance: %w", c.GetId(), line.GetLineNumber(), err)
		}
	}
	return nil
}

// Outcome is what an adjudicated claim's lines add up to: approved when
// every line is payable, denied when none is, partially denied between. A
// void decides no lines.
func Outcome(c *claimsv1.Claim) claimsv1.ClaimOutcome {
	if c.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
		return claimsv1.ClaimOutcome_CLAIM_OUTCOME_VOID
	}
	payable, denied := false, false
	for _, l := range c.GetLines() {
		if l.GetAdjudication().GetStatus() == claimsv1.LineStatus_LINE_STATUS_PAYABLE {
			payable = true
		} else {
			denied = true
		}
	}
	switch {
	case payable && denied:
		return claimsv1.ClaimOutcome_CLAIM_OUTCOME_PARTIALLY_DENIED
	case payable:
		return claimsv1.ClaimOutcome_CLAIM_OUTCOME_APPROVED
	}
	return claimsv1.ClaimOutcome_CLAIM_OUTCOME_DENIED
}

// MatchOriginal finds the original a replacement or void (frequency 7 or
// 8) refers to: the linked claim if original_link is set, otherwise the
// claim number the provider quoted (REF*F8), with or without a version
// suffix, resolved to that claim's latest version before this one that is
// not a void. A voided claim resolves to itself, superseded by its void,
// so checkOriginal pends what references it. It returns a PendError when nothing matches.
func (e *DefaultEngine) MatchOriginal(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Claim, claimsv1.OriginalMatch, error) {
	if link := c.GetOriginalLink(); link != nil {
		orig, err := e.Repos.Claims.Get(ctx, link.GetClaimId())
		if errors.Is(err, store.ErrNotFound) {
			return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, Pend(PendOriginalNotFound, "linked original %s does not exist", link.GetClaimId())
		}
		return orig, claimsv1.OriginalMatch_ORIGINAL_MATCH_LINKED, err
	}
	quoted := c.GetPayerClaimControlNumber()
	if quoted == "" {
		return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, Pend(PendOriginalNotFound, "the provider sent no original claim number")
	}
	notFound := Pend(PendOriginalNotFound, "the provider quotes claim %s, which matches no claim", quoted)
	m := quotedNumber.FindStringSubmatch(quoted)
	if m == nil {
		return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, notFound
	}
	if v, _ := strconv.Atoi(m[2]); m[2] != "" && v < 2 {
		return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, notFound
	}
	versions, _, err := e.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1000, Where: []store.Cond{store.Eq(m[1], "claimNumber")}, Filter: func(o *claimsv1.Claim) bool {
		return o.GetClaimNumber() == m[1] && o.GetId() != c.GetId() &&
			o.GetFrequency() != claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID &&
			// A version of the same number can only replace an earlier one.
			(c.GetClaimNumber() != m[1] || o.GetClaimVersion() < c.GetClaimVersion())
	}})
	if err != nil {
		return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, err
	}
	if len(versions) == 0 {
		return nil, claimsv1.OriginalMatch_ORIGINAL_MATCH_NONE, notFound
	}
	latest := versions[0]
	for _, v := range versions {
		if v.GetClaimVersion() > latest.GetClaimVersion() {
			latest = v
		}
	}
	return latest, claimsv1.OriginalMatch_ORIGINAL_MATCH_QUOTED_NUMBER, nil
}

var quotedNumber = regexp.MustCompile(`^\s*(\d{5}-[A-Z]-\d{5})(?:-(\d+))?\s*$`)

// checkOriginal pends a replacement or void until its original can be
// replaced: adjudicated, not already superseded or closed by the provider,
// and not already being replaced by another claim. These are data pends: they clear on
// reprocessing once the original settles.
// lockOriginal locks a claim for the rest of the transaction and returns
// its current version. Every replacement or void runs it, pended or not,
// so the check for a replacement already in progress can't be passed by
// two at once. An estimate or preview has no transaction, and nothing to
// protect.
func (e *DefaultEngine) lockOriginal(ctx context.Context, id string) (*claimsv1.Claim, error) {
	if err := e.Repos.Claims.Lock(ctx, id); err != nil {
		return nil, err
	}
	return e.Repos.Claims.Get(ctx, id)
}

func (e *DefaultEngine) checkOriginal(ctx context.Context, c, orig *claimsv1.Claim) error {
	name := displayNumber(orig)
	if orig.GetStatus() != claimsv1.ClaimStatus_CLAIM_STATUS_ADJUDICATED {
		return Pend(PendOriginalNotFinalized, "original %s is %s", name, strings.TrimPrefix(orig.GetStatus().String(), "CLAIM_STATUS_"))
	}
	// A claim that superseded it is this one only on a rerun that never
	// gets here: an adjudicated claim is not re-adjudicated.
	if by := orig.GetSupersededBy(); by != nil && by.GetClaimId() != c.GetId() {
		how := "replaced"
		if by.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
			how = "voided"
		}
		return Pend(PendOriginalNotFinalized, "original %s has already been %s", name, how)
	}
	// An original closed by the provider's correction was closed for one
	// claim. That claim passes; any other correction of it pends.
	closed := ClosedByProvider(orig)
	open, _, err := e.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1, Where: []store.Cond{store.Eq(orig.GetId(), "originalClaimId")}, Filter: func(o *claimsv1.Claim) bool {
		return o.GetId() != c.GetId() && o.GetOriginalClaimId() == orig.GetId() &&
			(closed || o.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_PENDED || o.GetStatus() == claimsv1.ClaimStatus_CLAIM_STATUS_RECEIVED)
	}})
	if err != nil {
		return err
	}
	if len(open) > 0 && closed {
		return Pend(PendOriginalNotFinalized, "original %s was already replaced or voided by the provider (%s)", name, displayNumber(open[0]))
	}
	if len(open) > 0 {
		return Pend(PendOpenReplacement, "original %s already has a replacement in progress (%s)", name, displayNumber(open[0]))
	}
	return nil
}

func displayNumber(c *claimsv1.Claim) string {
	if v := c.GetClaimVersion(); v > 1 {
		return fmt.Sprintf("%s-%d", c.GetClaimNumber(), v)
	}
	if c.GetClaimNumber() == "" {
		return c.GetId()
	}
	return c.GetClaimNumber()
}

// resolveMember asks Employer which member the claim is for, from the
// submitted fields only: the subscriber and, when sent apart, the patient.
// The member's customer becomes the claim's. Pend messages never quote an
// SSN.
func (e *DefaultEngine) resolveMember(ctx context.Context, c *claimsv1.Claim) (*eligibilityv1.Member, error) {
	sub, pt := claimPerson(c.GetSubmittedSubscriber()), claimPerson(c.GetSubmittedPatient())
	if sub == nil {
		sub = &employerv1.ClaimPerson{}
	}
	// member_card_id is the member number on every claim, whatever its
	// source; an 837 also carries it on the submitted subscriber.
	sub.MemberNumber = c.GetMemberCardId()
	res, err := e.employer().MatchClaimMember(ctx, &employerv1.MatchClaimMemberRequest{Subscriber: sub, Patient: pt})
	if err != nil {
		return nil, err
	}
	switch res.GetOutcome() {
	case employerv1.MemberMatchOutcome_MEMBER_MATCH_OUTCOME_MATCHED:
		return res.GetMember(), nil
	case employerv1.MemberMatchOutcome_MEMBER_MATCH_OUTCOME_AMBIGUOUS:
		return nil, Pend(PendMemberNotFound, "more than one member matches %s", submittedAs(c))
	}
	return nil, Pend(PendMemberNotFound, "no member matches %s", submittedAs(c))
}

// claimPerson is a submitted person as the Employer service takes them.
func claimPerson(p *claimsv1.SubmittedPerson) *employerv1.ClaimPerson {
	if p == nil {
		return nil
	}
	return &employerv1.ClaimPerson{MemberNumber: p.GetMemberNumber(), Ssn: p.GetSsn(), Name: p.GetName(),
		BirthDate: p.GetBirthDate(), GenderCode: p.GetGenderCode()}
}

// submittedAs describes who the claim named, for a pend message.
func submittedAs(c *claimsv1.Claim) string {
	if pt := c.GetSubmittedPatient(); pt != nil {
		return fmt.Sprintf("patient %s, born %s, under member id %q", pt.GetName().GetFirst(), dates.Format(pt.GetBirthDate()), c.GetMemberCardId())
	}
	return fmt.Sprintf("member id %q", c.GetMemberCardId())
}

// resolveCoverage finds the medical coverage span in force on the date of
// service, from the member's coverage on that date as we know it now.
//
// A member whose coverage ended before dos is denied (the member is
// liable). A member with no coverage yet pends, because enrollment files
// often arrive after the first claims.
func resolveCoverage(c *employerv1.GetMedicalCoverageResponse, dos *commonv1.Date) (*eligibilityv1.CoverageSpan, error) {
	if s := c.GetSpan(); s != nil && s.GetStatus() != eligibilityv1.CoverageStatus_COVERAGE_STATUS_TERMINATED {
		if s.GetStatus() == eligibilityv1.CoverageStatus_COVERAGE_STATUS_GRACE {
			return nil, Pend(PendCoverageInGrace, "coverage %s is in a premium grace period", s.GetId())
		}
		return s, nil
	}
	if c.GetStartedBefore() {
		return nil, &DenyError{
			Code:    DenyCoverageTerminated,
			Message: fmt.Sprintf("coverage ended before the date of service %s", dates.Format(dos)),
		}
	}
	return nil, Pend(PendNoEligibility, "member %s has no medical coverage on %s", c.GetMember().GetId(), dates.Format(dos))
}

func claimSignature(c *claimsv1.Claim) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|%s|%s", c.GetMemberId(), c.GetBillingProvider().GetNpi(), dates.Format(c.GetServiceDates().GetStart()))
	for _, l := range c.GetLines() {
		fmt.Fprintf(&b, "|%s:%d", l.GetProcedureCode(), l.GetCharge().GetCents())
	}
	return b.String()
}

// findDuplicate returns the id of a live claim with the same member,
// provider, date of service and lines.
func (e *DefaultEngine) findDuplicate(ctx context.Context, c *claimsv1.Claim) (string, error) {
	sig := claimSignature(c)
	if c.GetMemberId() == "" {
		return "", nil
	}
	// The member's other claims: the signature starts with the member.
	dups, _, err := e.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{
		PageSize: 1,
		Where:    []store.Cond{store.Eq(c.GetMemberId(), "memberId")},
		Filter: func(o *claimsv1.Claim) bool {
			if o.GetId() == c.GetId() || o.GetId() == c.GetOriginalClaimId() || o.GetOriginalClaimId() == c.GetId() {
				return false
			}
			if o.GetSupersededBy() != nil {
				return false
			}
			if o.GetFrequency() == claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID {
				return false
			}
			return claimSignature(o) == sig
		},
	})
	if err != nil || len(dups) == 0 {
		return "", err
	}
	return dups[0].GetId(), nil
}

// resolveProvider returns the provider for an NPI and, for the billed TIN
// on dos, where Network places them in the plan's network: the tier and
// the participation that put them there (neither when out-of-network, or
// when the plan has no network). An unknown provider is not an error: it
// is treated as non-contracted.
func (e *DefaultEngine) resolveProvider(ctx context.Context, ref *claimsv1.ProviderRef, networkID string, dos *commonv1.Date) (*providerv1.Provider, string, *providerv1.NetworkParticipation, error) {
	res, err := e.network().GetProvider(ctx, &networkv1.GetProviderRequest{Npi: ref.GetNpi(), Tin: ref.GetTin(), On: dos, NetworkId: networkID})
	if err != nil || res.GetProvider() == nil {
		return nil, "", nil, err
	}
	p := res.GetProvider()
	if p.GetStatus() == providerv1.ProviderStatus_PROVIDER_STATUS_SANCTIONED {
		return nil, "", nil, &DenyError{Code: DenyProviderSanctioned, Message: "provider is excluded from payment"}
	}
	return p, res.GetTierId(), res.GetParticipation(), nil
}

// selectTier is the plan's tier for the network tier a provider falls in:
// the tier with that id, or the plan's out-of-network tier when the
// provider is in none. Nil if the plan has no such tier.
func selectTier(v *planv1.PlanVersion, tierID string) *planv1.NetworkTier {
	for _, t := range v.GetNetworkTiers() {
		if tierID == "" && t.GetIsOutOfNetwork() || tierID != "" && !t.GetIsOutOfNetwork() && t.GetId() == tierID {
			return t
		}
	}
	return nil
}

func inRanges(code string, ranges []*planv1.CodeRange) bool {
	for _, r := range ranges {
		if code >= r.GetStart() && code <= r.GetEnd() {
			return true
		}
	}
	return false
}

// matchBenefit returns the first service category (by priority) whose
// criteria all match the line, and its benefit.
func matchBenefit(v *planv1.PlanVersion, c *claimsv1.Claim, line *claimsv1.ClaimLine) (*planv1.ServiceCategory, *planv1.Benefit) {
	cats := slices.Clone(v.GetServiceCategories())
	sort.SliceStable(cats, func(i, j int) bool { return cats[i].GetPriority() < cats[j].GetPriority() })
	for _, cat := range cats {
		if len(cat.GetProcedureCodes()) > 0 && !inRanges(line.GetProcedureCode(), cat.GetProcedureCodes()) {
			continue
		}
		if len(cat.GetRevenueCodes()) > 0 && !inRanges(line.GetRevenueCode(), cat.GetRevenueCodes()) {
			continue
		}
		if len(cat.GetPlaceOfServiceCodes()) > 0 && !slices.Contains(cat.GetPlaceOfServiceCodes(), line.GetPlaceOfServiceCode()) {
			continue
		}
		if len(cat.GetProviderTaxonomyCodes()) > 0 && !slices.Contains(cat.GetProviderTaxonomyCodes(), c.GetRenderingProvider().GetTaxonomyCode()) {
			continue
		}
		if len(cat.GetDiagnosisCodes()) > 0 && !slices.ContainsFunc(c.GetDiagnosisCodes(), func(dx string) bool {
			return inRanges(dx, cat.GetDiagnosisCodes())
		}) {
			continue
		}
		for _, b := range v.GetBenefits() {
			if b.GetServiceCategoryId() == cat.GetId() {
				return cat, b
			}
		}
	}
	return nil, nil
}

func pct(amount int64, bps int32) int64 { return (amount*int64(bps) + 5000) / 10000 }

func (e *DefaultEngine) feeFor(ctx context.Context, scheduleID string, line *claimsv1.ClaimLine) (int64, bool, error) {
	fs, err := e.Repos.FeeSchedules.Get(ctx, scheduleID)
	if errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	mod := ""
	if len(line.GetModifiers()) > 0 {
		mod = line.GetModifiers()[0]
	}
	var base *providerv1.FeeScheduleEntry
	for _, en := range fs.GetEntries() {
		if en.GetProcedureCode() != line.GetProcedureCode() {
			continue
		}
		if en.GetModifier() == mod {
			base = en
			break
		}
		if en.GetModifier() == "" {
			base = en
		}
	}
	if base == nil {
		return 0, false, nil
	}
	units := line.GetUnitsMilli()
	if units == 0 {
		units = 1000
	}
	return (base.GetAllowed().GetCents()*units + 500) / 1000, true, nil
}

// price determines the allowed amount: from the provider's contract when
// there is a participation, otherwise from the tier's non-contracted
// basis. Allowed never exceeds the charge.
func (e *DefaultEngine) price(ctx context.Context, line *claimsv1.ClaimLine, tier *planv1.NetworkTier, part *providerv1.NetworkParticipation) (*claimsv1.Pricing, error) {
	charge := line.GetCharge().GetCents()
	done := func(p *claimsv1.Pricing, allowed int64) (*claimsv1.Pricing, error) {
		p.Allowed = money(min(allowed, charge))
		return p, nil
	}

	if part == nil {
		basis := tier.GetNonContractedBasis()
		p := &claimsv1.Pricing{Source: claimsv1.PricingSource_PRICING_SOURCE_NON_CONTRACTED, NonContractedBasis: basis}
		switch basis.GetMethod() {
		case planv1.ReimbursementMethod_REIMBURSEMENT_METHOD_PERCENT_OF_BILLED:
			return done(p, pct(charge, basis.GetPercentBps()))
		case planv1.ReimbursementMethod_REIMBURSEMENT_METHOD_PERCENT_OF_MEDICARE:
			fee, ok, err := e.feeFor(ctx, MedicareFeeScheduleID, line)
			if err != nil {
				return nil, err
			}
			if ok {
				p.FeeScheduleId = MedicareFeeScheduleID
				return done(p, pct(fee, basis.GetPercentBps()))
			}
		}
		return nil, Pend(PendNoPrice, "no non-contracted rate for procedure %s", line.GetProcedureCode())
	}

	cres, err := e.network().GetContract(ctx, &networkv1.GetContractRequest{ContractId: part.GetContractId()})
	if network.NotFound(err) {
		return nil, Pend(PendNoPrice, "contract %s not found", part.GetContractId())
	} else if err != nil {
		return nil, err
	}
	contract := cres.GetContract()
	for i, term := range contract.GetTerms() {
		if len(term.GetProcedureCodes()) > 0 && !slices.Contains(term.GetProcedureCodes(), line.GetProcedureCode()) {
			continue
		}
		if len(term.GetPlaceOfServiceCodes()) > 0 && !slices.Contains(term.GetPlaceOfServiceCodes(), line.GetPlaceOfServiceCode()) {
			continue
		}
		p := &claimsv1.Pricing{
			Source:     claimsv1.PricingSource_PRICING_SOURCE_CONTRACT,
			ContractId: contract.GetId(), ContractTermIndex: int32(i), ContractMethod: term.GetMethod(),
		}
		switch term.GetMethod() {
		case providerv1.ContractMethod_CONTRACT_METHOD_FEE_SCHEDULE:
			fee, ok, err := e.feeFor(ctx, term.GetFeeScheduleId(), line)
			if err != nil {
				return nil, err
			}
			if ok {
				p.FeeScheduleId = term.GetFeeScheduleId()
				return done(p, fee)
			}
		case providerv1.ContractMethod_CONTRACT_METHOD_PERCENT_OF_BILLED:
			return done(p, pct(charge, term.GetPercentBps()))
		case providerv1.ContractMethod_CONTRACT_METHOD_PERCENT_OF_MEDICARE:
			fee, ok, err := e.feeFor(ctx, MedicareFeeScheduleID, line)
			if err != nil {
				return nil, err
			}
			if ok {
				p.FeeScheduleId = MedicareFeeScheduleID
				return done(p, pct(fee, term.GetPercentBps()))
			}
		case providerv1.ContractMethod_CONTRACT_METHOD_FLAT_RATE:
			return done(p, term.GetRate().GetCents())
		}
	}
	return nil, Pend(PendNoPrice, "no term of contract %s prices procedure %s", contract.GetId(), line.GetProcedureCode())
}

type accumulatorRef struct {
	id, ownerID string
}

// remaining returns how much room is left on the accumulators of one type
// that apply to this member in this tier, and which balances cost sharing
// counts toward.
//
// With both an individual and a family accumulator: embedded means the
// member is done when either their own or the family limit is reached;
// non-embedded means only the family total matters.
func remaining(v *planv1.PlanVersion, typ planv1.AccumulatorType, tierID string, m *eligibilityv1.Member, work *claimsv1.FamilyAccumulators) (int64, []accumulatorRef) {
	var ind, fam *planv1.Accumulator
	for _, a := range v.GetAccumulators() {
		if a.GetType() != typ || !slices.Contains(a.GetTierIds(), tierID) {
			continue
		}
		switch a.GetLevel() {
		case planv1.AccumulatorLevel_ACCUMULATOR_LEVEL_INDIVIDUAL:
			ind = a
		case planv1.AccumulatorLevel_ACCUMULATOR_LEVEL_FAMILY:
			fam = a
		}
	}
	var refs []accumulatorRef
	left := func(a *planv1.Accumulator, owner string) int64 {
		refs = append(refs, accumulatorRef{a.GetId(), owner})
		return max(0, a.GetLimit().GetCents()-Applied(work, a.GetId(), owner))
	}
	switch {
	case ind != nil && fam != nil:
		indLeft, famLeft := left(ind, m.GetId()), left(fam, m.GetSubscriberId())
		if fam.GetEmbedded() {
			return min(indLeft, famLeft), refs
		}
		return famLeft, refs
	case ind != nil:
		return left(ind, m.GetId()), refs
	case fam != nil:
		return left(fam, m.GetSubscriberId()), refs
	}
	if typ == planv1.AccumulatorType_ACCUMULATOR_TYPE_DEDUCTIBLE {
		return 0, nil // no deductible in this tier
	}
	return math.MaxInt64, nil // no out-of-pocket maximum in this tier
}

// applyCostShare splits allowed into deductible, copay, coinsurance and
// plan paid, capped by the out-of-pocket maximum. It fills la, adds the
// amounts to the working accumulator copy and returns the postings.
//
// Order: deductible, then copay, then coinsurance on what is left. The
// deductible counts toward the out-of-pocket maximum.
func applyCostShare(la *claimsv1.LineAdjudication, cs *planv1.CostShare, v *planv1.PlanVersion, tierID string, m *eligibilityv1.Member, work *claimsv1.FamilyAccumulators, lineNumber int32) []*claimsv1.AccumulatorPosting {
	allowed := la.GetPricing().GetAllowed().GetCents()
	dedLeft, dedAccs := remaining(v, planv1.AccumulatorType_ACCUMULATOR_TYPE_DEDUCTIBLE, tierID, m, work)
	oopLeft, oopAccs := remaining(v, planv1.AccumulatorType_ACCUMULATOR_TYPE_OUT_OF_POCKET, tierID, m, work)

	rest := allowed
	var ded, copay, coins int64
	if cs.GetDeductibleApplies() {
		ded = min(rest, dedLeft)
		rest -= ded
	}
	copay = min(cs.GetCopay().GetCents(), rest)
	rest -= copay
	coins = pct(rest, cs.GetCoinsuranceBps())

	// Out-of-pocket cap: trim coinsurance first, then copay, then
	// deductible.
	if over := ded + copay + coins - oopLeft; over > 0 {
		for _, part := range []*int64{&coins, &copay, &ded} {
			cut := min(over, *part)
			*part -= cut
			over -= cut
		}
	}

	totals := map[accumulatorRef]int64{}
	add := func(typ claimsv1.CostShareType, amount int64, accs ...[]accumulatorRef) {
		if amount == 0 {
			return
		}
		applied := &claimsv1.CostShareApplied{Type: typ, Amount: money(amount)}
		for _, group := range accs {
			for _, ref := range group {
				applied.AccumulatorIds = append(applied.AccumulatorIds, ref.id)
				totals[ref] += amount
			}
		}
		la.CostShares = append(la.CostShares, applied)
	}
	add(claimsv1.CostShareType_COST_SHARE_TYPE_DEDUCTIBLE, ded, dedAccs, oopAccs)
	add(claimsv1.CostShareType_COST_SHARE_TYPE_COPAY, copay, oopAccs)
	add(claimsv1.CostShareType_COST_SHARE_TYPE_COINSURANCE, coins, oopAccs)

	member := ded + copay + coins
	la.MemberResponsibility = money(member)
	la.Paid = money(allowed - member)

	var postings []*claimsv1.AccumulatorPosting
	for _, ref := range append(slices.Clone(dedAccs), oopAccs...) {
		amount, ok := totals[ref]
		if !ok {
			continue
		}
		delete(totals, ref)
		bump(work, ref.id, ref.ownerID, amount, nil)
		postings = append(postings, &claimsv1.AccumulatorPosting{
			AccumulatorId: ref.id, OwnerId: ref.ownerID, LineNumber: lineNumber, Amount: money(amount),
		})
	}
	return postings
}

// DeriveCAS produces the 835 CAS adjustments for a payable line from its
// breakdown: CO-45 for the contractual write-off, PR-1/2/3 for
// deductible, coinsurance and copay.
func DeriveCAS(la *claimsv1.LineAdjudication) []*claimsv1.CasAdjustment {
	var out []*claimsv1.CasAdjustment
	if w := la.GetCharge().GetCents() - la.GetPricing().GetAllowed().GetCents(); w > 0 {
		out = append(out, &claimsv1.CasAdjustment{Group: claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL, ReasonCode: "45", Amount: money(w)})
	}
	codes := map[claimsv1.CostShareType]string{
		claimsv1.CostShareType_COST_SHARE_TYPE_DEDUCTIBLE:  "1",
		claimsv1.CostShareType_COST_SHARE_TYPE_COINSURANCE: "2",
		claimsv1.CostShareType_COST_SHARE_TYPE_COPAY:       "3",
	}
	for _, cs := range la.GetCostShares() {
		out = append(out, &claimsv1.CasAdjustment{
			Group: claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY, ReasonCode: codes[cs.GetType()], Amount: cs.GetAmount(),
		})
	}
	return out
}

func deniedLine(l *claimsv1.ClaimLine, code, message string) *claimsv1.LineAdjudication {
	r, ok := denials[code]
	if !ok {
		panic(fmt.Sprintf("adjudication: denial %s has no 835 reporting in denials", code))
	}
	group := claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL
	if r.MemberLiable {
		group = claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY
	}
	return &claimsv1.LineAdjudication{
		Status: claimsv1.LineStatus_LINE_STATUS_DENIED,
		Denial: &claimsv1.Denial{Code: code, Message: message, MemberLiable: r.MemberLiable},
		Charge: l.GetCharge(),
		Paid:   money(0),
		CasAdjustments: []*claimsv1.CasAdjustment{
			{Group: group, ReasonCode: r.CARC, Amount: l.GetCharge()},
		},
		RemarkCodes: slices.Clone(r.Remarks),
	}
}

func overridden(c *claimsv1.Claim, code string, line int32) bool {
	if !overridable[code] {
		return false
	}
	for _, o := range c.GetPendOverrides() {
		if o.GetCode() == code && o.GetLineNumber() == line {
			return true
		}
	}
	return false
}

func money(cents int64) *commonv1.Money { return &commonv1.Money{Cents: cents} }
