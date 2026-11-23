package adjudication

import (
	"testing"

	planv1 "coreadmin/gen/coreadmin/plan/v1"
)

// The network says which tier a provider falls in; the plan's tier is the
// one with that id, or its out-of-network tier when the network places the
// provider in none.
func TestSelectTier(t *testing.T) {
	v := &planv1.PlanVersion{NetworkTiers: []*planv1.NetworkTier{
		{Id: "TIER1"}, {Id: "TIER2"}, {Id: "OON", IsOutOfNetwork: true},
	}}
	for networkTier, want := range map[string]string{"TIER1": "TIER1", "TIER2": "TIER2", "": "OON"} {
		if got := selectTier(v, networkTier).GetId(); got != want {
			t.Errorf("network tier %q: plan tier %q, want %q", networkTier, got, want)
		}
	}
	// A network tier the plan has no cost sharing for, and a provider
	// placed in a tier is never matched to the out-of-network one by id.
	for _, networkTier := range []string{"TIER3", "OON"} {
		if got := selectTier(v, networkTier); got != nil {
			t.Errorf("network tier %q: plan tier %q, want none", networkTier, got.GetId())
		}
	}
	if got := selectTier(&planv1.PlanVersion{NetworkTiers: v.GetNetworkTiers()[:2]}, ""); got != nil {
		t.Errorf("no out-of-network tier: got %q", got.GetId())
	}
}
