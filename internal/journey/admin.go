package journey

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
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	remittancev1 "coreadmin/gen/coreadmin/remittance/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/eligibility"
	"coreadmin/internal/sim"
)

// Admin is what Journey's tabs beside Claims show: members, employers,
// the network, stop-loss and files, read straight from each domain's
// stores as they stand in the sandbox. Payments are on the claims
// themselves (Snapshot).
type Admin struct {
	Members   []MemberRow    `json:"members"`
	Employers []EmployerView `json:"employers"`
	Providers []ProviderRow  `json:"providers"`
	Networks  []NetworkRow   `json:"networks"`
	Contracts []ContractRow  `json:"contracts"`
	StopLoss  []PolicyView   `json:"stopLoss"`
	Files     []FileRow      `json:"files"`
}

type MemberRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Card         string `json:"card"`
	Born         string `json:"born"`
	Relationship string `json:"relationship"`
	SubscriberID string `json:"subscriberId"`
	CustomerID   string `json:"customerId"`
	// Medical coverage on the sandbox's date: ACTIVE, TERMINATED (a span
	// ended before it), FUTURE (a span starts after it) or NONE.
	State    string     `json:"state"`
	Coverage []SpanView `json:"coverage"`
}

type SpanView struct {
	Line   string `json:"line"`
	Plan   string `json:"plan"`
	PlanID string `json:"planId"`
	Tier   string `json:"tier"`
	Start  string `json:"start"`
	End    string `json:"end,omitempty"` // empty: open-ended
	COBRA  bool   `json:"cobra,omitempty"`
}

type EmployerView struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	EIN     string `json:"ein"`
	Funding string `json:"funding"`
	Status  string `json:"status"`
	Since   string `json:"since"`
	Place   string `json:"place"`
	// Who may send its enrollment, and as what.
	Senders []SenderView `json:"senders"`
	Groups  []GroupView  `json:"groups"`
	Plans   []PlanView   `json:"plans"`
	// Subscribers with medical coverage on the sandbox's date, by tier.
	Tiers []TierCount `json:"tiers"`
}

type SenderView struct {
	Sender string `json:"sender"`
	Role   string `json:"role"`
}

type GroupView struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Plans []string `json:"plans"`
}

type TierCount struct {
	Tier        string `json:"tier"`
	Subscribers int    `json:"subscribers"`
}

// PlanView is a plan with the version in force on the sandbox's date.
type PlanView struct {
	ID       string        `json:"id"`
	Name     string        `json:"name"`
	Type     string        `json:"type"`
	Start    string        `json:"start"`
	End      string        `json:"end"`
	Version  int32         `json:"version"`
	Versions int           `json:"versions"`
	Tiers    []PlanTier    `json:"tiers"`
	Benefits []BenefitView `json:"benefits"`
	// What happens to a service no benefit matches: PEND or DENY.
	Default string `json:"default"`
}

type PlanTier struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Names of the networks that put a provider in this tier.
	Networks []string `json:"networks"`
	// How a non-contracted provider is paid, for the out-of-network tier.
	Basis string `json:"basis,omitempty"`
	// Limits in cents; zero where the plan has none.
	DeductibleIndividual int64 `json:"deductibleIndividual"`
	DeductibleFamily     int64 `json:"deductibleFamily"`
	OOPIndividual        int64 `json:"oopIndividual"`
	OOPFamily            int64 `json:"oopFamily"`
}

type BenefitView struct {
	Category  string `json:"category"`
	PriorAuth bool   `json:"priorAuth"`
	// One per plan tier, in the tiers' order; nil where the benefit says
	// nothing for that tier.
	Shares []*ShareView `json:"shares"`
	Limits []string     `json:"limits"`
}

type ShareView struct {
	Covered              bool  `json:"covered"`
	Copay                int64 `json:"copay"`
	CoinsuranceBps       int32 `json:"coinsuranceBps"`
	DeductibleApplies    bool  `json:"deductibleApplies"`
	CopayAfterDeductible bool  `json:"copayAfterDeductible"`
}

type ProviderRow struct {
	ID         string   `json:"id"`
	NPI        string   `json:"npi"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Status     string   `json:"status"`
	Taxonomies []string `json:"taxonomies"`
	// Participating in a network on the sandbox's date.
	InNetwork      bool                `json:"inNetwork"`
	TINs           []TINView           `json:"tins"`
	Participations []ParticipationView `json:"participations"`
	Locations      []LocationView      `json:"locations"`
}

type TINView struct {
	TIN         string `json:"tin"`
	BillingName string `json:"billingName"`
	Start       string `json:"start"`
	End         string `json:"end,omitempty"`
}

type ParticipationView struct {
	NetworkID  string `json:"networkId"`
	Network    string `json:"network"`
	TIN        string `json:"tin"`
	ContractID string `json:"contractId"`
	Contract   string `json:"contract"`
	Start      string `json:"start"`
	End        string `json:"end,omitempty"`
	Current    bool   `json:"current"`
}

type LocationView struct {
	Address   string `json:"address"`
	Accepting bool   `json:"accepting"`
}

type NetworkRow struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Vendor string `json:"vendor"`
	// Providers participating on the sandbox's date.
	Providers int `json:"providers"`
}

type ContractRow struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	NetworkID string     `json:"networkId"`
	Network   string     `json:"network"`
	Start     string     `json:"start"`
	End       string     `json:"end,omitempty"`
	Terms     []TermView `json:"terms"`
}

type TermView struct {
	Method string `json:"method"`
	// The fee schedule, percentage or rate the method uses.
	Detail string `json:"detail"`
	// Codes the term is limited to; empty for every service.
	Scope string `json:"scope,omitempty"`
}

type PolicyView struct {
	ID         string `json:"id"`
	CustomerID string `json:"customerId"`
	Customer   string `json:"customer"`
	Carrier    string `json:"carrier"`
	Number     string `json:"number"`
	Start      string `json:"start"`
	End        string `json:"end"`
	// The contract basis as the policy states it.
	RunInMonths    int32  `json:"runInMonths"`
	RunOutMonths   int32  `json:"runOutMonths"`
	UnlimitedRunIn bool   `json:"unlimitedRunIn"`
	Covers         string `json:"covers"`
	// Specific.
	Deductible int64       `json:"deductible"`
	Lasers     []LaserView `json:"lasers"`
	// Aggregate.
	Factors           []FactorView `json:"factors"`
	MinimumAttachment int64        `json:"minimumAttachment"`
	// Exposure and filings as recorded; empty until tracking is built.
	Specific  []SpecificView   `json:"specific"`
	Aggregate []*AggregateView `json:"aggregate"`
	Filings   []FilingView     `json:"filings"`
}

type LaserView struct {
	MemberID   string `json:"memberId"`
	Member     string `json:"member"`
	Deductible int64  `json:"deductible"`
	Note       string `json:"note"`
}

type FactorView struct {
	Tier    string `json:"tier"`
	Monthly int64  `json:"monthly"`
}

type SpecificView struct {
	MemberID   string `json:"memberId"`
	Member     string `json:"member"`
	Paid       int64  `json:"paid"`
	Deductible int64  `json:"deductible"`
	Excess     int64  `json:"excess"`
	Reported   bool   `json:"reported"`
}

type AggregateView struct {
	Attachment int64 `json:"attachment"`
	Paid       int64 `json:"paid"`
	Excess     int64 `json:"excess"`
}

type FilingView struct {
	Type      string `json:"type"`
	Member    string `json:"member"`
	Status    string `json:"status"`
	Requested int64  `json:"requested"`
}

// FileRow is one file received or sent.
type FileRow struct {
	// 837, 834, 999 or 835.
	Kind string `json:"kind"`
	// IN or OUT.
	Direction string `json:"direction"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	// The other party: the sender of an inbound file, the receiver of an
	// outbound one.
	Party  string `json:"party"`
	ICN    string `json:"icn,omitempty"`
	Time   string `json:"time"`
	Status string `json:"status"`
	// Records loaded and not loaded, for an inbound file; claims reported,
	// for an 835.
	Loaded   int64 `json:"loaded"`
	Rejected int64 `json:"rejected"`
	// For a 999: the file it answers.
	Answers string `json:"answers,omitempty"`
}

func enum(s, prefix string) string { return strings.TrimPrefix(s, prefix) }

func personName(n *commonv1.PersonName) string {
	return strings.TrimSpace(n.GetFirst() + " " + n.GetLast())
}

func listAll[T store.Resource](ctx context.Context, s store.Store[T]) ([]T, error) {
	all, _, err := s.List(ctx, store.ListOptions[T]{PageSize: 10_000})
	return all, err
}

// GetAdmin reads everything the tabs beside Claims show.
func GetAdmin(s *sim.Sim) (*Admin, error) {
	ctx := context.Background()
	today := dates.FromTime(s.Now())
	r := s.Repos
	a := &Admin{Files: []FileRow{}}

	plans, err := listAll(ctx, r.Plans)
	if err != nil {
		return nil, err
	}
	planName := map[string]string{}
	for _, p := range plans {
		planName[p.GetId()] = p.GetName()
	}
	networks, err := listAll(ctx, r.Networks)
	if err != nil {
		return nil, err
	}
	networkName := map[string]string{}
	for _, n := range networks {
		networkName[n.GetId()] = n.GetName()
	}
	contracts, err := listAll(ctx, r.Contracts)
	if err != nil {
		return nil, err
	}
	contractName := map[string]string{}
	for _, c := range contracts {
		contractName[c.GetId()] = c.GetName()
	}

	// Members.
	members, err := listAll(ctx, r.Members)
	if err != nil {
		return nil, err
	}
	sort.Slice(members, func(i, j int) bool { return members[i].GetId() < members[j].GetId() })
	memberName := map[string]string{}
	// Subscribers covered today, by customer and tier.
	tiers := map[string]map[eligibilityv1.CoverageTier]int{}
	for _, m := range members {
		memberName[m.GetId()] = personName(m.GetName())
		row := MemberRow{
			ID: m.GetId(), Name: personName(m.GetName()), Card: m.GetCardId(), Born: dates.Format(m.GetBirthDate()),
			Relationship: enum(m.GetRelationship().String(), "RELATIONSHIP_"),
			SubscriberID: m.GetSubscriberId(), CustomerID: m.GetCustomerId(),
			State: "NONE", Coverage: []SpanView{},
		}
		for _, sp := range m.GetCoverage() {
			row.Coverage = append(row.Coverage, SpanView{
				Line: enum(sp.GetLine().String(), "COVERAGE_LINE_"), Plan: planName[sp.GetPlanId()], PlanID: sp.GetPlanId(),
				Tier:  enum(sp.GetTier().String(), "COVERAGE_TIER_"),
				Start: dates.Format(sp.GetEffective().GetStart()), End: dates.Format(sp.GetEffective().GetEnd()), COBRA: sp.GetCobra(),
			})
			if sp.GetLine() != eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL {
				continue
			}
			eff := sp.GetEffective()
			switch {
			case dates.In(today, eff):
				row.State = "ACTIVE"
				if m.GetId() == m.GetSubscriberId() {
					if tiers[m.GetCustomerId()] == nil {
						tiers[m.GetCustomerId()] = map[eligibilityv1.CoverageTier]int{}
					}
					tiers[m.GetCustomerId()][sp.GetTier()]++
				}
			case row.State == "ACTIVE":
			case eff.GetEnd() != nil && dates.Cmp(eff.GetEnd(), today) < 0:
				row.State = "TERMINATED"
			case row.State == "NONE":
				row.State = "FUTURE"
			}
		}
		a.Members = append(a.Members, row)
	}

	// Employers.
	customers, err := listAll(ctx, r.Customers)
	if err != nil {
		return nil, err
	}
	sort.Slice(customers, func(i, j int) bool { return customers[i].GetName() < customers[j].GetName() })
	groups, err := listAll(ctx, r.Groups)
	if err != nil {
		return nil, err
	}
	customerName := map[string]string{}
	for _, c := range customers {
		customerName[c.GetId()] = c.GetName()
		a.Employers = append(a.Employers, employerView(c, groups, plans, networkName, planName, tiers[c.GetId()], today))
	}

	// Network.
	providers, err := listAll(ctx, r.Providers)
	if err != nil {
		return nil, err
	}
	sort.Slice(providers, func(i, j int) bool { return providerLabel(providers[i]) < providerLabel(providers[j]) })
	participating := map[string]int{}
	for _, p := range providers {
		row := ProviderRow{
			ID: p.GetId(), NPI: p.GetNpi(), Name: providerLabel(p),
			Kind: enum(p.GetKind().String(), "PROVIDER_KIND_"), Status: enum(p.GetStatus().String(), "PROVIDER_STATUS_"),
			Taxonomies: append([]string{}, p.GetTaxonomyCodes()...),
			TINs:       []TINView{}, Participations: []ParticipationView{}, Locations: []LocationView{},
		}
		for _, t := range p.GetTins() {
			row.TINs = append(row.TINs, TINView{TIN: t.GetTin(), BillingName: t.GetBillingName(),
				Start: dates.Format(t.GetEffective().GetStart()), End: dates.Format(t.GetEffective().GetEnd())})
		}
		in := map[string]bool{}
		for _, pt := range p.GetParticipations() {
			cur := dates.In(today, pt.GetEffective())
			if cur {
				row.InNetwork = true
				in[pt.GetNetworkId()] = true
			}
			row.Participations = append(row.Participations, ParticipationView{
				NetworkID: pt.GetNetworkId(), Network: networkName[pt.GetNetworkId()], TIN: pt.GetTin(),
				ContractID: pt.GetContractId(), Contract: contractName[pt.GetContractId()],
				Start: dates.Format(pt.GetEffective().GetStart()), End: dates.Format(pt.GetEffective().GetEnd()), Current: cur,
			})
		}
		for id := range in {
			participating[id]++
		}
		for _, l := range p.GetLocations() {
			ad := l.GetAddress()
			row.Locations = append(row.Locations, LocationView{
				Address:   strings.Join(append(append([]string{}, ad.GetLines()...), ad.GetCity()+", "+ad.GetState()+" "+ad.GetPostalCode()), ", "),
				Accepting: l.GetAcceptingNewPatients(),
			})
		}
		a.Providers = append(a.Providers, row)
	}
	for _, n := range networks {
		a.Networks = append(a.Networks, NetworkRow{ID: n.GetId(), Name: n.GetName(), Kind: enum(n.GetKind().String(), "NETWORK_KIND_"),
			Vendor: n.GetVendor(), Providers: participating[n.GetId()]})
	}
	for _, c := range contracts {
		row := ContractRow{ID: c.GetId(), Name: c.GetName(), NetworkID: c.GetNetworkId(), Network: networkName[c.GetNetworkId()],
			Start: dates.Format(c.GetEffective().GetStart()), End: dates.Format(c.GetEffective().GetEnd()), Terms: []TermView{}}
		for _, t := range c.GetTerms() {
			row.Terms = append(row.Terms, termView(t))
		}
		a.Contracts = append(a.Contracts, row)
	}

	// Stop-loss.
	if a.StopLoss, err = stopLoss(ctx, s, customerName, memberName); err != nil {
		return nil, err
	}

	// Files.
	if a.Files, err = files(ctx, s); err != nil {
		return nil, err
	}
	return a, nil
}

func providerLabel(p *providerv1.Provider) string {
	if p.GetOrganizationName() != "" {
		return p.GetOrganizationName()
	}
	return personName(p.GetName())
}

func employerView(c *customerv1.Customer, groups []*customerv1.Group, plans []*planv1.Plan, networkName, planName map[string]string, tiers map[eligibilityv1.CoverageTier]int, today *commonv1.Date) EmployerView {
	v := EmployerView{
		ID: c.GetId(), Name: c.GetName(), EIN: c.GetEin(),
		Funding: enum(c.GetFundingType().String(), "FUNDING_TYPE_"), Status: enum(c.GetStatus().String(), "CUSTOMER_STATUS_"),
		Since:   dates.Format(c.GetEffective().GetStart()),
		Place:   strings.TrimSuffix(c.GetAddress().GetCity()+", "+c.GetAddress().GetState(), ", "),
		Senders: []SenderView{}, Groups: []GroupView{}, Plans: []PlanView{}, Tiers: []TierCount{},
	}
	for _, s := range eligibility.Senders(c.GetId()) {
		v.Senders = append(v.Senders, SenderView{Sender: s.Sender, Role: s.Role.String()})
	}
	for _, g := range groups {
		if g.GetCustomerId() != c.GetId() {
			continue
		}
		gv := GroupView{ID: g.GetId(), Name: g.GetName(), Plans: []string{}}
		for _, id := range g.GetPlanIds() {
			gv.Plans = append(gv.Plans, planName[id])
		}
		v.Groups = append(v.Groups, gv)
	}
	for _, p := range plans {
		if p.GetCustomerId() == c.GetId() {
			v.Plans = append(v.Plans, planView(p, networkName, today))
		}
	}
	for _, t := range []eligibilityv1.CoverageTier{
		eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE, eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_SPOUSE,
		eligibilityv1.CoverageTier_COVERAGE_TIER_EMPLOYEE_CHILDREN, eligibilityv1.CoverageTier_COVERAGE_TIER_FAMILY,
	} {
		v.Tiers = append(v.Tiers, TierCount{Tier: enum(t.String(), "COVERAGE_TIER_"), Subscribers: tiers[t]})
	}
	return v
}

// planView shows the plan version in force today, or the latest one if
// none is.
func planView(p *planv1.Plan, networkName map[string]string, today *commonv1.Date) PlanView {
	v := PlanView{
		ID: p.GetId(), Name: p.GetName(), Type: enum(p.GetPlanType().String(), "PLAN_TYPE_"),
		Start: dates.Format(p.GetBenefitPeriod().GetStart()), End: dates.Format(p.GetBenefitPeriod().GetEnd()),
		Versions: len(p.GetVersions()), Tiers: []PlanTier{}, Benefits: []BenefitView{},
	}
	var pv *planv1.PlanVersion
	for _, x := range p.GetVersions() {
		if pv == nil || dates.In(today, x.GetEffective()) {
			pv = x
		}
	}
	if pv == nil {
		return v
	}
	v.Version = pv.GetVersion()
	v.Default = enum(pv.GetDefaultBehavior().String(), "DEFAULT_BENEFIT_BEHAVIOR_")
	tierAt := map[string]int{}
	for _, t := range pv.GetNetworkTiers() {
		pt := PlanTier{ID: t.GetId(), Name: t.GetName(), Networks: []string{}}
		for _, id := range t.GetNetworkIds() {
			pt.Networks = append(pt.Networks, networkName[id])
		}
		if b := t.GetNonContractedBasis(); b != nil {
			pt.Basis = basis(enum(b.GetMethod().String(), "REIMBURSEMENT_METHOD_"), b.GetPercentBps())
		}
		tierAt[t.GetId()] = len(v.Tiers)
		v.Tiers = append(v.Tiers, pt)
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
		bv := BenefitView{Category: category[b.GetServiceCategoryId()], PriorAuth: b.GetRequiresPriorAuth(),
			Shares: make([]*ShareView, len(v.Tiers)), Limits: []string{}}
		if bv.Category == "" {
			bv.Category = b.GetServiceCategoryId()
		}
		for _, cs := range b.GetCostShares() {
			if i, ok := tierAt[cs.GetTierId()]; ok {
				bv.Shares[i] = &ShareView{Covered: cs.GetCovered(), Copay: cs.GetCopay().GetCents(), CoinsuranceBps: cs.GetCoinsuranceBps(),
					DeductibleApplies: cs.GetDeductibleApplies(), CopayAfterDeductible: cs.GetCopayAfterDeductible()}
			}
		}
		for _, l := range b.GetLimits() {
			unit := strings.ToLower(enum(l.GetType().String(), "BENEFIT_LIMIT_TYPE_"))
			if l.GetType() == planv1.BenefitLimitType_BENEFIT_LIMIT_TYPE_DOLLARS {
				bv.Limits = append(bv.Limits, fmt.Sprintf("$%d per period", l.GetMaxAmount().GetCents()/100))
			} else {
				bv.Limits = append(bv.Limits, fmt.Sprintf("%d %s per period", l.GetMaxCount(), unit))
			}
		}
		v.Benefits = append(v.Benefits, bv)
	}
	return v
}

func basis(method string, bps int32) string {
	pct := fmt.Sprintf("%g%%", float64(bps)/100)
	switch method {
	case "PERCENT_OF_MEDICARE":
		return pct + " of Medicare"
	case "PERCENT_OF_BILLED":
		return pct + " of billed"
	case "FEE_SCHEDULE":
		return "Fee schedule"
	case "UCR":
		return "UCR"
	}
	return strings.ToLower(strings.ReplaceAll(method, "_", " "))
}

func termView(t *providerv1.ContractTerm) TermView {
	method := enum(t.GetMethod().String(), "CONTRACT_METHOD_")
	v := TermView{Method: method}
	switch t.GetMethod() {
	case providerv1.ContractMethod_CONTRACT_METHOD_FEE_SCHEDULE:
		v.Detail = t.GetFeeScheduleId()
	case providerv1.ContractMethod_CONTRACT_METHOD_PERCENT_OF_BILLED, providerv1.ContractMethod_CONTRACT_METHOD_PERCENT_OF_MEDICARE:
		v.Detail = fmt.Sprintf("%g%%", float64(t.GetPercentBps())/100)
	default:
		if t.GetRate() != nil {
			v.Detail = fmt.Sprintf("$%.2f", float64(t.GetRate().GetCents())/100)
		}
	}
	var scope []string
	if n := len(t.GetProcedureCodes()); n > 0 {
		scope = append(scope, "procedures "+strings.Join(t.GetProcedureCodes(), ", "))
	}
	if n := len(t.GetRevenueCodes()); n > 0 {
		scope = append(scope, "revenue codes "+strings.Join(t.GetRevenueCodes(), ", "))
	}
	if n := len(t.GetPlaceOfServiceCodes()); n > 0 {
		scope = append(scope, "POS "+strings.Join(t.GetPlaceOfServiceCodes(), ", "))
	}
	v.Scope = strings.Join(scope, "; ")
	return v
}

func stopLoss(ctx context.Context, s *sim.Sim, customerName, memberName map[string]string) ([]PolicyView, error) {
	r := s.Repos
	policies, err := listAll(ctx, r.StopLossPolicies)
	if err != nil {
		return nil, err
	}
	specific, err := listAll(ctx, r.SpecificExposures)
	if err != nil {
		return nil, err
	}
	aggregate, err := listAll(ctx, r.AggregateExposures)
	if err != nil {
		return nil, err
	}
	filings, err := listAll(ctx, r.StopLossFilings)
	if err != nil {
		return nil, err
	}
	out := []PolicyView{}
	for _, p := range policies {
		var covers []string
		if p.GetCoversMedical() {
			covers = append(covers, "medical")
		}
		if p.GetCoversPharmacy() {
			covers = append(covers, "pharmacy")
		}
		v := PolicyView{
			ID: p.GetId(), CustomerID: p.GetCustomerId(), Customer: customerName[p.GetCustomerId()],
			Carrier: p.GetCarrierName(), Number: p.GetPolicyNumber(),
			Start: dates.Format(p.GetPolicyPeriod().GetStart()), End: dates.Format(p.GetPolicyPeriod().GetEnd()),
			RunInMonths: p.GetContractBasis().GetRunInMonths(), RunOutMonths: p.GetContractBasis().GetRunOutMonths(),
			UnlimitedRunIn: p.GetContractBasis().GetUnlimitedRunIn(), Covers: strings.Join(covers, " + "),
			Deductible: p.GetSpecific().GetDeductible().GetCents(), MinimumAttachment: p.GetAggregate().GetMinimumAttachment().GetCents(),
			Lasers: []LaserView{}, Factors: []FactorView{}, Specific: []SpecificView{}, Aggregate: []*AggregateView{}, Filings: []FilingView{},
		}
		for _, l := range p.GetSpecific().GetLasers() {
			v.Lasers = append(v.Lasers, LaserView{MemberID: l.GetMemberId(), Member: memberName[l.GetMemberId()], Deductible: l.GetDeductible().GetCents(), Note: l.GetNote()})
		}
		for _, f := range p.GetAggregate().GetAttachmentFactors() {
			v.Factors = append(v.Factors, FactorView{Tier: enum(f.GetTier().String(), "COVERAGE_TIER_"), Monthly: f.GetMonthlyFactor().GetCents()})
		}
		for _, e := range specific {
			if e.GetPolicyId() == p.GetId() {
				v.Specific = append(v.Specific, SpecificView{MemberID: e.GetMemberId(), Member: memberName[e.GetMemberId()],
					Paid: e.GetEligiblePaid().GetCents(), Deductible: e.GetDeductible().GetCents(), Excess: e.GetExcess().GetCents(), Reported: e.GetReportedToCarrier()})
			}
		}
		for _, e := range aggregate {
			if e.GetPolicyId() == p.GetId() {
				v.Aggregate = append(v.Aggregate, &AggregateView{Attachment: e.GetAttachmentPoint().GetCents(), Paid: e.GetEligiblePaid().GetCents(), Excess: e.GetExcess().GetCents()})
			}
		}
		for _, f := range filings {
			if f.GetPolicyId() == p.GetId() {
				v.Filings = append(v.Filings, FilingView{Type: enum(f.GetType().String(), "FILING_TYPE_"), Member: memberName[f.GetMemberId()],
					Status: enum(f.GetStatus().String(), "FILING_STATUS_"), Requested: f.GetAmountRequested().GetCents()})
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// files lists every file received and sent, newest first: 837s and 834s
// in, the 999 answering each, and 835s out.
func files(ctx context.Context, s *sim.Sim) ([]FileRow, error) {
	r := s.Repos
	out := []FileRow{}
	inbound := func(kind string, jobs []*ingestionv1.IngestionJob) {
		for _, j := range jobs {
			at := j.GetReceivedTime().AsTime().Format(time.RFC3339)
			out = append(out, FileRow{
				Kind: kind, Direction: "IN", ID: j.GetId(), Name: j.GetOriginalFilename(),
				Party: j.GetEnvelope().GetSenderId(), ICN: j.GetEnvelope().GetInterchangeControlNumber(), Time: at,
				Status: enum(j.GetStatus().String(), "INGESTION_STATUS_"),
				Loaded: j.GetCounts().GetRecordsCreated(), Rejected: j.GetCounts().GetRecordsErrored(),
			})
			if j.GetAcknowledgmentUri() == "" {
				continue
			}
			accepted, rejected := int64(0), int64(0)
			for _, ts := range j.GetTransactionSets() {
				if ts.GetAccepted() {
					accepted++
				} else {
					rejected++
				}
			}
			out = append(out, FileRow{
				Kind: "999", Direction: "OUT", ID: j.GetId(), Name: j.GetOriginalFilename() + ".999",
				Party: j.GetEnvelope().GetSenderId(), Time: at, Status: "SENT",
				Loaded: accepted, Rejected: rejected, Answers: j.GetOriginalFilename(),
			})
		}
	}
	claimJobs, err := listAll(ctx, r.ClaimIngestionJobs)
	if err != nil {
		return nil, err
	}
	inbound("837", claimJobs)
	enrollmentJobs, err := listAll(ctx, r.EnrollmentIngestionJobs)
	if err != nil {
		return nil, err
	}
	inbound("834", enrollmentJobs)
	remits, err := listAll(ctx, r.Remittances)
	if err != nil {
		return nil, err
	}
	for _, m := range remits {
		if m.GetPreview() {
			continue
		}
		out = append(out, remitFile(m))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time > out[j].Time })
	return out, nil
}

func remitFile(m *remittancev1.Remittance) FileRow {
	return FileRow{
		Kind: "835", Direction: "OUT", ID: m.GetId(), Name: m.GetPayment().GetTraceNumber(),
		Party: m.GetPayee().GetName(), Time: m.GetCreatedTime().AsTime().Format(time.RFC3339),
		Status: "SENT", Loaded: int64(len(m.GetClaims())),
	}
}

// MemberAccumulators is the accumulator view of the family a member
// belongs to.
func MemberAccumulators(s *sim.Sim, memberID string) (sim.AccumulatorsView, error) {
	return s.FamilyAccumulators(memberID)
}
