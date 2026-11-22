package eligibility

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// RulesVersion names the derivation rules below. A member version records
// the rules that produced it; changing the rules is a reason to
// regenerate.
const RulesVersion = "eligibility-2"

// Rules are the derivation rules. Changing them changes what replaying the
// same inputs gives, which is what regeneration is for: compare first,
// then adopt as new versions.
type Rules struct {
	Version string
	// Senders must be set up per customer; the COBRA administrator speaks
	// for continuees; full files reconcile only their sender's members.
	SenderAuthority bool
}

var (
	// Current is what eligibility runs on.
	Current = Rules{Version: RulesVersion, SenderAuthority: true}
	// BeforeSenderAuthority is the rules as they were before sender
	// authority (any sender speaks for anyone). Kept to show what
	// replaying under a rules change does.
	BeforeSenderAuthority = Rules{Version: "eligibility-1"}
)

// Outcome statuses of an input.
const (
	Applied   = "APPLIED"
	Skipped   = "SKIPPED"
	Exception = "EXCEPTION"
	Proposed  = "PROPOSED"
)

// Outcome is what applying one input did.
type Outcome struct {
	InputID  string
	Sequence int64
	Status   string
	Message  string
	// Members the input asserted something about.
	MemberIDs []string
}

// Proposal is the terminations a full file proposes for the members of
// its customer that it left out. Nothing changes until it is approved.
type Proposal struct {
	JobID, SetControl string
	CustomerID        string
	// Who sent the full file.
	Sender string
	Role   Role
	// The file's effective date (DTP*007): coverage ends the day before.
	Effective time.Time
	// The input that closed the file.
	InputSequence int64
	Terms         []ProposedTerm
	Approved      bool
	ApprovedBy    string
}

type ProposedTerm struct {
	MemberID string
	Name     string
	// On COBRA: often not on the employer's file because someone else
	// (a COBRA administrator) is the source for them.
	COBRA bool
	// Last covered day.
	End time.Time
}

// State is eligibility as derived from a sequence of inputs.
type State struct {
	// By id, current members.
	Members   map[string]*eligibilityv1.Member
	Outcomes  []Outcome
	Proposals []*Proposal
	// By member id, every coverage assertion, in the order recorded: what
	// the members' coverage was resolved from.
	Assertions map[string][]Assertion
}

// Assertion is one input's claim about a member's coverage for a range of
// dates. For any date, the latest-recorded assertion that gets a say wins.
type Assertion struct {
	Sequence int64
	Line     eligibilityv1.CoverageLine
	From     time.Time
	// Last day; zero when open-ended.
	Through time.Time
	Covered bool
	COBRA   bool
	Sender  string
	Role    Role
	// An employer ending coverage the COBRA administrator established:
	// it doesn't get a say.
	Ignored bool
}

// MemberIDs lists the members in id order.
func (s *State) MemberIDs() []string {
	ids := make([]string, 0, len(s.Members))
	for id := range s.Members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// assertion is one input's claim about one member's coverage on one
// line, for a date range. covered false asserts no coverage. Coverage on
// any date is the latest-recorded assertion that covers it.
type assertion struct {
	seq      int64
	from, to time.Time // to is exclusive; forever when open
	covered  bool
	plan     string
	group    string
	tier     eligibilityv1.CoverageTier
	cobra    bool
	job      string
	// Who asserted it: the file's sender and its role for the customer.
	sender string
	role   Role
}

// person is a member being derived.
type person struct {
	m        *eligibilityv1.Member
	unit     string // customer|subscriber number
	ordinal  int    // order within the unit: 1 is the subscriber
	asserts  map[eligibilityv1.CoverageLine][]assertion
	lastSeq  int64
	enrolled bool // ever asserted covered
}

var forever = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// deriver replays inputs.
type deriver struct {
	ctx       context.Context
	repos     *repo.Repos
	st        *State
	people    map[string]*person
	units     map[string][]string // unit -> member ids, first seen first
	customers []*customerv1.Customer
	rules     Rules
	// The full file being read: its set key, and who it listed.
	fullKey   string
	fullSeen  map[string]bool
	fullFirst *eligibilityv1.EnrollmentTransaction
	fullSeq   int64
}

// Derive replays inputs, in sequence order, into eligibility. It reads
// the enrollment transactions inputs refer to and the customers and
// groups they resolve against, and writes nothing.
func Derive(ctx context.Context, repos *repo.Repos, inputs []*eligibilityv1.EligibilityInput) (*State, error) {
	return DeriveWith(ctx, repos, inputs, Current)
}

// DeriveWith replays inputs under the given rules.
func DeriveWith(ctx context.Context, repos *repo.Repos, inputs []*eligibilityv1.EligibilityInput, rules Rules) (*State, error) {
	custs, _, err := repos.Customers.List(ctx, store.ListOptions[*customerv1.Customer]{PageSize: 10_000})
	if err != nil {
		return nil, err
	}
	d := &deriver{ctx: ctx, repos: repos, st: &State{Members: map[string]*eligibilityv1.Member{}},
		people: map[string]*person{}, units: map[string][]string{}, customers: custs, rules: rules}
	sorted := slices.Clone(inputs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].GetSequence() < sorted[j].GetSequence() })
	for _, in := range sorted {
		var o Outcome
		switch x := in.GetInput().(type) {
		case *eligibilityv1.EligibilityInput_EnrollmentTransactionId:
			t, err := repos.EnrollmentTransactions.Get(ctx, x.EnrollmentTransactionId)
			if err != nil {
				return nil, err
			}
			d.closeFullFile(t)
			o, err = d.enrollment(in, t)
			if err != nil {
				return nil, err
			}
		case *eligibilityv1.EligibilityInput_FullFileApproval:
			d.closeFullFile(nil)
			o = d.approve(in, x.FullFileApproval)
		default:
			o = Outcome{Status: Exception, Message: "empty input"}
		}
		o.InputID, o.Sequence = in.GetId(), in.GetSequence()
		d.st.Outcomes = append(d.st.Outcomes, o)
	}
	d.closeFullFile(nil)
	d.st.Assertions = map[string][]Assertion{}
	for id, p := range d.people {
		d.st.Members[id] = d.build(p)
		d.st.Assertions[id] = exportAssertions(p, d.rules.SenderAuthority)
	}
	return d.st, nil
}

// enrollment applies one 834 member loop.
func (d *deriver) enrollment(in *eligibilityv1.EligibilityInput, t *eligibilityv1.EnrollmentTransaction) (Outcome, error) {
	h := t.GetHeader()
	switch h.GetPurpose() {
	case eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_INFORMATION_COPY:
		return Outcome{Status: Skipped, Message: "information copy (BGN01 22): not applied"}, nil
	case eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_UNKNOWN:
		return Outcome{Status: Exception, Message: "unrecognized purpose (BGN01): not applied"}, nil
	}
	cust := d.customer(h.GetSponsor())
	if cust == nil {
		return Outcome{Status: Exception, Message: fmt.Sprintf("sponsor %s %s%s isn't a customer", h.GetSponsor().GetName(),
			h.GetSponsor().GetIdQualifier(), h.GetSponsor().GetId())}, nil
	}
	sender := t.GetSource().GetSenderId()
	cobra := t.GetBenefitStatus() == eligibilityv1.BenefitStatus_BENEFIT_STATUS_COBRA
	role := Employer
	if d.rules.SenderAuthority {
		var ok bool
		if role, ok = roleOf(cust.GetId(), sender); !ok {
			return Outcome{Status: Exception, Message: fmt.Sprintf("%s isn't set up to send enrollment for %s", sender, cust.GetName())}, nil
		}
		termination := t.GetMaintenanceType() == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION
		switch admin := cobraAdministrator(cust.GetId()); {
		case role == Employer && cobra && admin != "":
			return Outcome{Status: Exception, Message: fmt.Sprintf("%s's COBRA is administered by %s; an employer file can't enroll COBRA", cust.GetName(), admin)}, nil
		case role == COBRAAdministrator && !cobra && !termination:
			return Outcome{Status: Exception, Message: fmt.Sprintf("%s administers COBRA only; benefit status must be C", sender)}, nil
		}
	}
	subNumber := ref(t.GetRefs(), "0F")
	id := memberID(cust.GetId(), subNumber, t)
	unit := cust.GetId() + "|" + subNumber
	if h.GetAction() == eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_REPLACE {
		d.openFullFile(t, in.GetSequence())
		d.fullSeen[id] = true
	}

	maint := t.GetMaintenanceType()
	if maint == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_UNKNOWN {
		return Outcome{Status: Exception, Message: "unrecognized maintenance type " + unrecognized(t, "INS03") + ": not applied", MemberIDs: []string{id}}, nil
	}
	p := d.people[id]
	if maint == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION && (p == nil || !p.enrolled) {
		return Outcome{Status: Exception, Message: "termination for a member who isn't enrolled", MemberIDs: []string{id}}, nil
	}
	if p == nil {
		p = &person{m: &eligibilityv1.Member{Id: id, CustomerId: cust.GetId()}, unit: unit,
			asserts: map[eligibilityv1.CoverageLine][]assertion{}}
		// The subscriber is 01; dependents follow from 02 in the order
		// they first appear, which replay preserves.
		p.ordinal = 1
		if !t.GetSubscriber() {
			p.ordinal = 2
			for _, other := range d.units[unit] {
				if d.people[other].ordinal != 1 {
					p.ordinal++
				}
			}
		}
		d.people[id] = p
		d.units[unit] = append(d.units[unit], id)
	}
	p.lastSeq = in.GetSequence()
	d.demographics(p, t, subNumber)

	msg := ""
	seq := in.GetSequence()
	switch {
	case maint == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION && len(t.GetCoverages()) == 0:
		end, ok := lastDay(t.GetDates(), "357")
		if !ok {
			return Outcome{Status: Exception, Message: "termination without an eligibility end date (DTP*357)", MemberIDs: []string{id}}, nil
		}
		for line := range p.asserts {
			p.asserts[line] = append(p.asserts[line], assertion{seq: seq, from: end.AddDate(0, 0, 1), to: forever, job: t.GetSource().GetIngestionJobId(), sender: sender, role: role})
		}
		msg = "coverage ends " + day(end)
	default:
		for _, hd := range t.GetCoverages() {
			line := coverageLine(hd.GetInsuranceLineCode())
			if hd.GetMaintenanceType() == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION ||
				maint == eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION {
				end, ok := lastDay(hd.GetDates(), "349")
				if !ok {
					if end, ok = lastDay(t.GetDates(), "357"); !ok {
						return Outcome{Status: Exception, Message: "termination without an end date (DTP*349 or 357)", MemberIDs: []string{id}}, nil
					}
				}
				p.asserts[line] = append(p.asserts[line], assertion{seq: seq, from: end.AddDate(0, 0, 1), to: forever, job: t.GetSource().GetIngestionJobId(), sender: sender, role: role})
				msg = join(msg, fmt.Sprintf("%s coverage ends %s", lineName(line), day(end)))
				continue
			}
			from, ok := firstDay(hd.GetDates(), "348")
			if !ok {
				if from, ok = firstDay(t.GetDates(), "356"); !ok {
					return Outcome{Status: Exception, Message: "coverage without a begin date (DTP*348 or 356)", MemberIDs: []string{id}}, nil
				}
			}
			to := forever
			if end, ok := lastDay(hd.GetDates(), "349"); ok {
				to = end.AddDate(0, 0, 1)
			}
			plan, group, err := d.plan(cust.GetId(), hd)
			if err != nil {
				return Outcome{}, err
			}
			if plan == "" {
				return Outcome{Status: Exception, Message: fmt.Sprintf("can't tell which plan %q means", hd.GetPlanCoverageDescription()), MemberIDs: []string{id}}, nil
			}
			p.asserts[line] = append(p.asserts[line], assertion{
				seq: seq, from: from, to: to, covered: true, plan: plan, group: group, tier: coverageTier(hd.GetCoverageLevelCode()),
				cobra: cobra, job: t.GetSource().GetIngestionJobId(), sender: sender, role: role,
			})
			p.enrolled = true
			msg = join(msg, fmt.Sprintf("%s coverage from %s", lineName(line), day(from)))
		}
	}
	if msg == "" {
		msg = "member details only"
	}
	if !t.GetSubscriber() && !d.hasSubscriber(unit) {
		msg += "; the subscriber isn't on file yet"
	}
	return Outcome{Status: Applied, Message: msg, MemberIDs: []string{id}}, nil
}

// openFullFile starts tracking a full file's members, if it's a new one.
func (d *deriver) openFullFile(t *eligibilityv1.EnrollmentTransaction, seq int64) {
	key := t.GetSource().GetIngestionJobId() + "|" + t.GetSource().GetTransactionSetControlNumber()
	if key == d.fullKey {
		return
	}
	d.closeFullFile(nil)
	d.fullKey, d.fullSeen, d.fullFirst = key, map[string]bool{}, t
	d.fullSeq = seq
}

// closeFullFile ends the full file being read unless next continues it,
// and proposes terminations for the enrolled members it left out.
func (d *deriver) closeFullFile(next *eligibilityv1.EnrollmentTransaction) {
	if d.fullKey == "" {
		return
	}
	if next != nil && next.GetSource().GetIngestionJobId()+"|"+next.GetSource().GetTransactionSetControlNumber() == d.fullKey {
		return
	}
	t := d.fullFirst
	cust := d.customer(t.GetHeader().GetSponsor())
	eff, ok := firstDay(t.GetHeader().GetDates(), "007")
	if !ok {
		eff = dateOf(t.GetHeader().GetDate())
	}
	role, _ := roleOf(cust.GetId(), t.GetSource().GetSenderId())
	pr := &Proposal{JobID: t.GetSource().GetIngestionJobId(), SetControl: t.GetSource().GetTransactionSetControlNumber(),
		CustomerID: cust.GetId(), Sender: t.GetSource().GetSenderId(), Role: role, Effective: eff, InputSequence: d.fullSeq}
	for _, id := range sortedKeys(d.people) {
		p := d.people[id]
		// A full file speaks only for the members whose coverage its
		// sender established: an employer's file doesn't reconcile the
		// COBRA administrator's continuees, and vice versa.
		win := winnerOn(p, eff, d.rules.SenderAuthority)
		if d.fullSeen[id] || p.m.GetCustomerId() != cust.GetId() || win == nil || !win.covered ||
			d.rules.SenderAuthority && win.sender != t.GetSource().GetSenderId() {
			continue
		}
		pr.Terms = append(pr.Terms, ProposedTerm{MemberID: id, Name: p.m.GetName().GetFirst() + " " + p.m.GetName().GetLast(),
			End: eff.AddDate(0, 0, -1), COBRA: win.cobra})
	}
	if len(pr.Terms) > 0 {
		d.st.Proposals = append(d.st.Proposals, pr)
		// The outcome of the file's last input says what it proposed.
		if n := len(d.st.Outcomes); n > 0 {
			o := &d.st.Outcomes[n-1]
			o.Status = Proposed
			o.Message = join(o.Message, fmt.Sprintf("full file leaves out %d enrolled member(s); terminations await approval", len(pr.Terms)))
		}
	}
	d.fullKey, d.fullSeen, d.fullFirst = "", nil, nil
}

// approve applies a full file's proposed terminations, except for members
// enrolled again since the file.
func (d *deriver) approve(in *eligibilityv1.EligibilityInput, a *eligibilityv1.FullFileApproval) Outcome {
	for _, pr := range d.st.Proposals {
		if pr.JobID != a.GetIngestionJobId() || pr.SetControl != a.GetTransactionSetControlNumber() {
			continue
		}
		if pr.Approved {
			return Outcome{Status: Skipped, Message: "already approved"}
		}
		pr.Approved, pr.ApprovedBy = true, in.GetRecordedBy()
		var ids []string
		for _, term := range pr.Terms {
			p := d.people[term.MemberID]
			if p.lastSeq > pr.InputSequence {
				continue
			}
			for line := range p.asserts {
				p.asserts[line] = append(p.asserts[line], assertion{seq: in.GetSequence(), from: term.End.AddDate(0, 0, 1), to: forever, job: pr.JobID,
					sender: pr.Sender, role: pr.Role})
			}
			p.lastSeq = in.GetSequence()
			ids = append(ids, term.MemberID)
		}
		return Outcome{Status: Applied, Message: fmt.Sprintf("terminated %d member(s) the full file left out, as of %s", len(ids), day(pr.Effective)), MemberIDs: ids}
	}
	return Outcome{Status: Exception, Message: "no full-file proposal for that file"}
}

// build resolves a person's assertions into a Member with coverage spans.
func (d *deriver) build(p *person) *eligibilityv1.Member {
	m := p.m
	m.Coverage = nil
	for _, line := range sortedLines(p.asserts) {
		as := p.asserts[line]
		points := []time.Time{}
		for _, a := range as {
			points = append(points, a.from, a.to)
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Before(points[j]) })
		points = slices.CompactFunc(points, func(a, b time.Time) bool { return a.Equal(b) })
		var cur *eligibilityv1.CoverageSpan
		var curKey string
		for i := 0; i+1 < len(points); i++ {
			from, to := points[i], points[i+1]
			// Every assertion covering this stretch, in the order recorded:
			// the last one wins.
			var covering []*assertion
			for k := range as {
				a := &as[k]
				if !a.from.After(from) && !a.to.Before(to) {
					covering = append(covering, a)
				}
			}
			if d.rules.SenderAuthority {
				covering = authoritative(covering)
			}
			sort.Slice(covering, func(i, j int) bool { return covering[i].seq < covering[j].seq })
			if len(covering) == 0 || !covering[len(covering)-1].covered {
				cur, curKey = nil, ""
				continue
			}
			win := covering[len(covering)-1]
			key := fmt.Sprintf("%s|%s|%d|%t", win.plan, win.group, win.tier, win.cobra)
			// The span's source is the input that last changed it: going
			// back from the winner, the earliest of the assertions that
			// said the same thing. A full file confirming coverage
			// already on file changes nothing, so creates no version.
			source := win
			for i := len(covering) - 2; i >= 0; i-- {
				a := covering[i]
				if !a.covered || fmt.Sprintf("%s|%s|%d|%t", a.plan, a.group, a.tier, a.cobra) != key {
					break
				}
				source = a
			}
			if cur != nil && key == curKey {
				cur.Effective.End = endOf(to)
				continue
			}
			cur, curKey = &eligibilityv1.CoverageSpan{
				Id:     fmt.Sprintf("cov_%s_%s_%s", strings.TrimPrefix(m.GetId(), "mem_"), strings.ToLower(lineName(line)), from.Format("20060102")),
				PlanId: win.plan, GroupId: win.group, Line: line, Tier: win.tier, Cobra: win.cobra,
				Status:               eligibilityv1.CoverageStatus_COVERAGE_STATUS_ACTIVE,
				Effective:            &commonv1.DateRange{Start: dateProto(from), End: endOf(to)},
				SourceIngestionJobId: source.job,
			}, key
			m.Coverage = append(m.Coverage, cur)
		}
	}
	// Card ids: the customer's prefix, the subscriber number's digits,
	// and the member's place in the unit (01 the subscriber).
	m.SubscriberId = subscriberID(p.unit)
	m.CardId = fmt.Sprintf("%s%s%02d", cardPrefix(d.customerByID(m.GetCustomerId())), digits(strings.SplitN(p.unit, "|", 2)[1]), p.ordinal)
	return m
}

func (d *deriver) demographics(p *person, t *eligibilityv1.EnrollmentTransaction, subNumber string) {
	m := p.m
	if n := t.GetName(); n.GetLast() != "" {
		m.Name = &commonv1.PersonName{First: title(n.GetFirst()), Middle: title(n.GetMiddle()), Last: title(n.GetLast()), Suffix: n.GetSuffix()}
	}
	if t.GetBirthDate() != nil {
		m.BirthDate = t.GetBirthDate()
	}
	if g := t.GetGenderCode(); g != "" {
		m.Gender = map[string]commonv1.Gender{"F": commonv1.Gender_GENDER_FEMALE, "M": commonv1.Gender_GENDER_MALE}[g]
		if m.Gender == commonv1.Gender_GENDER_UNSPECIFIED {
			m.Gender = commonv1.Gender_GENDER_UNKNOWN
		}
	}
	if t.GetMemberIdQualifier() == "34" && t.GetMemberId() != "" {
		m.Ssn = t.GetMemberId()
	}
	if a := t.GetAddress(); a != nil {
		m.Address = &commonv1.Address{Lines: titles(a.GetLines()), City: title(a.GetCity()), State: a.GetState(), PostalCode: a.GetPostalCode(), CountryCode: a.GetCountryCode()}
	}
	m.Relationship = relationship(t.GetRelationshipCode(), t.GetSubscriber())
	m.ExternalIds = []*commonv1.ExternalId{{System: "sponsor-subscriber-number", Value: subNumber}}
}

func (d *deriver) hasSubscriber(unit string) bool {
	for _, id := range d.units[unit] {
		if d.people[id].ordinal == 1 {
			return true
		}
	}
	return false
}

// customer matches a sponsor to a customer by federal tax id.
func (d *deriver) customer(sp *eligibilityv1.EnrollmentParty) *customerv1.Customer {
	if sp.GetIdQualifier() != "FI" {
		return nil
	}
	for _, c := range d.customers {
		if c.GetEin() == sp.GetId() {
			return c
		}
	}
	return nil
}

func (d *deriver) customerByID(id string) *customerv1.Customer {
	for _, c := range d.customers {
		if c.GetId() == id {
			return c
		}
	}
	return nil
}

// plan resolves a coverage loop to a plan and group: the customer's group
// offering one plan for the line. Empty when it can't tell.
func (d *deriver) plan(customerID string, hd *eligibilityv1.HealthCoverage) (string, string, error) {
	groups, _, err := d.repos.Groups.List(d.ctx, store.ListOptions[*customerv1.Group]{PageSize: 1000, Where: []store.Cond{store.Eq(customerID, "customerId")}})
	if err != nil {
		return "", "", err
	}
	var plan, group string
	for _, g := range groups {
		for _, pid := range g.GetPlanIds() {
			if plan != "" && plan != pid {
				return "", "", nil
			}
			plan, group = pid, g.GetId()
		}
	}
	return plan, group, nil
}

// memberID is stable across replays: it depends only on who the loop
// names. A subscriber is the subscriber number's self; a dependent is
// known by the member id the sponsor sent (NM109), or failing that by
// first name and birth date.
func memberID(customerID, subNumber string, t *eligibilityv1.EnrollmentTransaction) string {
	if t.GetSubscriber() {
		return subscriberID(customerID + "|" + subNumber)
	}
	key := t.GetMemberIdQualifier() + ":" + t.GetMemberId()
	if t.GetMemberId() == "" {
		b := t.GetBirthDate()
		key = fmt.Sprintf("%s:%04d%02d%02d", strings.ToUpper(t.GetName().GetFirst()), b.GetYear(), b.GetMonth(), b.GetDay())
	}
	return hashID(customerID + "|" + subNumber + "|" + key)
}

func subscriberID(unit string) string { return hashID(unit + "|self") }

func hashID(s string) string {
	h := sha256.Sum256([]byte(s))
	return "mem_" + hex.EncodeToString(h[:])[:12]
}

func exportAssertions(p *person, authority bool) []Assertion {
	var out []Assertion
	for _, line := range sortedLines(p.asserts) {
		as := p.asserts[line]
		for _, a := range as {
			x := Assertion{Sequence: a.seq, Line: line, From: a.from, Covered: a.covered, COBRA: a.cobra, Sender: a.sender, Role: a.role}
			if !a.to.Equal(forever) {
				x.Through = a.to.AddDate(0, 0, -1)
			}
			if authority && a.role == Employer && !a.covered {
				for _, b := range as {
					if b.role == COBRAAdministrator && b.covered && b.from.Before(a.to) && a.from.Before(b.to) {
						x.Ignored = true
					}
				}
			}
			out = append(out, x)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

// authoritative drops the assertions that don't get a say: an employer
// ending coverage doesn't end coverage a COBRA administrator established,
// because the administrator, not the employer, speaks for continuees.
// Employer coverage (a rehire, say) still counts.
func authoritative(as []*assertion) []*assertion {
	admin := false
	for _, a := range as {
		if a.role == COBRAAdministrator && a.covered {
			admin = true
		}
	}
	if !admin {
		return as
	}
	out := as[:0:0]
	for _, a := range as {
		if a.role == Employer && !a.covered {
			continue
		}
		out = append(out, a)
	}
	return out
}

// winnerOn is the assertion in force for a person on d, on any line,
// preferring one that says covered.
func winnerOn(p *person, d time.Time, authority bool) *assertion {
	var best *assertion
	for _, line := range sortedLines(p.asserts) {
		var here []*assertion
		for k := range p.asserts[line] {
			a := &p.asserts[line][k]
			if !a.from.After(d) && a.to.After(d) {
				here = append(here, a)
			}
		}
		if authority {
			here = authoritative(here)
		}
		var win *assertion
		for _, a := range here {
			if win == nil || a.seq > win.seq {
				win = a
			}
		}
		if win != nil && (best == nil || win.covered && !best.covered) {
			best = win
		}
	}
	return best
}

func relationship(code string, subscriber bool) eligibilityv1.Relationship {
	if subscriber || code == "18" {
		return eligibilityv1.Relationship_RELATIONSHIP_SELF
	}
	switch code {
	case "01":
		return eligibilityv1.Relationship_RELATIONSHIP_SPOUSE
	case "53":
		return eligibilityv1.Relationship_RELATIONSHIP_DOMESTIC_PARTNER
	case "19":
		return eligibilityv1.Relationship_RELATIONSHIP_CHILD
	}
	return eligibilityv1.Relationship_RELATIONSHIP_OTHER_DEPENDENT
}

func coverageLine(code string) eligibilityv1.CoverageLine {
	switch code {
	case "DEN":
		return eligibilityv1.CoverageLine_COVERAGE_LINE_DENTAL
	case "VIS":
		return eligibilityv1.CoverageLine_COVERAGE_LINE_VISION
	case "PDG":
		return eligibilityv1.CoverageLine_COVERAGE_LINE_PHARMACY
	}
	return eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL
}

func lineName(l eligibilityv1.CoverageLine) string {
	return map[eligibilityv1.CoverageLine]string{1: "Medical", 2: "Pharmacy", 3: "Dental", 4: "Vision"}[l]
}

func coverageTier(code string) eligibilityv1.CoverageTier {
	return map[string]eligibilityv1.CoverageTier{
		"EMP": eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE, "ESP": eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_SPOUSE,
		"ECH": eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_CHILDREN, "FAM": eligibilityv1.CoverageTier_COVERAGE_TIER_FAMILY,
	}[code]
}

func ref(refs []*eligibilityv1.QualifiedRef, q string) string {
	for _, r := range refs {
		if r.GetQualifier() == q {
			return r.GetValue()
		}
	}
	return ""
}

func unrecognized(t *eligibilityv1.EnrollmentTransaction, element string) string {
	for _, u := range t.GetUnrecognizedCodes() {
		if u.GetElement() == element {
			return u.GetValue()
		}
	}
	return "?"
}

func firstDay(ds []*eligibilityv1.QualifiedDate, q string) (time.Time, bool) {
	for _, d := range ds {
		if d.GetQualifier() == q && d.GetDate() != nil {
			return dateOf(d.GetDate()), true
		}
	}
	return time.Time{}, false
}

// lastDay is the date of a DTP, or the end of an RD8 range.
func lastDay(ds []*eligibilityv1.QualifiedDate, q string) (time.Time, bool) {
	for _, d := range ds {
		if d.GetQualifier() != q {
			continue
		}
		if d.GetEnd() != nil {
			return dateOf(d.GetEnd()), true
		}
		if d.GetDate() != nil {
			return dateOf(d.GetDate()), true
		}
	}
	return time.Time{}, false
}

func dateOf(d *commonv1.Date) time.Time {
	return time.Date(int(d.GetYear()), time.Month(d.GetMonth()), int(d.GetDay()), 0, 0, 0, 0, time.UTC)
}

func dateProto(t time.Time) *commonv1.Date {
	return &commonv1.Date{Year: int32(t.Year()), Month: int32(t.Month()), Day: int32(t.Day())}
}

// endOf turns an exclusive end into the inclusive end date of a span; nil
// when open-ended.
func endOf(to time.Time) *commonv1.Date {
	if to.Equal(forever) {
		return nil
	}
	return dateProto(to.AddDate(0, 0, -1))
}

func day(t time.Time) string { return t.Format("2006-01-02") }

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func cardPrefix(c *customerv1.Customer) string {
	n := strings.ToUpper(digitsless(c.GetName()))
	if len(n) > 3 {
		n = n[:3]
	}
	return n
}

func digitsless(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
			return r
		}
		return -1
	}, s)
}

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func title(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func titles(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = title(s)
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func sortedLines(m map[eligibilityv1.CoverageLine][]assertion) []eligibilityv1.CoverageLine {
	ls := make([]eligibilityv1.CoverageLine, 0, len(m))
	for l := range m {
		ls = append(ls, l)
	}
	slices.Sort(ls)
	return ls
}
