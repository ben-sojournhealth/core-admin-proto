// Package sim is a sandbox around the claims service: seeded data, a
// deterministic clock, a plain-language event log, and one Action type
// that drives everything. The demo UI and the scenario library both speak
// Actions, so anything clickable is also scriptable and testable.
//
// Every claim the sandbox submits is written as an 837P file of its own
// and loaded through claims intake, so claims arrive as they would from a
// clearinghouse: as sent, with nothing matched. No claim is submitted any
// other way (none through a portal, for now).
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
	"coreadmin/internal/claims"
	"coreadmin/internal/claims/adjudication"
	"coreadmin/internal/claims/claimsintake"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/common/x12"
	"coreadmin/internal/seed"
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
	// process, race, reprocess, reopen, adjust, void, corrected,
	// providerVoid, select, release, payment, setCoverage, setFee.
	Type string `json:"type"`

	// process / adjust / corrected (the corrected fields; empty keeps the
	// original).
	Claim *ClaimSpec `json:"claim,omitempty"`
	// race.
	Claims []*ClaimSpec `json:"claims,omitempty"`

	ClaimID string `json:"claimId,omitempty"`
	// Ask what the action would do, without doing it: process becomes
	// EstimateClaim, reprocess PreviewReadjudication, adjust
	// PreviewAdjustment.
	Preview bool   `json:"preview,omitempty"`
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
	// Set for estimates and previews: the projected claim.
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
	// Intake loads 837 files into the sandbox, and keeps them. Every claim
	// the sandbox submits arrives through it, in a file of its own.
	Intake *claimsintake.Processor

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
	// Files the sandbox has written, for their control numbers.
	nextFile int
	// The id to give the claim intake is handing over, and what became
	// of it (see receiver).
	assignID     string
	lastReceived *claimsv1.Claim
	lastErr      error
}

// receiver is what intake hands claims to: the claims service, noting the
// result, and naming the claim when the sandbox has a name for it.
type receiver struct{ s *Sim }

func (r receiver) Receive(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error) {
	if r.s.assignID != "" {
		c.Id = r.s.assignID
	}
	out, err := r.s.Claims.Receive(ctx, c)
	r.s.lastReceived, r.s.lastErr = out, err
	return out, err
}

// NewIn returns a freshly seeded sandbox in repos, which must be empty.
func NewIn(repos *repo.Repos) (*Sim, error) {
	ctx := context.Background()
	s := &Sim{Repos: repos, clock: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
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
	s.Intake = &claimsintake.Processor{
		Repos: s.Repos, Claims: receiver{s}, Now: s.now, Logf: s.logf,
		Files: map[string][]byte{}, ReceiverID: claimsintake.OurID,
	}
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
	found, _, _ := s.Repos.Claims.List(context.Background(), store.ListOptions[*claimsv1.Claim]{PageSize: 1, Where: []store.Cond{store.Eq(number, "claimNumber")}, Filter: func(o *claimsv1.Claim) bool {
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
// without a version (26075-E-00001-2). Unknown references come back
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
	if a.ClaimID != "" {
		a.ClaimID = s.Resolve(a.ClaimID)
	}
	switch a.Type {
	case "process":
		sub, err := s.newSubmission(ctx, a.Claim)
		if err != nil {
			entry.Title = "Process claim"
			return err
		}
		if a.Preview {
			entry.Title = "Estimate " + s.submissionLabel(sub, nil)
			c, err := s.parsed(sub)
			if err != nil {
				return err
			}
			r, err := s.Claims.EstimateClaim(ctx, &claimsv1.EstimateClaimRequest{Claim: c})
			if err != nil {
				return err
			}
			res.Preview = s.claimView(r.GetClaim())
			return nil
		}
		entry.Title = "Process " + s.submissionLabel(sub, nil)
		c, err := s.ingest(ctx, sub)
		if err != nil {
			return err
		}
		entry.Title = "Process " + s.submissionLabel(sub, c)
		res.ClaimID = c.GetId()

	case "race":
		entry.Title = fmt.Sprintf("Process %d claims at once", len(a.Claims))
		var cs []*claimsv1.Claim
		for _, spec := range a.Claims {
			sub, err := s.newSubmission(ctx, spec)
			if err != nil {
				return err
			}
			// Arriving at the same instant is what a file cannot do: these
			// are the claims as their files would carry them, handed over
			// together.
			c, err := s.parsed(sub)
			if err != nil {
				return err
			}
			cs = append(cs, c)
		}
		_, err := s.Claims.ProcessConcurrently(ctx, cs)
		return err

	case "reprocess":
		res.ClaimID = a.ClaimID
		var resolutions []*claimsv1.PendResolution
		for _, r := range a.Resolve {
			resolutions = append(resolutions, &claimsv1.PendResolution{Code: r.Code, LineNumber: r.Line, Note: a.Reason})
		}
		if a.Preview {
			entry.Title = "Preview readjudicating " + s.label(a.ClaimID)
			r, err := s.Claims.PreviewReadjudication(ctx, &claimsv1.PreviewReadjudicationRequest{ClaimId: a.ClaimID, Resolutions: resolutions})
			if err != nil {
				return err
			}
			res.Preview = s.claimView(r.GetClaim())
			return nil
		}
		entry.Title = "Reprocess " + s.label(a.ClaimID)
		if _, err := s.Claims.ReprocessClaim(ctx, &claimsv1.ReprocessClaimRequest{ClaimId: a.ClaimID, Reason: a.Reason, Resolutions: resolutions}); err != nil {
			return err
		}

	case "adjust":
		var corrected *claimsv1.Claim
		if a.Claim != nil {
			orig, err := s.Repos.Claims.Get(ctx, a.ClaimID)
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("%w: no claim %s", claims.ErrRejected, a.ClaimID)
			} else if err != nil {
				return err
			}
			if corrected, err = s.correct(ctx, orig, a.Claim); err != nil {
				return err
			}
		}
		if a.Preview {
			entry.Title = "Preview adjusting " + s.label(a.ClaimID)
			r, err := s.Claims.PreviewAdjustment(ctx, &claimsv1.PreviewAdjustmentRequest{OriginalClaimId: a.ClaimID, CorrectedClaim: corrected})
			if err != nil {
				return err
			}
			res.Delta, res.Preview = s.deltaOf(r.GetDelta()), s.claimView(r.GetClaim())
			return nil
		}
		entry.Title = "Adjust " + s.label(a.ClaimID)
		r, err := s.Claims.AdjustClaim(ctx, &claimsv1.AdjustClaimRequest{OriginalClaimId: a.ClaimID, Reason: a.Reason, CorrectedClaim: corrected})
		if err != nil {
			return err
		}
		res.Delta, res.ClaimID = s.deltaOf(r.GetDelta()), r.GetClaim().GetId()

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

	case "reopen":
		entry.Title = "Reopen " + s.label(a.ClaimID)
		_, err := s.Claims.ReopenClaim(ctx, &claimsv1.ReopenClaimRequest{ClaimId: a.ClaimID, Reason: a.Reason})
		return err

	// The provider's own corrected claim (frequency 7) or void (frequency
	// 8) of a claim: a new submission quoting the original's number.
	case "corrected", "providerVoid":
		orig, err := s.Repos.Claims.Get(ctx, a.ClaimID)
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: no claim %s", claims.ErrRejected, a.ClaimID)
		} else if err != nil {
			return err
		}
		spec := a.Claim
		if spec == nil {
			spec = &ClaimSpec{}
		}
		frequency := "7"
		entry.Title = "Provider's corrected claim for " + s.label(a.ClaimID)
		if a.Type == "providerVoid" {
			frequency, entry.Title = "8", "Provider's void of "+s.label(a.ClaimID)
		}
		sub, err := s.resubmission(ctx, orig, spec, frequency)
		if err != nil {
			return err
		}
		c, err := s.ingest(ctx, sub)
		if err != nil {
			return err
		}
		res.ClaimID = c.GetId()

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
	ps, _, err := s.Repos.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{PageSize: 1, Where: []store.Cond{store.Eq(npi, "npi")}})
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

// submission is a claim about to be submitted: the 837 file that carries
// it, the id the sandbox gives it, and the member it is for.
type submission struct {
	file     claimsintake.OneClaim
	id       string
	memberID string
}

// subscriber is who a submission names, as a provider would send them.
type subscriber struct {
	number, last, first, born, sex string
}

func subscriberOf(m *eligibilityv1.Member) subscriber {
	sex := "U"
	switch m.GetGender() {
	case commonv1.Gender_GENDER_FEMALE:
		sex = "F"
	case commonv1.Gender_GENDER_MALE:
		sex = "M"
	}
	return subscriber{
		number: m.GetCardId(), last: strings.ToUpper(m.GetName().GetLast()), first: strings.ToUpper(m.GetName().GetFirst()),
		born: x12Date(m.GetBirthDate()), sex: sex,
	}
}

func x12Date(d *commonv1.Date) string {
	if d == nil {
		return ""
	}
	return fmt.Sprintf("%04d%02d%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

// newSubmission expands a ClaimSpec into a one-line professional claim,
// as the 837 file a provider's clearinghouse would send.
func (s *Sim) newSubmission(ctx context.Context, spec *ClaimSpec) (*submission, error) {
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
	return s.write(ctx, &sp, subscriberOf(m), m.GetId(), "", "1", "", "M1711")
}

// resubmission is a provider's corrected claim (frequency 7) or void (8)
// of orig: orig's fields with the spec's non-empty ones applied, quoting
// orig's claim number.
func (s *Sim) resubmission(ctx context.Context, orig *claimsv1.Claim, spec *ClaimSpec, frequency string) (*submission, error) {
	line := orig.GetLines()[0]
	sp := ClaimSpec{
		ID:        spec.ID,
		Member:    orig.GetMemberId(),
		Provider:  orig.GetBillingProvider().GetNpi(),
		Procedure: line.GetProcedureCode(),
		POS:       line.GetPlaceOfServiceCode(),
		DOS:       dates.Format(orig.GetServiceDates().GetStart()),
		Charge:    line.GetCharge().GetCents(),
	}
	if len(orig.GetAuthorizationNumbers()) > 0 {
		sp.Auth = orig.GetAuthorizationNumbers()[0]
	}
	if spec.Member != "" {
		sp.Member = spec.Member
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
	// The member as matched or as the spec names; a claim that matched no
	// one is sent again with the subscriber it named.
	sent := orig.GetSubmittedSubscriber()
	who := subscriber{
		number: orig.GetMemberCardId(), last: sent.GetName().GetLast(), first: sent.GetName().GetFirst(),
		born: x12Date(sent.GetBirthDate()), sex: sent.GetGenderCode(),
	}
	if sp.Member != "" {
		m, err := s.member(ctx, sp.Member)
		if err != nil {
			return nil, err
		}
		who = subscriberOf(m)
	}
	dx := "M1711"
	if len(orig.GetDiagnosisCodes()) > 0 {
		dx = strings.ReplaceAll(orig.GetDiagnosisCodes()[0], ".", "")
	}
	return s.write(ctx, &sp, who, sp.Member, orig.GetPatientControlNumber(), frequency, claims.DisplayNumber(orig), dx)
}

// write is the 837 file for a filled-in spec.
func (s *Sim) write(ctx context.Context, sp *ClaimSpec, who subscriber, memberID, pcn, frequency, quoted, dx string) (*submission, error) {
	p, err := s.provider(ctx, sp.Provider)
	if err != nil {
		return nil, err
	}
	dos, err := dates.Parse(sp.DOS)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", claims.ErrRejected, err)
	}
	tin, err := s.tin(ctx, p)
	if err != nil {
		return nil, err
	}
	taxonomy := ""
	if len(p.GetTaxonomyCodes()) > 0 {
		taxonomy = p.GetTaxonomyCodes()[0]
	}
	s.nextFile++
	if pcn == "" {
		pcn = sp.ID
	}
	if pcn == "" {
		pcn = fmt.Sprintf("SIM-%05d", s.nextFile)
	}
	f := claimsintake.OneClaim{
		// Control numbers well clear of the clearinghouse's own files.
		ICN: fmt.Sprintf("%09d", 500_000_000+s.nextFile), Sent: s.clock, PCN: pcn,
		MemberNumber: who.number, Last: who.last, First: who.first, Born: who.born, Sex: who.sex,
		Org: strings.ToUpper(p.GetOrganizationName()), NPI: p.GetNpi(), TIN: tin, Taxonomy: taxonomy,
		Frequency: frequency, Quoted: quoted, Authorization: sp.Auth, Diagnosis: dx,
		Procedure: sp.Procedure, PlaceOfService: sp.POS, ServiceDate: x12Date(dos), ChargeCents: sp.Charge,
	}
	if f.Org == "" {
		f.ProviderLast, f.ProviderFirst = strings.ToUpper(p.GetName().GetLast()), strings.ToUpper(p.GetName().GetFirst())
	}
	return &submission{file: f, id: sp.ID, memberID: memberID}, nil
}

// parsed is a submission's claim as intake maps it from the file, without
// loading it: for an estimate, or to hand several over at once.
func (s *Sim) parsed(sub *submission) (*claimsv1.Claim, error) {
	ic, err := x12.Parse([]byte(sub.file.Text()))
	if err != nil {
		return nil, err
	}
	all := claimsintake.MapFile(ic, "")
	if len(all) != 1 || len(all[0].Errs) > 0 || len(all[0].Claims) != 1 {
		msg := "the file does not hold one readable claim"
		if len(all) == 1 && len(all[0].Errs) > 0 {
			msg = all[0].Errs[0].Message
		}
		return nil, fmt.Errorf("%w: %s", claims.ErrRejected, msg)
	}
	c := all[0].Claims[0].Claim
	// It did not arrive in a stored file.
	c.Id, c.SourceRecord = sub.id, nil
	return c, nil
}

// ingest loads a submission's file through intake and returns the claim
// it became.
func (s *Sim) ingest(ctx context.Context, sub *submission) (*claimsv1.Claim, error) {
	// The file-level events (received, numbers reserved, 999 returned) are
	// the same for every claim the sandbox submits; leave them out of its
	// log.
	quiet := *s.Intake
	quiet.Logf = nil
	s.assignID, s.lastReceived, s.lastErr = sub.id, nil, nil
	defer func() { s.assignID = "" }()
	job, err := quiet.Ingest(ctx, sub.file.Filename(), []byte(sub.file.Text()))
	if err != nil {
		return nil, err
	}
	if s.lastErr != nil {
		return nil, s.lastErr
	}
	if s.lastReceived == nil {
		msg := "the file was not loaded"
		for _, e := range job.GetErrors() {
			msg = e.GetMessage()
		}
		for _, set := range job.GetTransactionSets() {
			for _, e := range set.GetErrors() {
				msg = e.GetMessage()
			}
		}
		return nil, fmt.Errorf("%w: %s", claims.ErrRejected, msg)
	}
	return s.lastReceived, nil
}

// submissionLabel names a submission in the log: by the claim it became
// (c), or before that by the id the sandbox gave it.
func (s *Sim) submissionLabel(sub *submission, c *claimsv1.Claim) string {
	name := sub.id
	if c != nil {
		name = s.Claims.Label(c)
	}
	return fmt.Sprintf("%s (%s, %s)", name, s.memberName(sub.memberID), sub.file.Procedure)
}

// tin is the TIN a provider bills under: from its participations, or, for
// a provider in no network, the one it would put on its claim.
func (s *Sim) tin(ctx context.Context, p *providerv1.Provider) (string, error) {
	tin := outOfNetworkTINs[p.GetNpi()]
	parts, _, err := s.Repos.Participations.List(ctx, store.ListOptions[*providerv1.NetworkParticipation]{PageSize: 1, Where: []store.Cond{store.Eq(p.GetNpi(), "npi")}})
	if err != nil {
		return "", err
	}
	if len(parts) > 0 {
		tin = parts[0].GetTin()
	}
	return tin, nil
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
		// The corrected claim names the other member as its subscriber.
		who := subscriberOf(m)
		born, _ := dates.Parse(fmt.Sprintf("%s-%s-%s", who.born[:4], who.born[4:6], who.born[6:]))
		c.SubmittedPatient = nil
		c.SubmittedSubscriber = &claimsv1.SubmittedPerson{
			MemberNumber: who.number, Name: &commonv1.PersonName{Last: who.last, First: who.first}, BirthDate: born, GenderCode: who.sex,
		}
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

// Provider data knows a TIN only through a participation. A provider in
// no network tells us its TIN on the claim; these are the ones the
// simulator's out-of-network providers bill under.
var outOfNetworkTINs = map[string]string{"1987654321": "452345678"}

// fill sets the provider, dates, line and authorization from a spec, on a
// corrected claim we write ourselves (an adjustment). A provider that
// isn't changing stays as it was sent.
func (s *Sim) fill(ctx context.Context, c *claimsv1.Claim, sp *ClaimSpec) error {
	p, err := s.provider(ctx, sp.Provider)
	if err != nil {
		return err
	}
	dos, err := dates.Parse(sp.DOS)
	if err != nil {
		return fmt.Errorf("%w: %v", claims.ErrRejected, err)
	}
	if c.GetBillingProvider().GetNpi() != p.GetNpi() {
		tin, err := s.tin(ctx, p)
		if err != nil {
			return err
		}
		taxonomy := ""
		if len(p.GetTaxonomyCodes()) > 0 {
			taxonomy = p.GetTaxonomyCodes()[0]
		}
		c.BillingProvider = &claimsv1.ProviderRef{Npi: p.GetNpi(), Tin: tin, Name: providerName(p)}
		c.RenderingProvider = &claimsv1.ProviderRef{Npi: p.GetNpi(), TaxonomyCode: taxonomy}
	}
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
