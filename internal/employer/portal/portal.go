// Package portal is the employer portal: what one customer sees of its own
// enrollment. It is read-only: the people covered on a date and how each
// came to be, the plans in force, the 834 files received for the customer
// and what each member loop did, a check of a file before it is sent, and
// the counts billing and compliance start from.
//
// Every read is for one customer, and nothing about another customer is
// returned: members are looked up by customer, and a file shared with
// other customers (a COBRA administrator's) counts only this customer's
// member loops.
package portal

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	customerv1 "coreadmin/gen/coreadmin/customer/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	planv1 "coreadmin/gen/coreadmin/plan/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/eligibility"
	"coreadmin/internal/employer/enrollmentintake"
)

// Portal reads the Employer domain's stores for one customer at a time.
type Portal struct {
	Repos *repo.Repos
	// Eligibility: what each input did, the terminations full files
	// propose, and members' histories.
	Elig *eligibility.Service
	// Intake, to check a file without loading it.
	Intake *enrollmentintake.Processor
}

// EmployerRef is a customer that can sign in.
type EmployerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// View is everything the portal shows one customer.
type View struct {
	Employer EmployerRef `json:"employer"`
	Place    string      `json:"place"`
	// The date the counts and statuses are about: Now, unless another
	// date was asked for.
	Today string `json:"today"`
	// Today's date.
	Now string `json:"now"`
	// People with medical coverage on Today: employees, their dependents,
	// and everyone continuing on COBRA (former employees and dependents),
	// who are counted apart. The three add up to Covered.
	Covered     int `json:"covered"`
	Subscribers int `json:"subscribers"`
	Dependents  int `json:"dependents"`
	COBRA       int `json:"cobra"`
	// Covered subscribers by the tier they elected, COBRA included.
	Tiers    []TierCount `json:"tiers"`
	Plans    []Plan      `json:"plans"`
	Families []Family    `json:"families"`
	Files    []File      `json:"files"`
	// Terminations a full file proposed that nobody has approved yet.
	PendingTerms int `json:"pendingTerms"`
	// What the customer's full files proposed, newest first.
	Proposals []Proposal `json:"proposals"`
	// Covered on the first of each month of Now's year.
	Months []MonthCount `json:"months"`
	// Per plan, the covered lives of its benefit period so far.
	Lives []CoveredLives `json:"lives"`
}

type TierCount struct {
	Tier        string `json:"tier"`
	Subscribers int    `json:"subscribers"`
}

// Family is a subscriber and their dependents.
type Family struct {
	SubscriberNumber string `json:"subscriberNumber"`
	Name             string `json:"name"`
	// The subscriber's tier on Today, or of their latest coverage.
	Tier    string   `json:"tier"`
	Members []Member `json:"members"`
}

type Member struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
	Card         string `json:"card"`
	// Medical coverage on Today: active, ending (active, with an end date
	// on file), ended, upcoming or none.
	Status string `json:"status"`
	// The last covered day, when one is on file: of today's coverage, or
	// of the latest coverage when it has ended.
	Ends string `json:"ends,omitempty"`
	// The first covered day, when coverage is upcoming.
	Starts string `json:"starts,omitempty"`
	COBRA  bool   `json:"cobra"`
	// The last covered day a full file proposes, not yet approved.
	PendingEnd string `json:"pendingEnd,omitempty"`
	Spans      []Span `json:"spans"`
}

// Span is one period of medical coverage. To is empty when open-ended.
type Span struct {
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	Tier  string `json:"tier"`
	Plan  string `json:"plan"`
	COBRA bool   `json:"cobra"`
}

// Plan is a plan the customer offers, with the version in force on Today
// (the latest version if none is).
type Plan struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Start string `json:"start"`
	End   string `json:"end"`
	// People covered on the plan on Today.
	Covered  int        `json:"covered"`
	Tiers    []PlanTier `json:"tiers"`
	Benefits []Benefit  `json:"benefits"`
}

// PlanTier is a network tier's limits, in cents; zero where the plan has
// none.
type PlanTier struct {
	Name                 string `json:"name"`
	OutOfNetwork         bool   `json:"outOfNetwork"`
	DeductibleIndividual int64  `json:"deductibleIndividual"`
	DeductibleFamily     int64  `json:"deductibleFamily"`
	OOPIndividual        int64  `json:"oopIndividual"`
	OOPFamily            int64  `json:"oopFamily"`
}

type Benefit struct {
	Category string `json:"category"`
	// One per plan tier, in the tiers' order; nil where the benefit says
	// nothing for that tier.
	Shares []*Share `json:"shares"`
}

type Share struct {
	Covered           bool  `json:"covered"`
	Copay             int64 `json:"copay"`
	CoinsuranceBps    int32 `json:"coinsuranceBps"`
	DeductibleApplies bool  `json:"deductibleApplies"`
}

// File is an 834 received for the customer.
type File struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Received string `json:"received"`
	// Who sent it: employer or COBRA administrator.
	From string `json:"from"`
	// accepted, partial (some sets or loops didn't load) or rejected.
	Status string `json:"status"`
	// A full file: it lists everyone, rather than changes.
	Full bool `json:"full"`
	// The customer's member loops that loaded, and what they asked for.
	Members int           `json:"members"`
	Actions []ActionCount `json:"actions"`
	Error   string        `json:"error,omitempty"`
	// Why a set was refused, one per problem.
	Errors []string `json:"errors"`
	// The customer's member loops, in file order, and what each did.
	Loops []Loop `json:"loops"`
	// Loops eligibility could not apply.
	Exceptions int `json:"exceptions"`
}

// Loop is one member loop of a file and what eligibility did with it.
type Loop struct {
	Name   string `json:"name"`
	Action string `json:"action"`
	// applied, skipped, exception or proposed (a full file's loop, held
	// with the file's terminations); empty if eligibility hasn't seen it.
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
}

type ActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

// Employers lists the customers, for choosing whose portal to open.
func (p *Portal) Employers(ctx context.Context) ([]EmployerRef, error) {
	cs, _, err := p.Repos.Customers.List(ctx, store.ListOptions[*customerv1.Customer]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	out := []EmployerRef{}
	for _, c := range cs {
		out = append(out, EmployerRef{ID: c.GetId(), Name: c.GetName()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// View builds a customer's portal. now is today; on is the date the
// roster and counts are about, today when nil. Either way it is what we
// know now about that date.
func (p *Portal) View(ctx context.Context, customerID string, now time.Time, on *commonv1.Date) (*View, error) {
	c, err := p.Repos.Customers.Get(ctx, customerID)
	if err != nil {
		return nil, err
	}
	day := dates.FromTime(now)
	if on != nil {
		day = on
	}
	st, err := p.Elig.State(ctx)
	if err != nil {
		return nil, err
	}
	v := &View{
		Employer: EmployerRef{ID: c.GetId(), Name: c.GetName()}, Today: dates.Format(day), Now: dates.Format(dates.FromTime(now)),
		Proposals: []Proposal{}, Months: []MonthCount{}, Lives: []CoveredLives{},
		Place: strings.TrimSuffix(c.GetAddress().GetCity()+", "+c.GetAddress().GetState(), ", "),
		Tiers: []TierCount{}, Plans: []Plan{}, Families: []Family{}, Files: []File{},
	}

	plans, err := p.plans(ctx, customerID, day)
	if err != nil {
		return nil, err
	}
	planName := map[string]string{}
	for _, pl := range plans {
		planName[pl.ID] = pl.Name
	}

	pending := pendingTerms(st, customerID)
	v.PendingTerms = len(pending)
	v.Proposals = p.proposals(ctx, st, customerID)

	members, _, err := p.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 100_000,
		Where: []store.Cond{store.Eq(customerID, "customerId")}})
	if err != nil {
		return nil, err
	}
	families := map[string]*Family{}
	tiers := map[eligibilityv1.CoverageTier]int{}
	onPlan := map[string]int{}
	for _, m := range members {
		row := Member{
			ID: m.GetId(), Name: fullName(m.GetName()), Card: m.GetCardId(), Status: "none",
			Relationship: enum(m.GetRelationship().String(), "RELATIONSHIP_"), PendingEnd: pending[m.GetId()], Spans: []Span{},
		}
		self := m.GetRelationship() == eligibilityv1.Relationship_RELATIONSHIP_SELF
		spans := medical(m)
		var current, latest *eligibilityv1.CoverageSpan
		for _, s := range spans {
			sv := Span{From: dates.Format(s.GetEffective().GetStart()), Tier: enum(s.GetTier().String(), "COVERAGE_TIER_"),
				Plan: or(planName[s.GetPlanId()], s.GetPlanId()), COBRA: s.GetCobra()}
			if e := s.GetEffective().GetEnd(); e != nil {
				sv.To = dates.Format(e)
			}
			row.Spans = append(row.Spans, sv)
			latest = s
			switch {
			case dates.In(day, s.GetEffective()):
				current = s
			case dates.Cmp(s.GetEffective().GetStart(), day) > 0:
				if row.Status != "upcoming" {
					row.Status, row.Starts, row.Ends = "upcoming", sv.From, ""
				}
			case row.Status != "upcoming":
				row.Status, row.Ends = "ended", sv.To
			}
		}
		if current != nil {
			row.Status, row.COBRA, row.Starts, row.Ends = "active", current.GetCobra(), "", ""
			if e := current.GetEffective().GetEnd(); e != nil {
				row.Status, row.Ends = "ending", dates.Format(e)
			}
			v.Covered++
			onPlan[current.GetPlanId()]++
			if self {
				tiers[current.GetTier()]++
			}
			switch {
			case current.GetCobra():
				v.COBRA++
			case self:
				v.Subscribers++
			default:
				v.Dependents++
			}
		}

		sub := subscriberNumber(m)
		f := families[sub]
		if f == nil {
			f = &Family{SubscriberNumber: sub}
			families[sub] = f
		}
		if self {
			f.Name = m.GetName().GetLast()
			if t := or2(current, latest); t != nil {
				f.Tier = enum(t.GetTier().String(), "COVERAGE_TIER_")
			}
		}
		f.Members = append(f.Members, row)
	}
	for _, f := range families {
		sort.Slice(f.Members, func(i, j int) bool { return f.Members[i].Card < f.Members[j].Card })
		if f.Name == "" && len(f.Members) > 0 {
			f.Name = lastWord(f.Members[0].Name)
		}
		v.Families = append(v.Families, *f)
	}
	sort.Slice(v.Families, func(i, j int) bool { return v.Families[i].SubscriberNumber < v.Families[j].SubscriberNumber })

	for _, t := range []eligibilityv1.CoverageTier{
		eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE, eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_SPOUSE,
		eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_CHILDREN, eligibilityv1.CoverageTier_COVERAGE_TIER_FAMILY,
	} {
		v.Tiers = append(v.Tiers, TierCount{Tier: enum(t.String(), "COVERAGE_TIER_"), Subscribers: tiers[t]})
	}
	for _, pl := range plans {
		pl.Covered = onPlan[pl.ID]
		v.Plans = append(v.Plans, pl)
	}

	v.Months = months(members, dates.FromTime(now))
	v.Lives = coveredLives(members, plans, dates.FromTime(now))
	if v.Files, err = p.files(ctx, c, st); err != nil {
		return nil, err
	}
	return v, nil
}

// pendingTerms is, by member, the last covered day a full file proposes
// for the customer's members and nobody has approved.
func pendingTerms(st *eligibility.State, customerID string) map[string]string {
	out := map[string]string{}
	for _, pr := range st.Proposals {
		if pr.CustomerID != customerID || pr.Approved {
			continue
		}
		for _, t := range pr.Terms {
			out[t.MemberID] = t.End.Format("2006-01-02")
		}
	}
	return out
}

// plans are the plans of the customer's groups.
func (p *Portal) plans(ctx context.Context, customerID string, day *commonv1.Date) ([]Plan, error) {
	groups, _, err := p.Repos.Groups.List(ctx, store.ListOptions[*customerv1.Group]{PageSize: 1000,
		Where: []store.Cond{store.Eq(customerID, "customerId")}})
	if err != nil {
		return nil, err
	}
	out := []Plan{}
	seen := map[string]bool{}
	for _, g := range groups {
		for _, id := range g.GetPlanIds() {
			if seen[id] {
				continue
			}
			seen[id] = true
			pl, err := p.Repos.Plans.Get(ctx, id)
			if err != nil {
				return nil, err
			}
			if pl.GetCustomerId() != customerID {
				return nil, fmt.Errorf("group %s lists plan %s, which is another customer's", g.GetId(), id)
			}
			out = append(out, planView(pl, day))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func planView(p *planv1.Plan, day *commonv1.Date) Plan {
	v := Plan{
		ID: p.GetId(), Name: p.GetName(), Type: enum(p.GetPlanType().String(), "PLAN_TYPE_"),
		Start: dates.Format(p.GetBenefitPeriod().GetStart()), End: dates.Format(p.GetBenefitPeriod().GetEnd()),
		Tiers: []PlanTier{}, Benefits: []Benefit{},
	}
	var pv *planv1.PlanVersion
	for _, x := range p.GetVersions() {
		if pv == nil || dates.In(day, x.GetEffective()) {
			pv = x
		}
	}
	if pv == nil {
		return v
	}
	tierAt := map[string]int{}
	for _, t := range pv.GetNetworkTiers() {
		tierAt[t.GetId()] = len(v.Tiers)
		v.Tiers = append(v.Tiers, PlanTier{Name: t.GetName(), OutOfNetwork: t.GetIsOutOfNetwork()})
	}
	for _, acc := range pv.GetAccumulators() {
		for _, id := range acc.GetTierIds() {
			i, ok := tierAt[id]
			if !ok {
				continue
			}
			t, limit := &v.Tiers[i], acc.GetLimit().GetCents()
			ded := acc.GetType() == planv1.AccumulatorType_ACCUMULATOR_TYPE_DEDUCTIBLE
			fam := acc.GetLevel() == planv1.AccumulatorLevel_ACCUMULATOR_LEVEL_FAMILY
			switch {
			case ded && fam:
				t.DeductibleFamily = limit
			case ded:
				t.DeductibleIndividual = limit
			case fam:
				t.OOPFamily = limit
			default:
				t.OOPIndividual = limit
			}
		}
	}
	category := map[string]string{}
	for _, c := range pv.GetServiceCategories() {
		category[c.GetId()] = c.GetName()
	}
	for _, b := range pv.GetBenefits() {
		bv := Benefit{Category: or(category[b.GetServiceCategoryId()], b.GetServiceCategoryId()), Shares: make([]*Share, len(v.Tiers))}
		for _, cs := range b.GetCostShares() {
			if i, ok := tierAt[cs.GetTierId()]; ok {
				bv.Shares[i] = &Share{Covered: cs.GetCovered(), Copay: cs.GetCopay().GetCents(),
					CoinsuranceBps: cs.GetCoinsuranceBps(), DeductibleApplies: cs.GetDeductibleApplies()}
			}
		}
		v.Benefits = append(v.Benefits, bv)
	}
	return v
}

var actionNames = map[eligibilityv1.MaintenanceType]string{
	eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_ADDITION:      "added",
	eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_CHANGE:        "changed",
	eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION:   "terminated",
	eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_REINSTATEMENT: "reinstated",
	eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_AUDIT:         "listed",
}

var actionOrder = []string{"added", "changed", "terminated", "reinstated", "listed", "other"}

// files are the 834s received for the customer, newest first: those its
// own employer sender sent, and any other with a member loop naming it as
// sponsor (a COBRA administrator's). Only the customer's loops are
// counted.
func (p *Portal) files(ctx context.Context, c *customerv1.Customer, st *eligibility.State) ([]File, error) {
	// What eligibility did with each member loop.
	inputs, err := p.Elig.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	byInput := map[string]eligibility.Outcome{}
	for _, o := range st.Outcomes {
		byInput[o.InputID] = o
	}
	outcome := map[string]eligibility.Outcome{}
	for _, in := range inputs {
		if id := in.GetEnrollmentTransactionId(); id != "" {
			outcome[id] = byInput[in.GetId()]
		}
	}
	role := map[string]eligibility.Role{}
	for _, s := range eligibility.Senders(c.GetId()) {
		role[s.Sender] = s.Role
	}
	jobs, _, err := p.Repos.EnrollmentIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1000,
		Filter: func(j *ingestionv1.IngestionJob) bool {
			return j.GetKind() == ingestionv1.IngestionKind_INGESTION_KIND_ENROLLMENT
		}})
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].GetId() > jobs[j].GetId() })
	out := []File{}
	for _, j := range jobs {
		sender := j.GetEnvelope().GetSenderId()
		r, known := role[sender]
		if !known {
			// Not one of this customer's senders: nothing of it is shown.
			continue
		}
		loops, _, err := p.Repos.EnrollmentTransactions.List(ctx, store.ListOptions[*eligibilityv1.EnrollmentTransaction]{PageSize: 100_000,
			Where: []store.Cond{store.Eq(j.GetId(), "source", "ingestionJobId")}})
		if err != nil {
			return nil, err
		}
		f := File{ID: j.GetId(), Filename: j.GetOriginalFilename(), Received: j.GetReceivedTime().AsTime().Format(time.RFC3339),
			From: r.String(), Actions: []ActionCount{}, Errors: []string{}, Loops: []Loop{}}
		sort.Slice(loops, func(a, b int) bool {
			x, y := loops[a].GetSource(), loops[b].GetSource()
			if x.GetTransactionSetControlNumber() != y.GetTransactionSetControlNumber() {
				return x.GetTransactionSetControlNumber() < y.GetTransactionSetControlNumber()
			}
			return x.GetFirstSegment() < y.GetFirstSegment()
		})
		counts := map[string]int{}
		for _, t := range loops {
			if !eligibility.IsSponsor(c, t.GetHeader().GetSponsor()) {
				continue
			}
			f.Members++
			action := or(actionNames[t.GetMaintenanceType()], "other")
			counts[action]++
			o := outcome[t.GetId()]
			if o.Status == eligibility.Exception {
				f.Exceptions++
			}
			f.Loops = append(f.Loops, Loop{Name: title(fullName(t.GetName())), Action: action,
				Outcome: strings.ToLower(o.Status), Message: o.Message})
			if t.GetHeader().GetAction() == eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_REPLACE {
				f.Full = true
			}
		}
		// A sender shared with other customers: only its files about this
		// customer.
		if f.Members == 0 && r != eligibility.Employer {
			continue
		}
		for _, a := range actionOrder {
			if counts[a] > 0 {
				f.Actions = append(f.Actions, ActionCount{Action: a, Count: counts[a]})
			}
		}
		switch j.GetStatus() {
		case ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED:
			f.Status = "accepted"
		case ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED:
			f.Status = "rejected"
		default:
			f.Status = "partial"
		}
		if len(j.GetErrors()) > 0 {
			f.Error = j.GetErrors()[0].GetMessage()
		}
		for _, set := range j.GetTransactionSets() {
			for _, e := range set.GetErrors() {
				f.Errors = append(f.Errors, e.GetMessage())
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// medical is a member's medical coverage, earliest first.
func medical(m *eligibilityv1.Member) []*eligibilityv1.CoverageSpan {
	var out []*eligibilityv1.CoverageSpan
	for _, s := range m.GetCoverage() {
		if s.GetLine() == eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL {
			out = append(out, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return dates.Cmp(out[i].GetEffective().GetStart(), out[j].GetEffective().GetStart()) < 0
	})
	return out
}

func subscriberNumber(m *eligibilityv1.Member) string {
	for _, x := range m.GetExternalIds() {
		if x.GetSystem() == "sponsor-subscriber-number" {
			return x.GetValue()
		}
	}
	return m.GetSubscriberId()
}

func fullName(n *commonv1.PersonName) string {
	return strings.TrimSpace(n.GetFirst() + " " + n.GetLast())
}

func lastWord(s string) string {
	w := strings.Fields(s)
	if len(w) == 0 {
		return ""
	}
	return w[len(w)-1]
}

// title turns "MEI CHEN" into "Mei Chen".
func title(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

func enum(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func or2(a, b *eligibilityv1.CoverageSpan) *eligibilityv1.CoverageSpan {
	if a != nil {
		return a
	}
	return b
}
