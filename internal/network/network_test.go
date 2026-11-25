package network_test

import (
	"context"
	"testing"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	networkv1 "coreadmin/gen/coreadmin/network/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/network"
)

func d(y, m, day int32) *commonv1.Date { return &commonv1.Date{Year: y, Month: m, Day: day} }

func from(start *commonv1.Date) *commonv1.DateRange { return &commonv1.DateRange{Start: start} }

// A network places a provider by its components, in order: the first
// underlying network the provider participates in gives the tier and the
// participation whose contract prices the claim.
func TestProviderPlaceInANetwork(t *testing.T) {
	r := repotest.New(t)
	ctx := context.Background()
	var api network.API = network.New(r)

	for npi, parts := range map[string][][3]string{
		// In all three underlying networks.
		"1": {{"leased", "t1", "ctr_leased"}, {"wrap", "t1", "ctr_wrap"}, {"direct", "t1", "ctr_direct"}},
		// In the wrap network only, and under another TIN in direct.
		"2": {{"wrap", "t1", "ctr_wrap"}, {"direct", "t2", "ctr_direct"}},
		// In an underlying network that is no part of the network.
		"3": {{"elsewhere", "t1", "ctr_x"}},
	} {
		if _, err := r.Providers.Create(ctx, &providerv1.Provider{Id: "p_" + npi, Npi: npi}); err != nil {
			t.Fatal(err)
		}
		for _, x := range parts {
			if _, err := r.Participations.Create(ctx, &providerv1.NetworkParticipation{
				Id: npi + "/" + x[0] + "/" + x[1], Npi: npi, UnderlyingNetworkId: x[0], Tin: x[1], ContractId: x[2],
				Periods: []*commonv1.DateRange{from(d(2026, 1, 1))},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := r.Networks.Create(ctx, &providerv1.Network{Id: "mainline", Components: []*providerv1.NetworkComponent{
		{UnderlyingNetworkId: "direct", TierId: "TIER1"},
		{UnderlyingNetworkId: "wrap", TierId: "TIER2", Effective: from(d(2026, 7, 1))},
		{UnderlyingNetworkId: "leased", TierId: "TIER2"},
	}}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, npi, tin, network string
		on                      *commonv1.Date
		tier, contract          string
	}{
		{"first component wins, whatever the participations' order", "1", "t1", "mainline", d(2026, 8, 1), "TIER1", "ctr_direct"},
		{"next component the provider is in", "2", "t1", "mainline", d(2026, 8, 1), "TIER2", "ctr_wrap"},
		{"the billed TIN decides", "2", "t2", "mainline", d(2026, 8, 1), "TIER1", "ctr_direct"},
		{"a component not yet part of the network", "2", "t1", "mainline", d(2026, 3, 1), "", ""},
		{"before the participation", "1", "t1", "mainline", d(2025, 12, 1), "", ""},
		{"in none of the network's underlying networks", "3", "t1", "mainline", d(2026, 8, 1), "", ""},
		{"no network asked about", "1", "t1", "", d(2026, 8, 1), "", ""},
	} {
		res, err := api.GetProvider(ctx, &networkv1.GetProviderRequest{Npi: tc.npi, Tin: tc.tin, On: tc.on, NetworkId: tc.network})
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.GetTierId() != tc.tier || res.GetParticipation().GetContractId() != tc.contract {
			t.Errorf("%s: tier %q contract %q, want %q %q", tc.name, res.GetTierId(), res.GetParticipation().GetContractId(), tc.tier, tc.contract)
		}
	}

	// The participations themselves are still reported, in or out of the
	// network.
	if res, _ := api.GetProvider(ctx, &networkv1.GetProviderRequest{Npi: "3", Tin: "t1", On: d(2026, 8, 1), NetworkId: "mainline"}); len(res.GetParticipations()) != 1 {
		t.Errorf("participations outside the network: %v", res)
	}
	if res, err := api.GetProvider(ctx, &networkv1.GetProviderRequest{Npi: "9", NetworkId: "mainline", On: d(2026, 8, 1)}); err != nil || res.GetProvider() != nil {
		t.Errorf("unknown NPI: %v %v", res, err)
	}
	if _, err := api.GetProvider(ctx, &networkv1.GetProviderRequest{Npi: "1", NetworkId: "nope", On: d(2026, 8, 1)}); !network.NotFound(err) {
		t.Errorf("unknown network: %v", err)
	}
}
