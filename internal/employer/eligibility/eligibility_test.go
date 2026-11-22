package eligibility_test

import (
	"context"
	"coreadmin/internal/common/repo/repotest"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/eligibility"
	"coreadmin/internal/employer/enrollmentintake"
	"coreadmin/internal/seed"
	seeddata "coreadmin/seed"
)

type world struct {
	t     *testing.T
	repos *repo.Repos
	svc   *eligibility.Service
	proc  *enrollmentintake.Processor
	clock time.Time
}

// setup loads the customer, plan and group seed data, with no members:
// everyone comes from 834s, starting with the open-enrollment full file.
func setup(t *testing.T) *world {
	t.Helper()
	ctx := context.Background()
	repos := repotest.New(t)
	if _, err := seed.Load(ctx, repos, seeddata.FS); err != nil {
		t.Fatal(err)
	}
	seeded, _, _ := repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1000})
	for _, m := range seeded {
		if err := repos.Members.Delete(ctx, m.GetId()); err != nil {
			t.Fatal(err)
		}
	}
	w := &world{t: t, repos: repos, clock: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
	now := func() time.Time { w.clock = w.clock.Add(time.Minute); return w.clock }
	w.svc = &eligibility.Service{Repos: repos, Now: now}
	w.proc = &enrollmentintake.Processor{Repos: repos, Eligibility: w.svc, Files: map[string][]byte{}, ReceiverID: enrollmentintake.OurID, Now: now}
	for _, b := range enrollmentintake.Baselines() {
		w.sendText(b.Filename, b.Text)
	}
	return w
}

func (w *world) send(id string) {
	w.t.Helper()
	w.sendText(id+".834", sampleText(w.t, id))
}

func (w *world) sendText(name, text string) {
	w.t.Helper()
	job, err := w.proc.Ingest(context.Background(), name, []byte(text))
	if err != nil {
		w.t.Fatal(err)
	}
	if job.GetCounts().GetRecordsErrored() > 0 {
		w.t.Fatalf("%s: %v %v", name, job.GetErrors(), job.GetTransactionSets())
	}
}

// member finds a current member by first name.
func (w *world) member(first string) *eligibilityv1.Member {
	w.t.Helper()
	ms, _, _ := w.repos.Members.List(context.Background(), store.ListOptions[*eligibilityv1.Member]{PageSize: 1000,
		Filter: func(m *eligibilityv1.Member) bool { return m.GetName().GetFirst() == first }})
	if len(ms) != 1 {
		w.t.Fatalf("%d members named %s", len(ms), first)
	}
	return ms[0]
}

// spans renders a member's coverage: "2026-01-01..2026-01-31 FAMILY".
func spans(m *eligibilityv1.Member) string {
	var out []string
	for _, s := range m.GetCoverage() {
		e := s.GetEffective()
		end := "open"
		if e.GetEnd() != nil {
			end = iso(e.GetEnd().GetYear(), e.GetEnd().GetMonth(), e.GetEnd().GetDay())
		}
		out = append(out, iso(e.GetStart().GetYear(), e.GetStart().GetMonth(), e.GetStart().GetDay())+".."+end+" "+
			strings.TrimPrefix(s.GetTier().String(), "COVERAGE_TIER_"))
	}
	return strings.Join(out, ", ")
}

func iso(y, m, d int32) string {
	return time.Date(int(y), time.Month(m), int(d), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
}

func (w *world) versions(m *eligibilityv1.Member) []*eligibilityv1.MemberVersion {
	w.t.Helper()
	vs, err := w.svc.History(context.Background(), m.GetId())
	if err != nil {
		w.t.Fatal(err)
	}
	return vs
}

func (w *world) lastOutcome() eligibility.Outcome {
	w.t.Helper()
	st, err := w.svc.State(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return st.Outcomes[len(st.Outcomes)-1]
}

func TestOpenEnrollmentBuildsMembers(t *testing.T) {
	w := setup(t)
	jordan, alex, riley, chidi := w.member("Jordan"), w.member("Alex"), w.member("Riley"), w.member("Chidi")
	checks := map[string][2]any{
		"jordan self":       {jordan.GetRelationship(), eligibilityv1.Relationship_RELATIONSHIP_SELF},
		"jordan subscriber": {jordan.GetSubscriberId(), jordan.GetId()},
		"jordan card":       {jordan.GetCardId(), "ACM1010101"},
		"alex card":         {alex.GetCardId(), "ACM1010102"},
		"riley card":        {riley.GetCardId(), "ACM1010103"},
		"riley child":       {riley.GetRelationship(), eligibilityv1.Relationship_RELATIONSHIP_CHILD},
		"riley family":      {riley.GetSubscriberId(), jordan.GetId()},
		"customer":          {riley.GetCustomerId(), "cust_acme"},
		"jordan coverage":   {spans(jordan), "2026-01-01..open FAMILY"},
		"chidi coverage":    {spans(chidi), "2026-01-01..open EMPLOYEE"},
		"plan":              {chidi.GetCoverage()[0].GetPlanId(), "plan_acme_ppo"},
		"group":             {chidi.GetCoverage()[0].GetGroupId(), "grp_acme_main"},
		"one version":       {len(w.versions(chidi)), 1},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %v, want %v", name, c[0], c[1])
		}
	}
	if v := w.versions(chidi)[0]; v.GetInputSequence() == 0 || v.GetRulesVersion() != eligibility.RulesVersion {
		t.Errorf("version %v", v)
	}
}

func TestNewbornJoinsFamily(t *testing.T) {
	w := setup(t)
	w.send("newborn")
	noah, jordan := w.member("Noah"), w.member("Jordan")
	if noah.GetSubscriberId() != jordan.GetId() || noah.GetCardId() != "ACM1010104" || spans(noah) != "2026-03-02..open FAMILY" {
		t.Errorf("noah: subscriber %s card %s coverage %s", noah.GetSubscriberId(), noah.GetCardId(), spans(noah))
	}
	// Nobody else changed.
	if len(w.versions(jordan)) != 1 {
		t.Errorf("jordan has %d versions", len(w.versions(jordan)))
	}
}

// A retro term then a reinstatement: three versions, and the last is
// back where the first was. Asking as of a time in between shows the
// term as we believed it then.
func TestFlipFlopKeepsEveryVersion(t *testing.T) {
	w := setup(t)
	w.send("retro-term")
	if got := spans(w.member("Chidi")); got != "2026-01-01..2026-01-31 EMPLOYEE" {
		t.Fatalf("after retro term: %s", got)
	}
	termed := w.clock
	w.send("reinstatement")
	chidi := w.member("Chidi")
	if got := spans(chidi); got != "2026-01-01..open EMPLOYEE" {
		t.Errorf("after reinstatement: %s", got)
	}
	vs := w.versions(chidi)
	if len(vs) != 3 {
		t.Fatalf("%d versions", len(vs))
	}
	if spans(vs[1].GetMember()) != "2026-01-01..2026-01-31 EMPLOYEE" || spans(vs[2].GetMember()) != spans(vs[0].GetMember()) {
		t.Errorf("versions: %s / %s / %s", spans(vs[0].GetMember()), spans(vs[1].GetMember()), spans(vs[2].GetMember()))
	}
	then, err := w.svc.AsOf(context.Background(), chidi.GetId(), termed)
	if err != nil || then.GetVersion() != 2 {
		t.Errorf("as of the term: %v %v", then, err)
	}
	before, _ := w.svc.AsOf(context.Background(), chidi.GetId(), time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if before != nil {
		t.Errorf("as of New Year: %v, want nothing recorded yet", before)
	}
}

func TestTermAndChanges(t *testing.T) {
	w := setup(t)
	w.send("termination")
	if got := spans(w.member("Priya")); got != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE" {
		t.Errorf("priya: %s", got)
	}
	// The term named only Priya; Arjun's coverage is untouched.
	if got := spans(w.member("Arjun")); got != "2026-01-01..open EMPLOYEE_SPOUSE" {
		t.Errorf("arjun: %s", got)
	}
	w.send("address-change")
	jordan := w.member("Jordan")
	if jordan.GetAddress().GetLines()[0] != "1150 Riverside Dr" || spans(jordan) != "2026-01-01..open FAMILY" || len(w.versions(jordan)) != 2 {
		t.Errorf("jordan: %v, %s, %d versions", jordan.GetAddress(), spans(jordan), len(w.versions(jordan)))
	}
	w.send("cobra")
	priya := w.member("Priya")
	if got := spans(priya); got != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE, 2026-04-01..open EMPLOYEE" || !priya.GetCoverage()[1].GetCobra() {
		t.Errorf("priya on COBRA: %s", got)
	}
}

// A full file proposes terminating the enrolled members it leaves out,
// and nothing changes until the proposal is approved.
func TestFullFileWaitsForApproval(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	w.send("full-file")
	if o := w.lastOutcome(); o.Status != eligibility.Proposed {
		t.Errorf("outcome %v", o)
	}
	st, _ := w.svc.State(ctx)
	if len(st.Proposals) != 1 || len(st.Proposals[0].Terms) != 2 {
		t.Fatalf("proposals %+v", st.Proposals)
	}
	if got := spans(w.member("Priya")); got != "2026-01-01..open EMPLOYEE_SPOUSE" {
		t.Errorf("priya before approval: %s", got)
	}
	pr := st.Proposals[0]
	if err := w.svc.ApproveFullFile(store.WithActor(ctx, "ops"), pr.JobID, pr.SetControl, "confirmed with Acme HR"); err != nil {
		t.Fatal(err)
	}
	for _, first := range []string{"Priya", "Arjun"} {
		if got := spans(w.member(first)); got != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE" {
			t.Errorf("%s after approval: %s", first, got)
		}
	}
	if o := w.lastOutcome(); o.Status != eligibility.Applied || len(o.MemberIDs) != 2 {
		t.Errorf("approval outcome %v", o)
	}
	if err := w.svc.ApproveFullFile(ctx, pr.JobID, pr.SetControl, ""); err == nil {
		t.Error("approved twice")
	}
}

// What can't be applied is recorded as an exception and changes nothing.
func TestExceptionsChangeNothing(t *testing.T) {
	w := setup(t)
	chidi := w.member("Chidi")
	w.send("unknown-code")
	if o := w.lastOutcome(); o.Status != eligibility.Exception || !strings.Contains(o.Message, "099") {
		t.Errorf("unknown code: %v", o)
	}
	if len(w.versions(chidi)) != 1 {
		t.Errorf("chidi changed")
	}
	// A term for someone never enrolled creates no one.
	x := sampleText(t, "termination")
	x = strings.NewReplacer("E10201", "E19999", "000000203", "000000290", "PRIYA", "PAT", "999102001", "999199999").Replace(x)
	w.sendText("stranger.834", x)
	if o := w.lastOutcome(); o.Status != eligibility.Exception {
		t.Errorf("stranger: %v", o)
	}
	ms, _, _ := w.repos.Members.List(context.Background(), store.ListOptions[*eligibilityv1.Member]{PageSize: 1000})
	if len(ms) != 9 {
		t.Errorf("%d members", len(ms))
	}
	// An information copy is skipped.
	info := strings.NewReplacer("BGN*00*", "BGN*22*", "000000202", "000000291").Replace(sampleText(t, "newborn"))
	w.sendText("info.834", info)
	if o := w.lastOutcome(); o.Status != eligibility.Skipped {
		t.Errorf("information copy: %v", o)
	}
}

// Replaying the inputs reproduces the members on file; a member that drifts
// from what the inputs say is put back as a new version.
func TestRegenerate(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for _, id := range []string{"newborn", "retro-term", "reinstatement", "address-change", "termination"} {
		w.send(id)
	}
	if changes, err := w.svc.Regenerate(ctx, true); err != nil || len(changes) != 0 {
		t.Fatalf("dry run: %v %v", changes, err)
	}
	// Someone edits a member behind eligibility's back.
	riley := w.member("Riley")
	riley.Coverage = nil
	if _, err := w.repos.Members.Update(ctx, riley); err != nil {
		t.Fatal(err)
	}
	changes, err := w.svc.Regenerate(ctx, true)
	if err != nil || len(changes) != 1 || changes[0].MemberID != riley.GetId() {
		t.Fatalf("dry run after drift: %v %v", changes, err)
	}
	if spans(w.member("Riley")) != "" {
		t.Error("the dry run wrote")
	}
	if _, err := w.svc.Regenerate(ctx, false); err != nil {
		t.Fatal(err)
	}
	vs := w.versions(w.member("Riley"))
	if spans(w.member("Riley")) != "2026-01-01..open FAMILY" || vs[len(vs)-1].GetReason() != "regenerated" || vs[len(vs)-1].GetInputId() != "" {
		t.Errorf("after regenerating: %s, last version %v", spans(w.member("Riley")), vs[len(vs)-1])
	}

	// A second, independent replay of the same inputs gives the same
	// members: ids, cards and coverage don't depend on when they're built.
	ins, _ := w.svc.Inputs(ctx)
	again, err := eligibility.Derive(ctx, w.repos, ins)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range again.MemberIDs() {
		cur, err := w.repos.Members.Get(ctx, id)
		if err != nil || spans(cur) != spans(again.Members[id]) || cur.GetCardId() != again.Members[id].GetCardId() {
			t.Errorf("%s: replay differs: %v", id, err)
		}
	}
}

func sampleText(t *testing.T, id string) string {
	t.Helper()
	for _, s := range enrollmentintake.Samples() {
		if s.ID == id {
			return s.Text
		}
	}
	t.Fatalf("no sample %s", id)
	return ""
}

// A full file that only confirms what's on file creates no versions, and
// it doesn't reconcile members another sender speaks for.
func TestFullFileConfirmationIsQuiet(t *testing.T) {
	w := setup(t)
	w.send("termination")
	w.send("cobra")
	w.send("full-file")
	if n := len(w.versions(w.member("Chidi"))); n != 1 {
		t.Errorf("chidi has %d versions after a full file confirming his coverage", n)
	}
	// Priya is on COBRA from the vendor: the employer's full file doesn't
	// speak for her, so it proposes ending only Arjun's coverage.
	st, _ := w.svc.State(context.Background())
	var names []string
	for _, term := range st.Proposals[0].Terms {
		names = append(names, term.Name)
	}
	if strings.Join(names, ",") != "Arjun Patel" {
		t.Errorf("proposed terms: %v", names)
	}
}

// One COBRA vendor serves both clients: each election lands on the right
// customer's member.
func TestCOBRAVendorServesSeveralClients(t *testing.T) {
	w := setup(t)
	w.send("termination")
	w.send("brightline-term")
	w.send("cobra")
	priya, kim := w.member("Priya"), w.member("Kim")
	if priya.GetCustomerId() != "cust_acme" || kim.GetCustomerId() != "cust_brightline" || kim.GetCardId() != "BRI2020101" {
		t.Errorf("customers %s / %s, kim's card %s", priya.GetCustomerId(), kim.GetCustomerId(), kim.GetCardId())
	}
	for _, m := range []*eligibilityv1.Member{priya, kim} {
		cov := m.GetCoverage()
		if len(cov) != 2 || !cov[1].GetCobra() || iso(cov[1].GetEffective().GetStart().GetYear(), cov[1].GetEffective().GetStart().GetMonth(), cov[1].GetEffective().GetStart().GetDay()) != "2026-04-01" {
			t.Errorf("%s: %s", m.GetName().GetFirst(), spans(m))
		}
	}
}

// The vendor speaks for continuees: an employer termination arriving after
// the COBRA election doesn't end the COBRA coverage.
func TestEmployerTermDoesNotEndCOBRA(t *testing.T) {
	w := setup(t)
	w.send("cobra")
	w.send("termination")
	if got := spans(w.member("Priya")); got != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE, 2026-04-01..open EMPLOYEE" {
		t.Errorf("priya: %s", got)
	}
}

func TestSenderRoles(t *testing.T) {
	w := setup(t)
	// Acme's COBRA is the vendor's, so its HR system can't enroll COBRA.
	w.send("cobra-from-employer")
	if o := w.lastOutcome(); o.Status != eligibility.Exception || !strings.Contains(o.Message, "administered by COBRAVND") {
		t.Errorf("employer COBRA: %v", o)
	}
	// The vendor only sends COBRA.
	active := strings.NewReplacer("INS*Y*18*021**C*", "INS*Y*18*021**A*", "000000301", "000000390").Replace(sampleText(t, "cobra"))
	w.sendText("active.834", active)
	if o := w.lastOutcome(); o.Status != eligibility.Exception || !strings.Contains(o.Message, "administers COBRA only") {
		t.Errorf("vendor sending active coverage: %v", o)
	}
	// A COBRA administrator no client has set up is refused.
	other := strings.NewReplacer("COBRAVND", "OTHERCOB", "000000301", "000000391").Replace(sampleText(t, "cobra"))
	w.sendText("other.834", other)
	if o := w.lastOutcome(); o.Status != eligibility.Exception || !strings.Contains(o.Message, "isn't set up") {
		t.Errorf("unknown vendor: %v", o)
	}
	if spans(w.member("Priya")) != "2026-01-01..open EMPLOYEE_SPOUSE" {
		t.Errorf("priya changed: %s", spans(w.member("Priya")))
	}
}

// The vendor's full file reconciles only its continuees: Acme's employees,
// absent from it, aren't proposed for termination.
func TestVendorFullFileLeavesEmployeesAlone(t *testing.T) {
	w := setup(t)
	w.send("termination")
	w.send("cobra")
	w.send("cobra-full-file")
	st, _ := w.svc.State(context.Background())
	if len(st.Proposals) != 0 {
		t.Errorf("proposals: %+v", st.Proposals[0].Terms)
	}
	if n := len(w.versions(w.member("Priya"))); n != 3 {
		t.Errorf("priya has %d versions; the vendor's confirmation should add none", n)
	}
}

// Every sample, sent in many different orders: whatever the order, the
// stored inputs, versions and members always agree with each other and
// with a replay, at every step.
func TestVerifyHoldsInAnyOrder(t *testing.T) {
	var ids []string
	for _, s := range enrollmentintake.Samples() {
		if s.ID != "bad-set" && s.ID != "bad-envelope" {
			ids = append(ids, s.ID)
		}
	}
	for seed := int64(1); seed <= 12; seed++ {
		w := setup(t)
		order := slices.Clone(ids)
		rand.New(rand.NewSource(seed)).Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, id := range order {
			w.sendText(id+".834", sampleText(t, id))
		}
		// Approve whatever full files proposed, as an examiner would.
		st, _ := w.svc.State(context.Background())
		for _, p := range st.Proposals {
			if err := w.svc.ApproveFullFile(context.Background(), p.JobID, p.SetControl, "ok"); err != nil {
				t.Fatal(err)
			}
		}
		checks, err := w.svc.Verify(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range checks {
			if !c.OK {
				t.Errorf("order %v: %s: %s", order, c.Name, c.Detail)
			}
		}
	}
}

// Verify notices tampering.
func TestVerifyCatchesDrift(t *testing.T) {
	w := setup(t)
	w.send("retro-term")
	chidi := w.member("Chidi")
	chidi.Coverage = nil
	if _, err := w.repos.Members.Update(context.Background(), chidi); err != nil {
		t.Fatal(err)
	}
	checks, _ := w.svc.Verify(context.Background())
	failed := map[string]bool{}
	for _, c := range checks {
		if !c.OK {
			failed[c.Name] = true
		}
	}
	if !failed["The latest version is what's on file"] || !failed["Replay reproduces today"] {
		t.Errorf("checks: %+v", checks)
	}
}

// Replaying the same inputs under the rules from before sender authority
// shows what that change did: only Priya differs, because the old rules
// let the employer's full file end her COBRA. Nothing is written.
func TestCompareUnderOtherRules(t *testing.T) {
	w := setup(t)
	ctx := context.Background()
	for _, id := range []string{"termination", "cobra", "full-file"} {
		w.send(id)
	}
	st, _ := w.svc.State(ctx)
	p := st.Proposals[0]
	if err := w.svc.ApproveFullFile(ctx, p.JobID, p.SetControl, "ok"); err != nil {
		t.Fatal(err)
	}
	changes, err := w.svc.Compare(ctx, eligibility.BeforeSenderAuthority)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].After.GetName().GetFirst() != "Priya" {
		t.Fatalf("changes: %v", changes)
	}
	if spans(changes[0].Before) != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE, 2026-04-01..open EMPLOYEE" ||
		spans(changes[0].After) != "2026-01-01..2026-03-31 EMPLOYEE_SPOUSE" {
		t.Errorf("priya now %s, under the old rules %s", spans(changes[0].Before), spans(changes[0].After))
	}
	if same, _ := w.svc.Compare(ctx, eligibility.Current); len(same) != 0 {
		t.Errorf("current rules differ from what's on file: %v", same)
	}
}
