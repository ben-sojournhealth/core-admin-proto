package servicedefs_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/claims/servicedefs"
	"coreadmin/internal/claims/servicedefsdemo"
	"coreadmin/internal/common/repo/repotest"
)

func day(y, m, d int32) *commonv1.Date { return &commonv1.Date{Year: y, Month: m, Day: d} }

func seeded(t *testing.T) *servicedefs.Service {
	t.Helper()
	svc, err := servicedefsdemo.NewIn(context.Background(), repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func matchIDs(t *testing.T, svc *servicedefs.Service, line *claimsv1.MatchServiceDefinitionsRequest) []string {
	t.Helper()
	res, err := svc.MatchServiceDefinitions(context.Background(), line)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, m := range res.GetMatches() {
		ids = append(ids, m.GetDefinition().GetId())
		if len(m.GetMatchedEntries()) == 0 {
			t.Errorf("%s matched with no entries", m.GetDefinition().GetId())
		}
	}
	return ids
}

func same(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A line is in a definition when every code system the definition checks
// has one of the line's codes; a line can be in several.
func TestMatchChecksEverySystemTheDefinitionUses(t *testing.T) {
	svc := seeded(t)
	for _, tc := range []struct {
		name string
		line *claimsv1.MatchServiceDefinitionsRequest
		want []string
	}{
		{"office visit in an office", &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "99213", PlaceOfServiceCode: "11"}, []string{"office-visit"}},
		{"the same code in the ER is an ER service only", &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "99213", PlaceOfServiceCode: "23"}, []string{"emergency-room"}},
		{"a scan in the ER is both", &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "70551", PlaceOfServiceCode: "23"}, []string{"advanced-imaging", "emergency-room"}},
		{"a revenue code", &claimsv1.MatchServiceDefinitionsRequest{RevenueCode: "0120"}, []string{"inpatient-facility"}},
		{"nothing lists it", &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "97110", PlaceOfServiceCode: "11"}, []string{}},
		{"a shorter code is not inside a range of longer ones", &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "7045"}, []string{}},
	} {
		tc.line.DateOfService = day(2026, 3, 2)
		if got := matchIDs(t, svc, tc.line); !same(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
	if _, err := svc.MatchServiceDefinitions(context.Background(), &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "99213"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("no date of service: %v", err)
	}
}

// Membership is as of the date of service: place of service 10 counts for
// an office visit only from 2022.
func TestMatchUsesTheEntriesInEffectOnTheDateOfService(t *testing.T) {
	svc := seeded(t)
	line := func(on *commonv1.Date) *claimsv1.MatchServiceDefinitionsRequest {
		return &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "99213", PlaceOfServiceCode: "10", DateOfService: on}
	}
	if got := matchIDs(t, svc, line(day(2021, 12, 31))); len(got) != 0 {
		t.Errorf("before the entry starts: %v", got)
	}
	if got := matchIDs(t, svc, line(day(2022, 1, 1))); !same(got, []string{"office-visit"}) {
		t.Errorf("on the day it starts: %v", got)
	}
	ctx := context.Background()
	res, err := svc.GetServiceDefinition(ctx, &claimsv1.GetServiceDefinitionRequest{Id: "office-visit", EffectiveOn: day(2021, 6, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.GetDefinition().GetEntries()); n != 3 {
		t.Errorf("entries in effect in 2021: %d, want 3", n)
	}

	// A definition whose entries have all ended matches nothing, rather
	// than everything.
	d := &claimsv1.ServiceDefinition{Id: "retired", Name: "Retired", Entries: []*claimsv1.CodeEntry{{
		System: claimsv1.CodeSystem_CODE_SYSTEM_PROCEDURE, Start: "99213",
		Effective: &commonv1.DateRange{End: day(2020, 12, 31)},
	}}}
	if _, err := svc.CreateServiceDefinition(ctx, &claimsv1.CreateServiceDefinitionRequest{Definition: d}); err != nil {
		t.Fatal(err)
	}
	if got := matchIDs(t, svc, &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "99213", PlaceOfServiceCode: "11", DateOfService: day(2026, 3, 2)}); !same(got, []string{"office-visit"}) {
		t.Errorf("after an entry ends: %v", got)
	}
}

// An update names the revision it read; a second writer on the same
// revision is refused, and the change shows up in matching.
func TestUpdateReplacesTheDefinitionAtTheRevisionRead(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	get := func() *claimsv1.ServiceDefinition {
		res, err := svc.GetServiceDefinition(ctx, &claimsv1.GetServiceDefinitionRequest{Id: "advanced-imaging"})
		if err != nil {
			t.Fatal(err)
		}
		return res.GetDefinition()
	}
	a, b := get(), get()
	a.Entries = append(a.Entries, &claimsv1.CodeEntry{System: claimsv1.CodeSystem_CODE_SYSTEM_PROCEDURE, Start: " 78815 "})
	res, err := svc.UpdateServiceDefinition(ctx, &claimsv1.UpdateServiceDefinitionRequest{Definition: a})
	if err != nil {
		t.Fatal(err)
	}
	last := res.GetDefinition().GetEntries()[3]
	if res.GetDefinition().GetAudit().GetRevision() != 2 || last.GetStart() != "78815" || last.GetEnd() != "78815" {
		t.Errorf("stored: revision %d, entry %v", res.GetDefinition().GetAudit().GetRevision(), last)
	}
	if got := matchIDs(t, svc, &claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: "78815", DateOfService: day(2026, 3, 2)}); !same(got, []string{"advanced-imaging"}) {
		t.Errorf("the added code: %v", got)
	}
	// Every version is kept, newest first, each as it was stored.
	revs, err := svc.ListServiceDefinitionRevisions(ctx, &claimsv1.ListServiceDefinitionRevisionsRequest{Id: "advanced-imaging"})
	if err != nil {
		t.Fatal(err)
	}
	if rs := revs.GetRevisions(); len(rs) != 2 || rs[0].GetAudit().GetRevision() != 2 || len(rs[0].GetEntries()) != 4 ||
		rs[1].GetAudit().GetRevision() != 1 || len(rs[1].GetEntries()) != 3 || rs[1].GetAudit().GetUpdatedBy() != "seed" {
		t.Errorf("revisions: %v", rs)
	}
	if _, err := svc.ListServiceDefinitionRevisions(ctx, &claimsv1.ListServiceDefinitionRevisionsRequest{Id: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("revisions of an unknown id: %v", err)
	}
	b.Name = "Imaging"
	if _, err := svc.UpdateServiceDefinition(ctx, &claimsv1.UpdateServiceDefinitionRequest{Definition: b}); connect.CodeOf(err) != connect.CodeAborted {
		t.Errorf("update at a stale revision: %v", err)
	}
	b.Audit = nil
	if _, err := svc.UpdateServiceDefinition(ctx, &claimsv1.UpdateServiceDefinitionRequest{Definition: b}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("update with no revision: %v", err)
	}
}

// The seeded Office visit arrives at what Seed lists through its edit
// history, each revision stored on its own day.
func TestSeededOfficeVisitHasAnEditHistory(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	revs, err := svc.ListServiceDefinitionRevisions(ctx, &claimsv1.ListServiceDefinitionRevisionsRequest{Id: "office-visit"})
	if err != nil {
		t.Fatal(err)
	}
	rs := revs.GetRevisions()
	if len(rs) != 5 {
		t.Fatalf("%d revisions, want 5", len(rs))
	}
	var want *claimsv1.ServiceDefinition
	for _, d := range servicedefsdemo.Seed() {
		if d.GetId() == "office-visit" {
			want = d
		}
	}
	got := proto.Clone(rs[0]).(*claimsv1.ServiceDefinition)
	got.Audit = nil
	if !proto.Equal(got, want) {
		t.Errorf("the latest revision:\n got %v\nwant %v", got, want)
	}
	if first := rs[4]; first.GetAudit().GetUpdateTime().AsTime().Year() != 2019 || len(first.GetEntries()) != 2 || rs[0].GetAudit().GetUpdateTime().AsTime().Year() != 2022 {
		t.Errorf("first saved %v with %d entries; latest saved %v", first.GetAudit().GetUpdateTime().AsTime(), len(first.GetEntries()), rs[0].GetAudit().GetUpdateTime().AsTime())
	}
}

func TestCreateAndListValidate(t *testing.T) {
	svc := seeded(t)
	ctx := context.Background()
	proc := claimsv1.CodeSystem_CODE_SYSTEM_PROCEDURE
	for name, d := range map[string]*claimsv1.ServiceDefinition{
		"bad id":             {Id: "Burn Treatment", Name: "Burn treatment"},
		"no name":            {Id: "burn-treatment"},
		"no system":          {Id: "burn-treatment", Name: "Burn treatment", Entries: []*claimsv1.CodeEntry{{Start: "16000"}}},
		"backwards range":    {Id: "burn-treatment", Name: "Burn treatment", Entries: []*claimsv1.CodeEntry{{System: proc, Start: "16030", End: "16000"}}},
		"mixed-length range": {Id: "burn-treatment", Name: "Burn treatment", Entries: []*claimsv1.CodeEntry{{System: proc, Start: "1600", End: "16030"}}},
		"ends before it starts": {Id: "burn-treatment", Name: "Burn treatment", Entries: []*claimsv1.CodeEntry{{System: proc, Start: "16000",
			Effective: &commonv1.DateRange{Start: day(2026, 1, 1), End: day(2025, 1, 1)}}}},
	} {
		if _, err := svc.CreateServiceDefinition(ctx, &claimsv1.CreateServiceDefinitionRequest{Definition: d}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := svc.CreateServiceDefinition(ctx, &claimsv1.CreateServiceDefinitionRequest{Definition: &claimsv1.ServiceDefinition{Id: "office-visit", Name: "Again"}}); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Errorf("an id in use: %v", err)
	}
	if _, err := svc.GetServiceDefinition(ctx, &claimsv1.GetServiceDefinitionRequest{Id: "nope"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown id: %v", err)
	}

	all, err := svc.ListServiceDefinitions(ctx, &claimsv1.ListServiceDefinitionsRequest{})
	if err != nil || len(all.GetDefinitions()) != 6 {
		t.Fatalf("list: %d definitions, %v", len(all.GetDefinitions()), err)
	}
	with, err := svc.ListServiceDefinitions(ctx, &claimsv1.ListServiceDefinitionsRequest{System: proc, Code: "29881"})
	if err != nil || len(with.GetDefinitions()) != 1 || with.GetDefinitions()[0].GetId() != "outpatient-surgery" {
		t.Errorf("definitions listing 29881: %v, %v", with.GetDefinitions(), err)
	}
	if _, err := svc.ListServiceDefinitions(ctx, &claimsv1.ListServiceDefinitionsRequest{Code: "29881"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a code with no system: %v", err)
	}
}
