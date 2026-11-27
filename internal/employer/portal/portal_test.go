package portal_test

import (
	"context"
	"strings"
	"testing"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/common/repo/repotest"
	"coreadmin/internal/employer/eligibilitydemo"
	"coreadmin/internal/employer/enrollmentintake"
	"coreadmin/internal/employer/portal"
)

func sandbox(t *testing.T) (*eligibilitydemo.Sandbox, *portal.Portal) {
	t.Helper()
	sb, err := eligibilitydemo.NewSandboxIn(repotest.New(t))
	if err != nil {
		t.Fatal(err)
	}
	return sb, &portal.Portal{Repos: sb.Repos, Elig: sb.Elig, Intake: sb.Intake}
}

func view(t *testing.T, sb *eligibilitydemo.Sandbox, p *portal.Portal, customer string) *portal.View {
	t.Helper()
	v, err := p.View(context.Background(), customer, sb.Now(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func names(v *portal.View) string {
	var out []string
	for _, f := range v.Families {
		for _, m := range f.Members {
			out = append(out, m.Name)
		}
	}
	return strings.Join(out, ", ")
}

// After open enrollment, each customer sees its own people, plan and file,
// and nothing of the other's.
func TestEachCustomerSeesOnlyItsOwn(t *testing.T) {
	sb, p := sandbox(t)
	acme, bright := view(t, sb, p, "cust_acme"), view(t, sb, p, "cust_brightline")

	if got := names(acme); got != "Jordan Rivera, Alex Rivera, Riley Rivera, Priya Patel, Arjun Patel, Chidi Okafor" {
		t.Errorf("Acme roster: %s", got)
	}
	if got := names(bright); got != "Diego Morales, Ana Morales, Kim Lee" {
		t.Errorf("Brightline roster: %s", got)
	}
	if acme.Covered != 6 || acme.Subscribers != 3 || acme.Dependents != 3 || acme.COBRA != 0 {
		t.Errorf("Acme counts: %+v", acme)
	}
	if got := acme.Tiers; got[0].Subscribers != 1 || got[1].Subscribers != 1 || got[2].Subscribers != 0 || got[3].Subscribers != 1 {
		t.Errorf("Acme tiers: %+v", got)
	}
	if len(acme.Plans) != 1 || acme.Plans[0].Name != "Acme PPO 1500" || acme.Plans[0].Covered != 6 ||
		acme.Plans[0].Tiers[0].DeductibleIndividual != 150000 {
		t.Errorf("Acme plans: %+v", acme.Plans)
	}
	if len(acme.Files) != 1 || acme.Files[0].Filename != "ACMEHR_000000200.834" || !acme.Files[0].Full ||
		acme.Files[0].Status != "accepted" || acme.Files[0].Members != 6 || acme.Files[0].From != "employer" {
		t.Errorf("Acme files: %+v", acme.Files)
	}
	if len(bright.Files) != 1 || bright.Files[0].Filename != "BRIGHTHR_000000400.834" || bright.Files[0].Members != 3 {
		t.Errorf("Brightline files: %+v", bright.Files)
	}
}

// A termination dated ahead shows as ending; one already past as ended.
// Brightline's portal doesn't change when Acme's files arrive.
func TestRosterFollowsThe834s(t *testing.T) {
	sb, p := sandbox(t)
	ctx := context.Background()
	before := view(t, sb, p, "cust_brightline")
	for _, id := range []string{"termination", "retro-term", "new-hire"} {
		if err := sb.Send(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	acme := view(t, sb, p, "cust_acme")
	status := map[string]string{}
	for _, f := range acme.Families {
		for _, m := range f.Members {
			status[m.Name] = m.Status + " " + m.Ends + m.Starts
		}
	}
	for name, want := range map[string]string{
		"Priya Patel":   "ending 2026-03-31",
		"Chidi Okafor":  "ended 2026-01-31",
		"Sam Nguyen":    "upcoming 2026-04-01",
		"Jordan Rivera": "active ",
	} {
		if status[name] != want {
			t.Errorf("%s: %q, want %q", name, status[name], want)
		}
	}
	if len(acme.Files) != 4 || acme.Files[0].Filename != "ACMEHR_000000201.834" || acme.Files[0].Actions[0] != (portal.ActionCount{Action: "added", Count: 3}) {
		t.Errorf("Acme files: %+v", acme.Files)
	}
	after := view(t, sb, p, "cust_brightline")
	if names(after) != names(before) || len(after.Files) != len(before.Files) || after.Covered != before.Covered {
		t.Errorf("Brightline changed: %+v", after)
	}
}

// The roster can be asked about another date, from what we know now: a
// retroactive termination changes who was covered in February.
func TestRosterOnAnotherDate(t *testing.T) {
	sb, p := sandbox(t)
	ctx := context.Background()
	feb := &commonv1.Date{Year: 2026, Month: 2, Day: 10}
	before, err := p.View(ctx, "cust_acme", sb.Now(), feb)
	if err != nil {
		t.Fatal(err)
	}
	if err := sb.Send(ctx, "retro-term"); err != nil {
		t.Fatal(err)
	}
	after, err := p.View(ctx, "cust_acme", sb.Now(), feb)
	if err != nil {
		t.Fatal(err)
	}
	if before.Covered != 6 || after.Covered != 5 || after.Today != "2026-02-10" || after.Now != "2026-03-16" {
		t.Errorf("covered on Feb 10: %d before, %d after (%s, now %s)", before.Covered, after.Covered, after.Today, after.Now)
	}
	// Billing's headcount and the covered lives follow: Chidi counts for
	// January only.
	if m := after.Months; m[0].Members != 6 || m[1].Members != 5 || m[0].Employees != 3 || m[1].Employees != 2 || m[2].Future || !m[3].Future {
		t.Errorf("months: %+v", m)
	}
	// Jan 1 to Mar 16 is 75 days: five people throughout, Chidi for 31.
	if l := after.Lives; len(l) != 1 || l[0].Days != 75 || l[0].LifeDays != 5*75+31 {
		t.Errorf("covered lives: %+v", l)
	}
}

// A file's loops say what each did, and a member's history names the file
// behind each version. Another customer's member is not found.
func TestLoopsAndHistory(t *testing.T) {
	sb, p := sandbox(t)
	ctx := context.Background()
	for _, id := range []string{"termination", "cobra-from-employer"} {
		if err := sb.Send(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	acme := view(t, sb, p, "cust_acme")
	term := acme.Files[1]
	if len(term.Loops) != 1 || term.Loops[0] != (portal.Loop{Name: "Priya Patel", Action: "terminated", Outcome: "applied", Message: term.Loops[0].Message}) {
		t.Errorf("termination file: %+v", term)
	}
	if f := acme.Files[0]; f.Exceptions != 1 || f.Loops[0].Outcome != "exception" || f.Loops[0].Message == "" {
		t.Errorf("COBRA from the employer: %+v", f)
	}
	priya, err := sb.Member(ctx, "Priya")
	if err != nil {
		t.Fatal(err)
	}
	h, err := p.Member(ctx, "cust_acme", priya.GetId(), sb.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Versions) != 2 || h.Versions[0].File != "ACMEHR_000000200.834" || h.Versions[1].File != "ACMEHR_000000203.834" ||
		h.Versions[1].Spans[0].To != "2026-03-31" {
		t.Errorf("Priya's history: %+v", h.Versions)
	}
	if _, err := p.Member(ctx, "cust_brightline", priya.GetId(), sb.Now()); err != portal.ErrNotYours {
		t.Errorf("Brightline asking for Acme's member: %v", err)
	}
}

// Checking a file reads it and loads nothing.
func TestCheckFileLoadsNothing(t *testing.T) {
	sb, p := sandbox(t)
	ctx := context.Background()
	var text string
	for _, x := range enrollmentintake.Samples() {
		if x.ID == "new-hire" {
			text = x.Text
		}
	}
	before := view(t, sb, p, "cust_acme")
	c, err := p.CheckFile(ctx, "cust_acme", []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if c.Error != "" || !c.SenderOK || c.Full || len(c.Sets) != 1 || !c.Sets[0].Accepted || len(c.Sets[0].Loops) != 3 ||
		c.Sets[0].Loops[0].Name != "Sam Nguyen" || c.Sets[0].Loops[0].Action != "Add" {
		t.Errorf("check: %+v", c)
	}
	if other, err := p.CheckFile(ctx, "cust_brightline", []byte(text)); err != nil || other.SenderOK {
		t.Errorf("Acme's sender checked as Brightline: %+v, %v", other, err)
	}
	after := view(t, sb, p, "cust_acme")
	if len(after.Files) != len(before.Files) || names(after) != names(before) {
		t.Errorf("checking a file loaded it: %d files, %s", len(after.Files), names(after))
	}
	if s := p.Samples("cust_brightline"); len(s) == 0 || !strings.HasPrefix(s[0].Filename, "BRIGHTHR") {
		t.Errorf("Brightline samples: %+v", s)
	}
}

// The demo's larger Acme roster loads through intake and eligibility like
// any other file, and leaves Brightline alone.
func TestLargeRoster(t *testing.T) {
	sb, p := sandbox(t)
	start := time.Now()
	if err := sb.Load(context.Background(), enrollmentintake.LargeRoster()...); err != nil {
		t.Fatal(err)
	}
	t.Logf("loaded in %s; sandbox clock %s", time.Since(start), sb.Now())
	start = time.Now()
	acme := view(t, sb, p, "cust_acme")
	t.Logf("view in %s: %d families, %d covered, %d employees, %d on COBRA, %d files, tiers %+v", time.Since(start), len(acme.Families), acme.Covered, acme.Subscribers, acme.COBRA, len(acme.Files), acme.Tiers)
	if len(acme.Families) != 107 || acme.Subscribers < 80 || acme.Subscribers+acme.Dependents+acme.COBRA != acme.Covered || acme.Covered < 200 || acme.COBRA < 8 {
		t.Errorf("Acme: %d families, %d employees, %d covered, %d on COBRA", len(acme.Families), acme.Subscribers, acme.Covered, acme.COBRA)
	}
	for _, f := range acme.Files {
		if f.Exceptions != 0 || f.Status != "accepted" {
			t.Errorf("%s: %s, %d exceptions", f.Filename, f.Status, f.Exceptions)
		}
	}
	if sb.Now().After(time.Date(2026, 3, 16, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("the sandbox's clock ran to %s", sb.Now())
	}
	if b := view(t, sb, p, "cust_brightline"); b.Covered != 3 {
		t.Errorf("Brightline: %d covered", b.Covered)
	}
}
