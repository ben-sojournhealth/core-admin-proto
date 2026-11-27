package portal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/x12"
	"coreadmin/internal/employer/eligibility"
	"coreadmin/internal/employer/enrollmentintake"
)

// Proposal is the terminations one of the customer's full files proposed
// for the people it left out. Nothing changes until it is approved.
type Proposal struct {
	File string `json:"file"`
	// Who sent the full file: employer or COBRA administrator.
	From string `json:"from"`
	// The file's effective date: coverage would end the day before.
	Effective  string         `json:"effective"`
	Approved   bool           `json:"approved"`
	ApprovedBy string         `json:"approvedBy,omitempty"`
	Terms      []ProposedTerm `json:"terms"`
}

type ProposedTerm struct {
	MemberID string `json:"memberId"`
	Name     string `json:"name"`
	// The last covered day proposed.
	End   string `json:"end"`
	COBRA bool   `json:"cobra"`
}

func (p *Portal) proposals(ctx context.Context, st *eligibility.State, customerID string) []Proposal {
	out := []Proposal{}
	for i := len(st.Proposals) - 1; i >= 0; i-- {
		pr := st.Proposals[i]
		if pr.CustomerID != customerID {
			continue
		}
		v := Proposal{From: pr.Role.String(), Effective: pr.Effective.Format("2006-01-02"),
			Approved: pr.Approved, ApprovedBy: pr.ApprovedBy, Terms: []ProposedTerm{}}
		if j, err := p.Repos.EnrollmentIngestionJobs.Get(ctx, pr.JobID); err == nil {
			v.File = j.GetOriginalFilename()
		}
		for _, t := range pr.Terms {
			v.Terms = append(v.Terms, ProposedTerm{MemberID: t.MemberID, Name: t.Name, End: t.End.Format("2006-01-02"), COBRA: t.COBRA})
		}
		out = append(out, v)
	}
	return out
}

// MonthCount is who had medical coverage on the first of a month, from
// what we know now. Which day of the month a fee counts heads on is the
// contract's; the first is used here.
type MonthCount struct {
	// The first of the month.
	Month string `json:"month"`
	// Subscribers: employees, and former employees continuing on COBRA.
	Employees int `json:"employees"`
	Members   int `json:"members"`
	// After today: known future coverage, not yet billable.
	Future bool `json:"future"`
}

func months(members []*eligibilityv1.Member, now *commonv1.Date) []MonthCount {
	out := []MonthCount{}
	for m := int32(1); m <= 12; m++ {
		first := &commonv1.Date{Year: now.GetYear(), Month: m, Day: 1}
		mc := MonthCount{Month: dates.Format(first), Future: dates.Cmp(first, now) > 0}
		for _, mem := range members {
			if coveredOn(mem, first) == nil {
				continue
			}
			mc.Members++
			if mem.GetRelationship() == eligibilityv1.Relationship_RELATIONSHIP_SELF {
				mc.Employees++
			}
		}
		out = append(out, mc)
	}
	return out
}

// coveredOn is the member's medical span that includes the day, if any.
func coveredOn(m *eligibilityv1.Member, day *commonv1.Date) *eligibilityv1.CoverageSpan {
	for _, s := range medical(m) {
		if dates.In(day, s.GetEffective()) {
			return s
		}
	}
	return nil
}

// CoveredLives counts a plan's covered lives over its benefit period so
// far, the actual count way: every covered person (employees and
// dependents) on every day, added up. The average is LifeDays / Days. It
// is the count the PCORI fee is figured from; for a period still running
// it is a figure to date, not the one to file.
type CoveredLives struct {
	Plan string `json:"plan"`
	From string `json:"from"`
	// The last day counted: today, or the period's end if it has passed.
	Through string `json:"through"`
	// Days in From..Through.
	Days int64 `json:"days"`
	// Covered people, summed over those days.
	LifeDays int64 `json:"lifeDays"`
}

func coveredLives(members []*eligibilityv1.Member, plans []Plan, now *commonv1.Date) []CoveredLives {
	out := []CoveredLives{}
	for _, pl := range plans {
		from, err1 := dates.Parse(pl.Start)
		end, err2 := dates.Parse(pl.End)
		if err1 != nil || dates.Cmp(from, now) > 0 {
			continue
		}
		through := now
		if err2 == nil && dates.Cmp(end, now) < 0 {
			through = end
		}
		cl := CoveredLives{Plan: pl.Name, From: dates.Format(from), Through: dates.Format(through), Days: days(from, through)}
		for _, m := range members {
			for _, s := range medical(m) {
				if s.GetPlanId() != pl.ID {
					continue
				}
				a, b := s.GetEffective().GetStart(), s.GetEffective().GetEnd()
				if dates.Cmp(a, from) < 0 {
					a = from
				}
				if b == nil || dates.Cmp(b, through) > 0 {
					b = through
				}
				if n := days(a, b); n > 0 {
					cl.LifeDays += n
				}
			}
		}
		out = append(out, cl)
	}
	return out
}

// days counts the days from a through b, both included; zero or less when
// b is before a.
func days(a, b *commonv1.Date) int64 {
	t := func(d *commonv1.Date) time.Time {
		return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
	}
	return int64(t(b).Sub(t(a))/(24*time.Hour)) + 1
}

// History is how a member's record came to be: every version eligibility
// saved, oldest first, with the file that caused it.
type History struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Versions []Version `json:"versions"`
}

type Version struct {
	Version  int32  `json:"version"`
	Recorded string `json:"recorded"`
	// What caused it, e.g. "Priya Patel · termination".
	Title string `json:"title"`
	File  string `json:"file,omitempty"`
	// What changed from the version before: coverage, name, address, ...
	Changes []string `json:"changes"`
	Spans   []Span   `json:"spans"`
}

// ErrNotYours is asking for something that isn't the customer's.
var ErrNotYours = errors.New("not found")

// Member is the history of one of the customer's members. A member of
// another customer is not found.
func (p *Portal) Member(ctx context.Context, customerID, memberID string, now time.Time) (*History, error) {
	m, err := p.Repos.Members.Get(ctx, memberID)
	if err != nil || m.GetCustomerId() != customerID {
		return nil, ErrNotYours
	}
	d, err := p.Elig.Member(ctx, memberID, now)
	if err != nil {
		return nil, err
	}
	h := &History{ID: m.GetId(), Name: fullName(m.GetName()), Versions: []Version{}}
	for _, v := range d.Versions {
		hv := Version{Version: v.Version, Recorded: v.Recorded, Title: v.Title, File: v.File, Changes: v.Changes, Spans: []Span{}}
		for _, s := range v.Spans {
			hv.Spans = append(hv.Spans, Span{From: s.From, To: s.To, Tier: s.Tier, Plan: s.Plan, COBRA: s.COBRA})
		}
		h.Versions = append(h.Versions, hv)
	}
	return h, nil
}

// Check is how we would read a file, without loading it: nothing is
// stored, no acknowledgment is sent, nobody's coverage changes.
type Check struct {
	// Why the file as a whole would be refused, if it would.
	Error  string `json:"error,omitempty"`
	Sender string `json:"sender"`
	// The sender is set up to send this customer's enrollment.
	SenderOK bool `json:"senderOk"`
	// A full file: it lists everyone, rather than changes.
	Full bool       `json:"full"`
	Sets []CheckSet `json:"sets"`
}

type CheckSet struct {
	Control  string      `json:"control"`
	Accepted bool        `json:"accepted"`
	Errors   []string    `json:"errors"`
	Loops    []CheckLoop `json:"loops"`
}

type CheckLoop struct {
	Name         string   `json:"name"`
	Relationship string   `json:"relationship"`
	Action       string   `json:"action"`
	Reason       string   `json:"reason,omitempty"`
	Coverage     []string `json:"coverage"`
	COBRA        bool     `json:"cobra"`
	// Codes we don't recognize, kept as sent.
	Unrecognized []string `json:"unrecognized"`
}

// CheckFile reads a file the way intake would and reports what it found.
func (p *Portal) CheckFile(ctx context.Context, customerID string, raw []byte) (*Check, error) {
	if _, err := p.Repos.Customers.Get(ctx, customerID); err != nil {
		return nil, err
	}
	jv, err := p.Intake.Preview(ctx, raw)
	if err != nil {
		return nil, err
	}
	c := &Check{Error: jv.Error, Sender: jv.Sender, Full: jv.Mode == "full", Sets: []CheckSet{}}
	for _, s := range eligibility.Senders(customerID) {
		if s.Sender == jv.Sender {
			c.SenderOK = true
		}
	}
	for _, set := range jv.Sets {
		cs := CheckSet{Control: set.Control, Accepted: set.Accepted, Errors: []string{}, Loops: []CheckLoop{}}
		for _, e := range set.Errors {
			cs.Errors = append(cs.Errors, fmt.Sprintf("segment %d: %s", e.Segment, e.Message))
		}
		for _, m := range set.Members {
			cs.Loops = append(cs.Loops, CheckLoop{Name: m.Name, Relationship: m.Relationship, Action: m.Action, Reason: m.Reason,
				Coverage: m.Coverage, COBRA: m.COBRA, Unrecognized: append([]string{}, m.Unrecognized...)})
		}
		c.Sets = append(c.Sets, cs)
	}
	return c, nil
}

// SampleFile is a sample 834 to try the file check with.
type SampleFile struct {
	Title    string `json:"title"`
	Filename string `json:"filename"`
	Text     string `json:"text"`
}

// Samples are the sample files the customer's own employer sender sends.
func (p *Portal) Samples(customerID string) []SampleFile {
	mine := map[string]bool{}
	for _, s := range eligibility.Senders(customerID) {
		if s.Role == eligibility.Employer {
			mine[s.Sender] = true
		}
	}
	out := []SampleFile{}
	for _, x := range enrollmentintake.Samples() {
		if ic, err := x12.Parse([]byte(x.Text)); err == nil && mine[ic.Sender()] {
			out = append(out, SampleFile{Title: x.Title, Filename: x.Filename, Text: x.Text})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out
}
