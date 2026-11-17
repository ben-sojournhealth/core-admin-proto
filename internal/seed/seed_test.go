package seed_test

import (
	"context"
	"testing"

	"coreadmin/internal/repo"
	"coreadmin/internal/seed"
	"coreadmin/internal/store"
	seeddata "coreadmin/seed"
)

// TestSeedLoadsAndLinks loads the repo's seed fixtures (strict protojson)
// and checks that references between them resolve.
func TestSeedLoadsAndLinks(t *testing.T) {
	ctx := context.Background()
	r := repo.NewInMemory()
	n, err := seed.Load(ctx, r, seeddata.FS)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no seed resources loaded")
	}

	mustExist := func(what, id string, get func(context.Context, string) error) {
		t.Helper()
		if err := get(ctx, id); err != nil {
			t.Errorf("%s %q: %v", what, id, err)
		}
	}
	customer := func(ctx context.Context, id string) error { _, err := r.Customers.Get(ctx, id); return err }
	plan := func(ctx context.Context, id string) error { _, err := r.Plans.Get(ctx, id); return err }
	group := func(ctx context.Context, id string) error { _, err := r.Groups.Get(ctx, id); return err }
	member := func(ctx context.Context, id string) error { _, err := r.Members.Get(ctx, id); return err }
	network := func(ctx context.Context, id string) error { _, err := r.Networks.Get(ctx, id); return err }
	contract := func(ctx context.Context, id string) error { _, err := r.Contracts.Get(ctx, id); return err }
	feeSchedule := func(ctx context.Context, id string) error { _, err := r.FeeSchedules.Get(ctx, id); return err }

	plans := listAll(t, r.Plans)
	for _, p := range plans {
		mustExist("plan customer", p.GetCustomerId(), customer)
		for _, v := range p.GetVersions() {
			accs := map[string]bool{}
			tiers := map[string]bool{}
			cats := map[string]bool{}
			for _, t := range v.GetNetworkTiers() {
				tiers[t.GetId()] = true
				for _, n := range t.GetNetworkIds() {
					mustExist("tier network", n, network)
				}
			}
			for _, a := range v.GetAccumulators() {
				accs[a.GetId()] = true
			}
			for _, a := range v.GetAccumulators() {
				for _, tid := range a.GetTierIds() {
					if !tiers[tid] {
						t.Errorf("plan %s accumulator %s: unknown tier %s", p.GetId(), a.GetId(), tid)
					}
				}
				if id := a.GetIndividualAccumulatorId(); id != "" && !accs[id] {
					t.Errorf("plan %s accumulator %s: unknown individual accumulator %s", p.GetId(), a.GetId(), id)
				}
			}
			for _, c := range v.GetServiceCategories() {
				cats[c.GetId()] = true
			}
			for _, b := range v.GetBenefits() {
				if !cats[b.GetServiceCategoryId()] {
					t.Errorf("plan %s: benefit for unknown category %s", p.GetId(), b.GetServiceCategoryId())
				}
				for _, cs := range b.GetCostShares() {
					if !tiers[cs.GetTierId()] {
						t.Errorf("plan %s: cost share for unknown tier %s", p.GetId(), cs.GetTierId())
					}
				}
			}
		}
	}

	members := listAll(t, r.Members)
	for _, m := range members {
		mustExist("member customer", m.GetCustomerId(), customer)
		mustExist("member subscriber", m.GetSubscriberId(), member)
		for _, c := range m.GetCoverage() {
			mustExist("coverage plan", c.GetPlanId(), plan)
			mustExist("coverage group", c.GetGroupId(), group)
		}
	}

	providers := listAll(t, r.Providers)
	for _, p := range providers {
		for _, np := range p.GetParticipations() {
			mustExist("participation network", np.GetNetworkId(), network)
			mustExist("participation contract", np.GetContractId(), contract)
		}
	}

	contracts := listAll(t, r.Contracts)
	for _, c := range contracts {
		for _, term := range c.GetTerms() {
			if id := term.GetFeeScheduleId(); id != "" {
				mustExist("contract fee schedule", id, feeSchedule)
			}
		}
	}

	policies := listAll(t, r.StopLossPolicies)
	for _, p := range policies {
		mustExist("stop-loss customer", p.GetCustomerId(), customer)
		for _, l := range p.GetSpecific().GetLasers() {
			mustExist("laser member", l.GetMemberId(), member)
		}
	}

	claims := listAll(t, r.Claims)
	for _, c := range claims {
		mustExist("claim customer", c.GetCustomerId(), customer)
	}
}

func listAll[T store.Resource](t *testing.T, s store.Store[T]) []T {
	t.Helper()
	items, next, err := s.List(context.Background(), store.ListOptions[T]{PageSize: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if next != "" {
		t.Fatal("listAll: more than one page")
	}
	return items
}
