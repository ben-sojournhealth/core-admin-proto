// Package sim is a sandbox around the claims service: seeded data, a
// deterministic clock, a plain-language event log, and one Action type
// that drives everything. The demo UI and the scenario library both speak
// Actions, so anything clickable is also scriptable and testable.
package sim

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/adjudication"
	"coreadmin/internal/claims"
	"coreadmin/internal/dates"
	"coreadmin/internal/repo"
	"coreadmin/internal/seed"
	"coreadmin/internal/store"
	seeddata "coreadmin/seed"
)

const (
	CustomerID      = "cust_acme"
	PlanID          = "plan_acme_ppo"
	SubscriberID    = "mem_1001"
	DefaultProvider = "1234567893" // Dr. Chen, in-network
	DefaultDOS      = "2026-03-10"
	FeeScheduleID   = "fs_ppo_2026"
)

// Action is one thing that can happen to the system.
type Action struct {
	// process, race, reprocess, adjust, void, select, release, pull,
	// releaseHold, clearFlag, payment, setCoverage, setFee.
	Type string `json:"type"`

	// process / adjust (the corrected fields; empty keeps the original).
	Claim *ClaimSpec `json:"claim,omitempty"`
	// race.
	Claims []*ClaimSpec `json:"claims,omitempty"`

	ClaimID string `json:"claimId,omitempty"`
	DryRun  bool   `json:"dryRun,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// reprocess: pends the examiner clears.
	Resolve []Resolve `json:"resolve,omitempty"`
	// payment: clear, return or reissue.
	Event string `json:"event,omitempty"`

	// setCoverage: sets the member's medical coverage dates (YYYY-MM-DD;
	// empty End is open-ended).
	MemberID string `json:"memberId,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`

	// setFee: sets the contracted rate for a procedure.
	Procedure string `json:"procedure,omitempty"`
	Cents     int64  `json:"cents,omitempty"`
}

type Resolve struct {
	Code string `json:"code"`
	Line int32  `json:"line"`
}

// ClaimSpec is a compact one-line professional claim.
type ClaimSpec struct {
	ID        string `json:"id,omitempty"`
	Member    string `json:"member,omitempty"`
	Provider  string `json:"provider,omitempty"` // NPI
	Procedure string `json:"procedure,omitempty"`
	POS       string `json:"pos,omitempty"`
	DOS       string `json:"dos,omitempty"`
	Charge    int64  `json:"charge,omitempty"` // cents
	Auth      string `json:"auth,omitempty"`
}

// Result is what one Action did.
type Result struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	// Set for dry runs: the projected claim.
	Preview *ClaimView `json:"preview,omitempty"`
	// Set for adjustments.
	Delta *DeltaView `json:"delta,omitempty"`
	// The claim the action produced or changed, when there is one (for a
	// replacement or void, the new claim).
	ClaimID string `json:"claimId,omitempty"`
}

type LogEntry struct {
	N     int      `json:"n"`
	Title string   `json:"title"`
	Lines []string `json:"lines"`
	Error string   `json:"error,omitempty"`
}

type Sim struct {
	Repos  *repo.Repos
	Claims *claims.Service

	// ShortIDs gives new original claims readable ids (clm_001, ...) and
	// names later versions clm_001-2, clm_001-3, as the claim number does.
	// The ids of replacements and voids are always UUIDv7s; the short
	// names resolve to them (see Resolve). With ShortIDs off, every claim
	// gets a UUIDv7 and is named by its claim number.
	ShortIDs bool

	clock  time.Time
	log    []*LogEntry
	cur    *LogEntry
	nextID int
}

// New returns a freshly seeded sandbox.
func New() (*Sim, error) {
	ctx := context.Background()
	s := &Sim{Repos: repo.NewInMemory(), clock: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
	if _, err := seed.Load(ctx, s.Repos, seeddata.FS); err != nil {
		return nil, err
	}
	// The sandbox starts with no claims.
	seeded, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000})
	if err != nil {
		return nil, err
	}
	for _, c := range seeded {
		if err := s.Repos.Claims.Delete(ctx, c.GetId()); err != nil {
			return nil, err
		}
	}
	ledger := &adjudication.StoreLedger{Records: s.Repos.FamilyAccumulators, Now: s.now}
	engine := &adjudication.DefaultEngine{Repos: s.Repos, Ledger: ledger, Now: s.now, Version: "sandbox-1"}
	s.Claims = &claims.Service{Repos: s.Repos, Engine: engine, Ledger: ledger, Now: s.now, Logf: s.logf}
	s.UseShortIDs(true)
	return s, nil
}

// UseShortIDs switches between short names (scenario scripts) and claim
// numbers (Journey) for naming claims.
func (s *Sim) UseShortIDs(on bool) {
	s.ShortIDs = on
	s.Claims.Ref = nil
	if on {
		s.Claims.Ref = s.shortName
	}
}

// shortName is the original's id plus the version suffix: clm_A, clm_A-2.
func (s *Sim) shortName(c *claimsv1.Claim) string {
	if c.GetClaimVersion() <= 1 || c.GetClaimNumber() == "" {
		return c.GetId()
	}
	if root := s.version(c.GetClaimNumber(), 1); root != nil {
		return fmt.Sprintf("%s-%d", root.GetId(), c.GetClaimVersion())
	}
	return claims.DisplayNumber(c)
}

func (s *Sim) version(number string, v int32) *claimsv1.Claim {
	found, _, _ := s.Repos.Claims.List(context.Background(), store.ListOptions[*claimsv1.Claim]{PageSize: 1, Filter: func(o *claimsv1.Claim) bool {
		return o.GetClaimNumber() == number && max(o.GetClaimVersion(), 1) == v
	}})
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

var versioned = regexp.MustCompile(`^(.+)-(\d+)$`)

// Resolve turns a claim reference into an internal id. A reference is an
// id, a short name with a version (clm_A-2), or a claim number with or
// without a version (26075-A-00001-2). Unknown references come back
// unchanged, so the caller reports "no claim".
func (s *Sim) Resolve(ref string) string {
	ctx := context.Background()
	if _, err := s.Repos.Claims.Get(ctx, ref); err == nil {
		return ref
	}
	if c := s.version(ref, 1); c != nil {
		return c.GetId()
	}
	if m := versioned.FindStringSubmatch(ref); m != nil {
		v, _ := strconv.Atoi(m[2])
		number := m[1]
		if root, err := s.Repos.Claims.Get(ctx, m[1]); err == nil {
			number = root.GetClaimNumber()
		}
		if c := s.version(number, int32(v)); c != nil {
			return c.GetId()
		}
	}
	return ref
}

// now is the sandbox clock: it moves forward one second per reading so
// events have a strict order.
func (s *Sim) now() time.Time {
	s.clock = s.clock.Add(time.Second)
	return s.clock
}

func (s *Sim) logf(format string, args ...any) {
	if s.cur != nil {
		s.cur.Lines = append(s.cur.Lines, fmt.Sprintf(format, args...))
	}
}

// Now is the sandbox's current time.
func (s *Sim) Now() time.Time { return s.clock }

// Advance moves the sandbox clock forward, e.g. to spread claims over days.
func (s *Sim) Advance(d time.Duration) { s.clock = s.clock.Add(d) }

// SetClock sets the sandbox clock.
func (s *Sim) SetClock(t time.Time) { s.clock = t }

// Log returns the event log, oldest first.
func (s *Sim) Log() []*LogEntry { return s.log }

// Apply runs one action. A rejected action is not an error: it comes back
// with OK false and the reason, and is recorded in the log.
func (s *Sim) Apply(a Action) *Result {
	ctx := store.WithActor(context.Background(), "examiner")
	entry := &LogEntry{N: len(s.log) + 1, Lines: []string{}}
	s.log = append(s.log, entry)
	s.cur = entry
	defer func() { s.cur = nil }()

	res := &Result{}
	err := s.apply(ctx, a, entry, res)
	res.Title, res.Lines = entry.Title, entry.Lines
	if err != nil {
		msg := strings.TrimPrefix(err.Error(), claims.ErrRejected.Error()+": ")
		if !errors.Is(err, claims.ErrRejected) {
			msg = "internal error: " + msg
		}
		entry.Error, res.Error = msg, msg
		return res
	}
	res.OK = true
	return res
}

// Do runs something outside the Action vocabulary (e.g. loading a file)
// as one logged event with the given title.
func (s *Sim) Do(actor, title string, f func(ctx context.Context) error) *Result {
	ctx := store.WithActor(context.Background(), actor)
	entry := &LogEntry{N: len(s.log) + 1, Title: title, Lines: []string{}}
	s.log = append(s.log, entry)
	s.cur = entry
	defer func() { s.cur = nil }()
	res := &Result{Title: title}
	if err := f(ctx); err != nil {
		entry.Error, res.Error = err.Error(), err.Error()
	} else {
		res.OK = true
	}
	res.Lines = entry.Lines
	return res
}

func (s *Sim) apply(ctx context.Context, a Action, entry *LogEntry, res *Result) error {
	dry := map[bool]string{true: " (dry run)", false: ""}[a.DryRun]
	if a.ClaimID != "" {
		a.ClaimID = s.Resolve(a.ClaimID)
	}
	switch a.Type {
	case "process":
		c, err := s.buildClaim(ctx, a.Claim)
		if err != nil {
			entry.Title = "Process claim"
			return err
		}
		entry.Title = "Process " + s.claimLabel(c) + dry
		r, err := s.Claims.ProcessClaim(ctx, &claimsv1.ProcessClaimRequest{Claim: c, DryRun: a.DryRun})
		if err != nil {
			return err
		}
		if !a.DryRun {
			entry.Title = "Process " + s.claimLabel(r.GetClaim())
			res.ClaimID = r.GetClaim().GetId()
		}
		if a.DryRun {
			res.Preview = s.claimView(r.GetClaim())
		}

	case "race":
		entry.Title = fmt.Sprintf("Process %d claims at once", len(a.Claims))
		var cs []*claimsv1.Claim
		for _, spec := range a.Claims {
			c, err := s.buildClaim(ctx, spec)
			if err != nil {
				return err
			}
			cs = append(cs, c)
		}
		_, err := s.Claims.ProcessConcurrently(ctx, cs)
		return err

	case "reprocess":
		entry.Title = "Reprocess " + s.label(a.ClaimID) + dry
		res.ClaimID = a.ClaimID
		req := &claimsv1.ReprocessClaimRequest{ClaimId: a.ClaimID, Reason: a.Reason, DryRun: a.DryRun}
		for _, r := range a.Resolve {
			req.Resolutions = append(req.Resolutions, &claimsv1.PendResolution{Code: r.Code, LineNumber: r.Line, Note: a.Reason})
		}
		r, err := s.Claims.ReprocessClaim(ctx, req)
		if err != nil {
			return err
		}
		if a.DryRun {
			res.Preview = s.claimView(r.GetClaim())
		}

	case "adjust":
		entry.Title = "Adjust " + s.label(a.ClaimID) + dry
		req := &claimsv1.AdjustClaimRequest{OriginalClaimId: a.ClaimID, Reason: a.Reason, DryRun: a.DryRun}
		if a.Claim != nil {
			orig, err := s.Repos.Claims.Get(ctx, a.ClaimID)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: no claim %s", claims.ErrRejected, a.ClaimID)
			} else if err != nil {
				return err
			}
			if req.CorrectedClaim, err = s.correct(ctx, orig, a.Claim); err != nil {
				return err
			}
		}
		r, err := s.Claims.AdjustClaim(ctx, req)
		if err != nil {
			return err
		}
		res.Delta = s.deltaView(r)
		if !a.DryRun {
			res.ClaimID = r.GetClaim().GetId()
		}
		if a.DryRun {
			res.Preview = s.claimView(r.GetClaim())
			if n := len(r.GetFlaggedClaimIds()); n > 0 {
				s.logf("Would flag %d later claim(s) for review: %s.", n, strings.Join(s.labels(r.GetFlaggedClaimIds()), ", "))
			}
		}

	case "void":
		entry.Title = "Void " + s.label(a.ClaimID)
		r, err := s.Claims.VoidClaim(ctx, &claimsv1.VoidClaimRequest{ClaimId: a.ClaimID, Reason: a.Reason})
		if err != nil {
			return err
		}
		res.ClaimID = r.GetVoidClaim().GetId()

	case "select":
		entry.Title = "Payment selection"
		var ids []string
		if a.ClaimID != "" {
			ids = []string{a.ClaimID}
			entry.Title = "Select " + s.label(a.ClaimID) + " for payment"
		}
		_, err := s.Claims.SelectForPayment(ctx, ids)
		return err

	case "release":
		entry.Title = "Release payment run"
		_, err := s.Claims.ReleasePaymentRun(ctx)
		return err

	case "pull":
		entry.Title = "Pull " + s.label(a.ClaimID) + " from payment"
		_, err := s.Claims.PullClaimFromPayment(ctx, &claimsv1.PullClaimFromPaymentRequest{ClaimId: a.ClaimID, Reason: a.Reason})
		return err

	case "releaseHold":
		entry.Title = "Release hold on " + s.label(a.ClaimID)
		_, err := s.Claims.ReleasePaymentHold(ctx, &claimsv1.ReleasePaymentHoldRequest{ClaimId: a.ClaimID, Note: a.Reason})
		return err

	case "clearFlag":
		entry.Title = "Clear flag on " + s.label(a.ClaimID)
		_, err := s.Claims.ClearReviewFlag(ctx, &claimsv1.ClearReviewFlagRequest{ClaimId: a.ClaimID, Note: a.Reason})
		return err

	case "payment":
		entry.Title = fmt.Sprintf("Bank: %s %s", s.label(a.ClaimID), a.Event)
		_, err := s.Claims.RecordPaymentEvent(ctx, a.ClaimID, a.Event)
		return err

	case "setCoverage":
		entry.Title = "Eligibility update"
		return s.setCoverage(ctx, a)

	case "setFee":
		entry.Title = "Fee schedule update"
		return s.setFee(ctx, a)

	default:
		entry.Title = "Unknown action"
		return fmt.Errorf("%w: unknown action type %q", claims.ErrRejected, a.Type)
	}
	return nil
}

func (s *Sim) member(ctx context.Context, id string) (*eligibilityv1.Member, error) {
	m, err := s.Repos.Members.Get(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("%w: no member %s", claims.ErrRejected, id)
	}
	return m, err
}

func (s *Sim) provider(ctx context.Context, npi string) (*providerv1.Provider, error) {
	ps, _, err := s.Repos.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{
		PageSize: 1, Filter: func(p *providerv1.Provider) bool { return p.GetNpi() == npi },
	})
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("%w: no provider with NPI %s", claims.ErrRejected, npi)
	}
	return ps[0], nil
}

func providerName(p *providerv1.Provider) string {
	if p.GetOrganizationName() != "" {
		return p.GetOrganizationName()
	}
	return "Dr. " + p.GetName().GetLast()
}

// buildClaim expands a ClaimSpec into a full one-line professional claim.
func (s *Sim) buildClaim(ctx context.Context, spec *ClaimSpec) (*claimsv1.Claim, error) {
	if spec == nil {
		return nil, fmt.Errorf("%w: no claim given", claims.ErrRejected)
	}
	sp := *spec
	if sp.ID == "" && s.ShortIDs {
		for {
			s.nextID++
			sp.ID = fmt.Sprintf("clm_%03d", s.nextID)
			if _, err := s.Repos.Claims.Get(ctx, sp.ID); errors.Is(err, store.ErrNotFound) {
				break
			}
		}
	}
	if sp.Member == "" {
		sp.Member = SubscriberID
	}
	if sp.Provider == "" {
		sp.Provider = DefaultProvider
	}
	if sp.POS == "" {
		sp.POS = "11"
	}
	if sp.DOS == "" {
		sp.DOS = DefaultDOS
	}
	if sp.Procedure == "" || sp.Charge <= 0 {
		return nil, fmt.Errorf("%w: a claim needs a procedure code and a charge", claims.ErrRejected)
	}
	m, err := s.member(ctx, sp.Member)
	if err != nil {
		return nil, err
	}
	c := &claimsv1.Claim{
		Id:             sp.ID,
		CustomerId:     CustomerID,
		Type:           claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL,
		Frequency:      claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL,
		MemberCardId:   m.GetCardId(),
		MemberId:       m.GetId(),
		Source:         claimsv1.ClaimSource_CLAIM_SOURCE_API,
		Lines:          []*claimsv1.ClaimLine{{LineNumber: 1, UnitsMilli: 1000, DiagnosisPointers: []int32{1}}},
		DiagnosisCodes: []string{"M17.11"},
	}
	return c, s.fill(ctx, c, &sp)
}

// correct returns a copy of orig with the spec's non-empty fields applied.
func (s *Sim) correct(ctx context.Context, orig *claimsv1.Claim, spec *ClaimSpec) (*claimsv1.Claim, error) {
	c := proto.Clone(orig).(*claimsv1.Claim)
	c.Id = spec.ID
	line := c.GetLines()[0]
	sp := ClaimSpec{
		Provider:  c.GetBillingProvider().GetNpi(),
		Procedure: line.GetProcedureCode(),
		POS:       line.GetPlaceOfServiceCode(),
		DOS:       dates.Format(c.GetServiceDates().GetStart()),
		Charge:    line.GetCharge().GetCents(),
	}
	if len(c.GetAuthorizationNumbers()) > 0 {
		sp.Auth = c.GetAuthorizationNumbers()[0]
	}
	if spec.Member != "" && spec.Member != c.GetMemberId() {
		m, err := s.member(ctx, spec.Member)
		if err != nil {
			return nil, err
		}
		c.MemberId, c.MemberCardId = m.GetId(), m.GetCardId()
	}
	if spec.Provider != "" {
		sp.Provider = spec.Provider
	}
	if spec.Procedure != "" {
		sp.Procedure = spec.Procedure
	}
	if spec.POS != "" {
		sp.POS = spec.POS
	}
	if spec.DOS != "" {
		sp.DOS = spec.DOS
	}
	if spec.Charge > 0 {
		sp.Charge = spec.Charge
	}
	if spec.Auth != "" {
		sp.Auth = spec.Auth
	}
	return c, s.fill(ctx, c, &sp)
}

// fill sets the provider, dates, line and authorization from a spec.
func (s *Sim) fill(ctx context.Context, c *claimsv1.Claim, sp *ClaimSpec) error {
	p, err := s.provider(ctx, sp.Provider)
	if err != nil {
		return err
	}
	dos, err := dates.Parse(sp.DOS)
	if err != nil {
		return fmt.Errorf("%w: %v", claims.ErrRejected, err)
	}
	tin := ""
	if len(p.GetTins()) > 0 {
		tin = p.GetTins()[0].GetTin()
	}
	taxonomy := ""
	if len(p.GetTaxonomyCodes()) > 0 {
		taxonomy = p.GetTaxonomyCodes()[0]
	}
	c.BillingProvider = &claimsv1.ProviderRef{Npi: p.GetNpi(), Tin: tin, Name: providerName(p)}
	c.RenderingProvider = &claimsv1.ProviderRef{Npi: p.GetNpi(), TaxonomyCode: taxonomy}
	c.ServiceDates = &commonv1.DateRange{Start: dos, End: dos}
	c.TotalCharge = &commonv1.Money{Cents: sp.Charge}
	c.AuthorizationNumbers = nil
	if sp.Auth != "" {
		c.AuthorizationNumbers = []string{sp.Auth}
	}
	line := c.GetLines()[0]
	line.ProcedureCode = sp.Procedure
	line.PlaceOfServiceCode = sp.POS
	line.ServiceDates = c.GetServiceDates()
	line.Charge = c.GetTotalCharge()
	return nil
}

func (s *Sim) claimLabel(c *claimsv1.Claim) string {
	line := c.GetLines()[0]
	name := c.GetId()
	if c.GetClaimNumber() != "" {
		name = s.Claims.Label(c)
	}
	return fmt.Sprintf("%s (%s, %s)", name, s.memberName(c.GetMemberId()), line.GetProcedureCode())
}

func (s *Sim) labels(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		out = append(out, s.label(id))
	}
	return out
}

// label names a stored claim the way this sandbox's log does.
func (s *Sim) label(id string) string {
	c, err := s.Repos.Claims.Get(context.Background(), id)
	if err != nil {
		return id
	}
	return s.Claims.Label(c)
}

func (s *Sim) memberName(id string) string {
	m, err := s.Repos.Members.Get(context.Background(), id)
	if err != nil {
		return id
	}
	return m.GetName().GetFirst()
}

// setCoverage is the sandbox's stand-in for an enrollment file: it sets
// the dates of a member's medical coverage, adding a span if there is
// none.
func (s *Sim) setCoverage(ctx context.Context, a Action) error {
	m, err := s.member(ctx, a.MemberID)
	if err != nil {
		return err
	}
	start, err := dates.Parse(a.Start)
	if err != nil {
		return fmt.Errorf("%w: %v", claims.ErrRejected, err)
	}
	eff := &commonv1.DateRange{Start: start}
	status := eligibilityv1.CoverageStatus_COVERAGE_STATUS_ACTIVE
	desc := "from " + a.Start
	if a.End != "" {
		if eff.End, err = dates.Parse(a.End); err != nil {
			return fmt.Errorf("%w: %v", claims.ErrRejected, err)
		}
		desc = a.Start + " to " + a.End
	}
	var span *eligibilityv1.CoverageSpan
	for _, sp := range m.GetCoverage() {
		if sp.GetLine() == eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL {
			span = sp
		}
	}
	if span == nil {
		span = &eligibilityv1.CoverageSpan{
			Id: "cov_" + strings.TrimPrefix(m.GetId(), "mem_") + "_med_2026", PlanId: PlanID, GroupId: "grp_acme_main",
			Line: eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL, Tier: eligibilityv1.CoverageTier_COVERAGE_TIER_FAMILY,
		}
		m.Coverage = append(m.Coverage, span)
	}
	span.Effective, span.Status = eff, status
	if _, err := s.Repos.Members.Update(ctx, m); err != nil {
		return err
	}
	s.logf("%s's medical coverage is now %s. Existing claims are not changed by this.", m.GetName().GetFirst(), desc)
	return nil
}

// setFee changes a contracted rate, as a corrected fee schedule load
// would.
func (s *Sim) setFee(ctx context.Context, a Action) error {
	fs, err := s.Repos.FeeSchedules.Get(ctx, FeeScheduleID)
	if err != nil {
		return err
	}
	if a.Procedure == "" || a.Cents <= 0 {
		return fmt.Errorf("%w: a procedure code and a rate are required", claims.ErrRejected)
	}
	old := "none"
	found := false
	for _, e := range fs.GetEntries() {
		if e.GetProcedureCode() == a.Procedure && e.GetModifier() == "" {
			old = claims.USD(e.GetAllowed().GetCents())
			e.Allowed = &commonv1.Money{Cents: a.Cents}
			found = true
		}
	}
	if !found {
		fs.Entries = append(fs.Entries, &providerv1.FeeScheduleEntry{ProcedureCode: a.Procedure, Allowed: &commonv1.Money{Cents: a.Cents}})
	}
	if _, err := s.Repos.FeeSchedules.Update(ctx, fs); err != nil {
		return err
	}
	s.logf("Contracted rate for %s changed from %s to %s. Existing claims are not changed by this.", a.Procedure, old, claims.USD(a.Cents))
	return nil
}
