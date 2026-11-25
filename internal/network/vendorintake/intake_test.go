package vendorintake_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	networkv1 "coreadmin/gen/coreadmin/network/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/common/store"
	"coreadmin/internal/network"
	"coreadmin/internal/network/vendorintake"
)

const (
	ppo = "cigna-ppo-individual"
	oap = "cigna-oap-individual"
	hdr = "NPI,TIN,LAST_NAME,FIRST_NAME,GROUP_NAME,SPECIALTY_1,SPECIALTY_2,SPECIALTY_3,SPECIALTY_4,ADDRESS_1,CITY,STATE,ZIP,PHONE,EFF_DATE,TERM_DATE,LANGUAGE\n"
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	repos *repo.Repos
	proc  *vendorintake.Processor
	api   network.API
}

func setup(t *testing.T) *fixture {
	t.Helper()
	r := repotest.New(t)
	ctx := store.WithActor(context.Background(), "ops")
	if err := vendorintake.Setup(ctx, r); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	return &fixture{t: t, ctx: ctx, repos: r, api: network.New(r), proc: &vendorintake.Processor{Repos: r, Now: func() time.Time {
		clock = clock.Add(time.Millisecond)
		return clock
	}}}
}

// send takes a file in and returns the update it proposes.
func (f *fixture) send(source, text string) *providerv1.ProviderUpdate {
	f.t.Helper()
	_, u, err := f.proc.Ingest(f.ctx, source, "file.csv", strings.NewReader(text))
	if err != nil {
		f.t.Fatal(err)
	}
	if u == nil {
		f.t.Fatal("the file failed")
	}
	return u
}

func (f *fixture) approve(u *providerv1.ProviderUpdate) {
	f.t.Helper()
	if _, err := f.proc.Approve(f.ctx, u.GetId()); err != nil {
		f.t.Fatal(err)
	}
}

// tins are the TINs an NPI participates under on a date, with the
// underlying network of each.
func (f *fixture) tins(npi string, on *commonv1.Date) map[string]string {
	f.t.Helper()
	res, err := f.api.GetProvider(f.ctx, &networkv1.GetProviderRequest{Npi: npi, On: on})
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]string{}
	for _, p := range res.GetParticipations() {
		out[p.GetTin()+"@"+p.GetUnderlyingNetworkId()] = p.GetBillingName()
	}
	return out
}

func day(y, m, d int32) *commonv1.Date { return &commonv1.Date{Year: y, Month: m, Day: d} }

func (f *fixture) changes(u *providerv1.ProviderUpdate) map[string]*providerv1.VendorProviderChange {
	f.t.Helper()
	all, _, err := f.repos.VendorProviderChanges.List(f.ctx, store.ListOptions[*providerv1.VendorProviderChange]{PageSize: 10000, Where: []store.Cond{store.Eq(u.GetId(), "providerUpdateId")}})
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]*providerv1.VendorProviderChange{}
	for _, c := range all {
		out[c.GetNpi()+"/"+c.GetTin()] = c
	}
	return out
}

// A file changes nothing until its update is approved; approving it makes
// the provider, and its participation under each TIN.
func TestNothingAppliesUntilApproved(t *testing.T) {
	f := setup(t)
	u := f.send(ppo, hdr+
		"1000000001,310000001,Abara,Amara,Dayton Medical Group,Cardiology,,,,22 Main St,Dayton,OH,45402,555-0100,2020-01-01,,Spanish\n"+
		"1000000001,310000002,Abara,Amara,Columbus Heart,Cardiology,,,,9 High St,Columbus,OH,43215,555-0200,01/01/2022,,\n")
	if s := u.GetSummary(); u.GetStatus() != providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_PROPOSED ||
		s.GetAdded() != 2 || s.GetRowsRead() != 2 || s.GetNewProviders() != 1 || s.GetNewTins() != 2 || s.GetParticipationsAfter() != 2 {
		t.Fatalf("update: %v", u)
	}
	if got := f.tins("1000000001", day(2026, 1, 1)); len(got) != 0 {
		t.Fatalf("applied before approval: %v", got)
	}
	// The source's next file waits for the decision.
	if _, _, err := f.proc.Ingest(f.ctx, ppo, "next.csv", strings.NewReader(hdr)); !errors.Is(err, vendorintake.ErrPending) {
		t.Fatalf("second file while one is proposed: %v", err)
	}
	f.approve(u)

	got := f.tins("1000000001", day(2026, 1, 1))
	if len(got) != 2 || got["310000001@net_cigna_ppo"] != "Dayton Medical Group" || got["310000002@net_cigna_ppo"] != "Columbus Heart" {
		t.Errorf("TINs for the NPI: %v", got)
	}
	// Before the second TIN's start date, only the first.
	if got := f.tins("1000000001", day(2021, 6, 1)); len(got) != 1 {
		t.Errorf("TINs in 2021: %v", got)
	}
	// The roll-up from the TIN's side.
	under, _, err := f.repos.Participations.List(f.ctx, store.ListOptions[*providerv1.NetworkParticipation]{Where: []store.Cond{store.Eq("310000002", "tin")}})
	if err != nil || len(under) != 1 || under[0].GetNpi() != "1000000001" {
		t.Errorf("NPIs under the TIN: %v %v", under, err)
	}
	// The row is kept as sent, the unmapped column included.
	for _, c := range f.changes(u) {
		if c.GetTin() == "310000001" && c.GetRows()[0].GetOtherColumns()["LANGUAGE"] != "Spanish" {
			t.Errorf("row as sent: %v", c.GetRows()[0])
		}
	}
	res, _ := f.api.GetProvider(f.ctx, &networkv1.GetProviderRequest{Npi: "1000000001", On: day(2026, 1, 1)})
	if p := res.GetProvider(); p.GetName().GetLast() != "Abara" || p.GetProviderUpdateId() != u.GetId() {
		t.Errorf("provider: %v", p)
	}
	job, _ := f.repos.VendorIngestionJobs.Get(f.ctx, u.GetIngestionJobId())
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED || job.GetCounts().GetRecordsCreated() != 2 {
		t.Errorf("job: %v", job)
	}
}

// Extra rows for one NPI and TIN continue its specialties and carry its
// earlier periods; together they make one participation.
func TestRowsForAPairMakeOneParticipation(t *testing.T) {
	f := setup(t)
	u := f.send(ppo, hdr+
		"1000000001,310000001,Abara,Amara,Dayton Medical Group,Cardiology,Internal Medicine,Geriatrics,Sports Medicine,22 Main St,Dayton,OH,45402,555-0100,2023-01-01,,\n"+
		"1000000001,310000001,Abara,Amara,Dayton Medical Group,Sleep Medicine,,,,22 Main St,Dayton,OH,45402,555-0100,2023-01-01,,\n"+
		"1000000001,310000001,Abara,Amara,Dayton Medical Group,Cardiology,,,,5 Old Rd,Dayton,OH,45402,555-0100,2015-01-01,2019-12-31,\n")
	if s := u.GetSummary(); s.GetAdded() != 1 || s.GetRowsRead() != 3 {
		t.Fatalf("summary: %v", s)
	}
	f.approve(u)
	p, err := f.repos.Participations.Get(f.ctx, vendorintake.ParticipationID(ppo, "1000000001", "310000001"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.GetSpecialties()) != 5 || len(p.GetPeriods()) != 2 || p.GetPeriods()[0].GetEnd().GetYear() != 2019 || p.GetPeriods()[1].GetEnd() != nil || len(p.GetLocations()) != 2 || p.GetLocations()[0].GetAddress().GetLines()[0] != "22 Main St" {
		t.Errorf("participation: %v", p)
	}
	// In network in both periods, out between them.
	for on, want := range map[*commonv1.Date]int{day(2017, 1, 1): 1, day(2021, 1, 1): 0, day(2026, 1, 1): 1} {
		if got := f.tins("1000000001", on); len(got) != want {
			t.Errorf("on %v: %v", on, got)
		}
	}
}

// The next full file is compared with the last approved one: only what
// differs is proposed, and a pair the file no longer has is removed for
// every date.
func TestNextFileProposesOnlyDifferences(t *testing.T) {
	f := setup(t)
	row := func(npi, tin, group, spec, addr, term string) string {
		return npi + "," + tin + ",Doc," + npi[9:] + "," + group + "," + spec + ",,,," + addr + ",Dayton,OH,45402,555-0100,2020-01-01," + term + ",\n"
	}
	f.approve(f.send(ppo, hdr+
		row("1000000001", "310000001", "Group A", "Cardiology", "1 Main St", "")+
		row("1000000002", "310000001", "Group A", "Pediatrics", "1 Main St", "")+
		row("1000000003", "310000001", "Group A", "Neurology", "1 Main St", "")+
		row("1000000004", "310000001", "Group A", "Psychiatry", "1 Main St", "")))

	u := f.send(ppo, hdr+
		// Row order doesn't matter, and 0001 is unchanged.
		row("1000000002", "310000001", "Group A", "Pediatrics", "1 Main St", "2026-10-31")+ // termed
		row("1000000001", "310000001", "Group A", "Cardiology", "1 Main St", "")+
		row("1000000003", "310000001", "Group A", "Neurology", "8 New Ave", "")+ // moved
		row("1000000005", "310000009", "Group B", "Dermatology", "3 Elm St", "")+ // joined
		",310000009,No,Npi,Group B,Dermatology,,,,3 Elm St,Dayton,OH,45402,555-0100,2020-01-01,,\n") // unusable
	s := u.GetSummary()
	if s.GetAdded() != 1 || s.GetChanged() != 2 || s.GetRemoved() != 1 || s.GetUnchanged() != 1 || s.GetRowsErrored() != 1 ||
		s.GetParticipationsBefore() != 4 || s.GetParticipationsAfter() != 4 || s.GetNewProviders() != 1 || s.GetNewTins() != 1 {
		t.Fatalf("summary: %v", s)
	}
	ch := f.changes(u)
	if len(ch) != 4 {
		t.Fatalf("%d changes stored, want only the 4 differences", len(ch))
	}
	aspect := func(key string) providerv1.VendorChangeAspect { return ch[key].GetAspects()[0] }
	if aspect("1000000002/310000001") != providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_TERMED ||
		aspect("1000000003/310000001") != providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_LOCATION ||
		ch["1000000004/310000001"].GetKind() != providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_DROPPED ||
		len(ch["1000000004/310000001"].GetPreviousRows()) != 1 || len(ch["1000000003/310000001"].GetPreviousRows()) != 1 {
		t.Errorf("changes: %v", ch)
	}
	job, _ := f.repos.VendorIngestionJobs.Get(f.ctx, u.GetIngestionJobId())
	if len(job.GetErrors()) != 1 || job.GetErrors()[0].GetRecordIndex() != 6 || job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_AWAITING_APPROVAL {
		t.Errorf("job: %v", job)
	}

	// Still as before until approved.
	if got := f.tins("1000000004", day(2025, 1, 1)); len(got) != 1 {
		t.Fatalf("0004 before approval: %v", got)
	}
	f.approve(u)
	// Termed: in network before the term date, out after.
	if in, out := f.tins("1000000002", day(2026, 10, 31)), f.tins("1000000002", day(2026, 11, 1)); len(in) != 1 || len(out) != 0 {
		t.Errorf("termed provider: %v then %v", in, out)
	}
	// Dropped: out for every date, though its history is kept.
	if got := f.tins("1000000004", day(2025, 1, 1)); len(got) != 0 {
		t.Errorf("dropped provider still in network in 2025: %v", got)
	}
	vs, err := f.repos.Participations.Versions(f.ctx, vendorintake.ParticipationID(ppo, "1000000004", "310000001"))
	if err != nil || len(vs) != 1 || vs[0].To.IsZero() {
		t.Errorf("dropped participation's history: %v %v", vs, err)
	}
	if got := f.tins("1000000005", day(2026, 1, 1)); len(got) != 1 {
		t.Errorf("joined provider: %v", got)
	}

	// The same file again proposes nothing.
	again := f.send(ppo, hdr+
		row("1000000002", "310000001", "Group A", "Pediatrics", "1 Main St", "2026-10-31")+
		row("1000000001", "310000001", "Group A", "Cardiology", "1 Main St", "")+
		row("1000000003", "310000001", "Group A", "Neurology", "8 New Ave", "")+
		row("1000000005", "310000009", "Group B", "Dermatology", "3 Elm St", ""))
	if s := again.GetSummary(); s.GetAdded()+s.GetChanged()+s.GetRemoved() != 0 || s.GetUnchanged() != 4 || len(f.changes(again)) != 0 {
		t.Errorf("same file again: %v", s)
	}
}

// A rejected update applies nothing, and the next file is compared with
// the last approved one.
func TestRejectedUpdateAppliesNothing(t *testing.T) {
	f := setup(t)
	one := "1000000001,310000001,Abara,Amara,Group A,Cardiology,,,,1 Main St,Dayton,OH,45402,555-0100,2020-01-01,,\n"
	two := "1000000002,310000001,Bennett,Ben,Group A,Pediatrics,,,,1 Main St,Dayton,OH,45402,555-0100,2020-01-01,,\n"
	f.approve(f.send(ppo, hdr+one+two))

	cut := f.send(ppo, hdr+one)
	if cut.GetSummary().GetRemoved() != 1 {
		t.Fatalf("cut file: %v", cut.GetSummary())
	}
	u, err := f.proc.Reject(f.ctx, cut.GetId())
	if err != nil || u.GetStatus() != providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_REJECTED || u.GetDecidedBy() != "ops" {
		t.Fatalf("reject: %v %v", u, err)
	}
	if got := f.tins("1000000002", day(2026, 1, 1)); len(got) != 1 {
		t.Errorf("rejected update removed a provider: %v", got)
	}
	if _, err := f.proc.Approve(f.ctx, cut.GetId()); err == nil {
		t.Error("approved a rejected update")
	}
	if s := f.send(ppo, hdr+one+two).GetSummary(); s.GetUnchanged() != 2 || s.GetAdded() != 0 {
		t.Errorf("file after a rejection: %v", s)
	}
}

// A file speaks only for its own source: the OAP file adds OAP
// participations beside the PPO ones and removes none of them.
func TestAFileSpeaksOnlyForItsSource(t *testing.T) {
	f := setup(t)
	one := "1000000001,310000001,Abara,Amara,Group A,Cardiology,,,,1 Main St,Dayton,OH,45402,555-0100,2020-01-01,,\n"
	two := "1000000002,310000001,Bennett,Ben,Group A,Pediatrics,,,,1 Main St,Dayton,OH,45402,555-0100,2020-01-01,,\n"
	f.approve(f.send(ppo, hdr+one+two))
	u := f.send(oap, hdr+one)
	if s := u.GetSummary(); s.GetAdded() != 1 || s.GetRemoved() != 0 || s.GetNewProviders() != 0 || s.GetParticipationsBefore() != 0 {
		t.Fatalf("OAP file: %v", s)
	}
	f.approve(u)
	if got := f.tins("1000000001", day(2026, 1, 1)); len(got) != 2 || got["310000001@net_cigna_oap"] == "" {
		t.Errorf("0001: %v", got)
	}
	if got := f.tins("1000000002", day(2026, 1, 1)); len(got) != 1 {
		t.Errorf("0002: %v", got)
	}
	providers, _, _ := f.repos.Providers.List(f.ctx, store.ListOptions[*providerv1.Provider]{})
	if len(providers) != 2 {
		t.Errorf("%d providers, want one per NPI", len(providers))
	}
}

// A file that isn't the source's layout, or has no rows, fails as a whole.
func TestUnreadableFileFails(t *testing.T) {
	f := setup(t)
	for name, text := range map[string]string{"no TIN column": "NPI,EFF_DATE\n1,2020-01-01\n", "no rows": hdr, "empty": ""} {
		job, u, err := f.proc.Ingest(f.ctx, ppo, "x.csv", strings.NewReader(text))
		if err != nil || u != nil || job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED || len(job.GetErrors()) != 1 {
			t.Errorf("%s: job %v, update %v, err %v", name, job, u, err)
		}
	}
}

// The demo's files, sent in order: a larger run through every path, more
// than one batch of changes.
func TestSamples(t *testing.T) {
	f := setup(t)
	s := map[string]vendorintake.Sample{}
	for _, x := range vendorintake.Samples() {
		s[x.ID] = x
	}
	first := f.send(ppo, s["ppo-first"].Text)
	if n := first.GetSummary().GetAdded(); n <= 500 || first.GetSummary().GetNewProviders() != 500 {
		t.Fatalf("first file: %v", first.GetSummary())
	}
	f.approve(first)
	parts, _, err := f.repos.Participations.List(f.ctx, store.ListOptions[*providerv1.NetworkParticipation]{PageSize: 10000})
	if err != nil || int64(len(parts)) != first.GetSummary().GetAdded() {
		t.Fatalf("%d participations after the first file, want %d (%v)", len(parts), first.GetSummary().GetAdded(), err)
	}

	next := f.send(ppo, s["ppo-next"].Text)
	ns := next.GetSummary()
	if ns.GetAdded() != 14 || ns.GetRemoved() == 0 || ns.GetChanged() == 0 || ns.GetRowsErrored() != 2 || len(ns.GetAspects()) < 4 || len(ns.GetStates()) < 3 ||
		ns.GetUnchanged()+ns.GetChanged()+ns.GetRemoved() != ns.GetParticipationsBefore() {
		t.Fatalf("next month: %v", ns)
	}
	if _, err := f.proc.Reject(f.ctx, next.GetId()); err != nil {
		t.Fatal(err)
	}
	// The file cut short would remove most of the network: the review
	// shows it, and nothing happens unless it is approved.
	cut := f.send(ppo, s["ppo-truncated"].Text)
	if cs := cut.GetSummary(); cs.GetRemoved() < cs.GetParticipationsBefore()/2 {
		t.Errorf("cut file: %v", cs)
	}
}
