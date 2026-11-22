package eligibilitydemo

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/eligibility"
)

var store1000 = store.ListOptions[*eligibilityv1.MemberVersion]{PageSize: 1_000_000}

// Tour is the walkthrough's data. Nothing in it is written by hand: each
// example plays samples through a fresh sandbox and reports what
// eligibility did.
type Tour struct {
	Examples map[string]*Example `json:"examples"`
	// Checks run after sending every sample and approving every full file.
	Checks   []eligibility.Check `json:"checks"`
	CheckRun string              `json:"checkRun"`
}

type Example struct {
	Member string `json:"member"`
	// The files sent after open enrollment, in order.
	Steps      []Step                     `json:"steps"`
	Versions   []eligibility.VersionView  `json:"versions"`
	Assertions []AssertionView            `json:"assertions"`
	Questions  []Question                 `json:"questions"`
	Proposals  []eligibility.ProposalView `json:"proposals"`
	// Versions added across the whole sandbox by the story's steps.
	VersionsAdded int `json:"versionsAdded"`
	// Free-form numbers an example reports.
	Facts map[string]any `json:"facts,omitempty"`
	// The full-file example: who was enrolled, who the file listed, and
	// what happened to each person it left out.
	FullFile *FullFileView `json:"fullFile,omitempty"`
}

type FullFileView struct {
	File      string `json:"file"`
	Effective string `json:"effective"`
	// Members of the customer covered on the effective date, before the
	// file arrived.
	Enrolled []string `json:"enrolled"`
	// Members the file lists.
	InFile []string `json:"inFile"`
	// Each member it left out: proposed for termination.
	LeftOut []LeftOutView `json:"leftOut"`
	// Member versions the file added before anyone approved.
	VersionsBeforeApproval int `json:"versionsBeforeApproval"`
}

type LeftOutView struct {
	Name string `json:"name"`
	// The input that enrolled them, and its file.
	EnrolledBy     string                 `json:"enrolledBy"`
	EnrolledByFile string                 `json:"enrolledByFile"`
	Before         []eligibility.SpanView `json:"before"`
	After          []eligibility.SpanView `json:"after"`
}

type Step struct {
	Sequence int64  `json:"sequence"`
	Recorded string `json:"recorded"`
	By       string `json:"by"`
	Title    string `json:"title"`
	File     string `json:"file"`
	Status   string `json:"status"`
	Message  string `json:"message"`
}

type AssertionView struct {
	Sequence int64  `json:"sequence"`
	Cause    string `json:"cause"`
	By       string `json:"by"`
	From     string `json:"from"`
	Through  string `json:"through,omitempty"`
	Covered  bool   `json:"covered"`
	COBRA    bool   `json:"cobra"`
	Ignored  bool   `json:"ignored"`
}

// Question is an as-of question: what did we believe at a moment about
// one date of coverage.
type Question struct {
	Asked  string `json:"asked"`
	About  string `json:"about"`
	Answer string `json:"answer"`
}

// BuildTour plays every example.
func BuildTour(ctx context.Context) (*Tour, error) {
	// Each example's sandbox is a schema of its own, dropped at the end.
	var drops []func()
	defer func() {
		for _, d := range drops {
			d()
		}
	}()
	ctx = context.WithValue(ctx, dropsKey{}, &drops)
	t := &Tour{Examples: map[string]*Example{}}
	for name, f := range map[string]func(context.Context) (*Example, error){
		"retro": retro, "fullfile": fullFile, "cobra": cobra, "exceptions": exceptions, "regen": regen,
	} {
		ex, err := f(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		t.Examples[name] = ex
	}
	checks, run, err := checkEverything(ctx)
	if err != nil {
		return nil, err
	}
	t.Checks, t.CheckRun = checks, run
	return t, nil
}

// story is a sandbox playing one example.
type story struct {
	sb       *Sandbox
	ctx      context.Context
	start    int64 // inputs before the story's steps
	versions int
}

type dropsKey struct{}

func newStory(ctx context.Context) (*story, error) {
	repos, drop, err := repo.NewSandbox(ctx)
	if err != nil {
		return nil, err
	}
	drops, ok := ctx.Value(dropsKey{}).(*[]func())
	if !ok {
		drop()
		return nil, fmt.Errorf("eligibilitydemo: a story runs only inside BuildTour")
	}
	*drops = append(*drops, drop)
	sb, err := NewSandboxIn(repos)
	if err != nil {
		return nil, err
	}
	ins, _ := sb.Elig.Inputs(ctx)
	st := &story{sb: sb, ctx: ctx, start: int64(len(ins))}
	st.versions = st.countVersions()
	return st, nil
}

func (s *story) countVersions() int {
	vs, _, _ := s.sb.Repos.MemberVersions.List(s.ctx, store1000)
	return len(vs)
}

func (s *story) send(ids ...string) error {
	for _, id := range ids {
		if err := s.sb.Send(s.ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// finish fills in the example for the focus member.
func (s *story) finish(first string, ex *Example) (*Example, error) {
	ov, err := s.sb.Elig.Overview(s.ctx, s.sb.Now())
	if err != nil {
		return nil, err
	}
	titles := map[int64]eligibility.InputView{}
	for i := len(ov.Inputs) - 1; i >= 0; i-- {
		in := ov.Inputs[i]
		titles[in.Sequence] = in
		if in.Sequence > s.start {
			ex.Steps = append(ex.Steps, Step{Sequence: in.Sequence, Recorded: in.Recorded, By: in.By, Title: in.Title,
				File: in.File, Status: in.Status, Message: in.Message})
		}
	}
	ex.Proposals = ov.Proposals
	ex.VersionsAdded = s.countVersions() - s.versions
	if first == "" {
		return ex, nil
	}
	m, err := s.sb.Member(s.ctx, first)
	if err != nil {
		return nil, err
	}
	ex.Member = m.GetName().GetFirst() + " " + m.GetName().GetLast()
	d, err := s.sb.Elig.Member(s.ctx, m.GetId(), s.sb.Now())
	if err != nil {
		return nil, err
	}
	ex.Versions = d.Versions
	st, err := s.sb.Elig.State(s.ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range st.Assertions[m.GetId()] {
		in := titles[a.Sequence]
		av := AssertionView{Sequence: a.Sequence, Cause: in.Title, By: a.Sender, From: a.From.Format("2006-01-02"),
			Covered: a.Covered, COBRA: a.COBRA, Ignored: a.Ignored}
		if in.Kind == "approval" {
			av.Cause = "Full-file approval"
		}
		if !a.Through.IsZero() {
			av.Through = a.Through.Format("2006-01-02")
		}
		ex.Assertions = append(ex.Assertions, av)
	}
	return ex, nil
}

// ask records what we believed at a moment about a date.
func (s *story) ask(ex *Example, first string, asked time.Time, about string) error {
	m, err := s.sb.Member(s.ctx, first)
	if err != nil {
		return err
	}
	v, err := s.sb.Elig.AsOf(s.ctx, m.GetId(), asked)
	if err != nil {
		return err
	}
	answer := "not on file yet"
	if v != nil {
		answer = coverageOn(v.GetMember(), about) + fmt.Sprintf(" (v%d)", v.GetVersion())
	}
	ex.Questions = append(ex.Questions, Question{Asked: asked.Format(time.RFC3339), About: about, Answer: answer})
	return nil
}

func coverageOn(m *eligibilityv1.Member, day string) string {
	d, _ := time.Parse("2006-01-02", day)
	on := &commonv1.Date{Year: int32(d.Year()), Month: int32(d.Month()), Day: int32(d.Day())}
	for _, c := range m.GetCoverage() {
		e := c.GetEffective()
		if cmp(e.GetStart(), on) <= 0 && (e.GetEnd() == nil || cmp(on, e.GetEnd()) <= 0) {
			tier := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(c.GetTier().String(), "COVERAGE_TIER_"), "_", " "))
			if c.GetCobra() {
				return "covered, COBRA"
			}
			return "covered, " + tier
		}
	}
	return "not covered"
}

func cmp(a, b *commonv1.Date) int {
	x := a.GetYear()*10000 + a.GetMonth()*100 + a.GetDay()
	y := b.GetYear()*10000 + b.GetMonth()*100 + b.GetDay()
	switch {
	case x < y:
		return -1
	case x > y:
		return 1
	}
	return 0
}

// retro: Chidi is terminated back to January 31, then reinstated. Three
// versions; the third is back where the first was; asking as of each
// moment gives what we believed then.
func retro(ctx context.Context) (*Example, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, err
	}
	ex := &Example{}
	t0 := s.sb.Now()
	if err := s.send("retro-term"); err != nil {
		return nil, err
	}
	t1 := s.sb.Now()
	if err := s.send("reinstatement"); err != nil {
		return nil, err
	}
	t2 := s.sb.Now()
	for _, at := range []time.Time{t0, t1, t2} {
		if err := s.ask(ex, "Chidi", at, "2026-02-10"); err != nil {
			return nil, err
		}
	}
	if err := s.ask(ex, "Chidi", t1, "2026-01-20"); err != nil {
		return nil, err
	}
	return s.finish("Chidi", ex)
}

// fullFile: the April full file leaves out the Patels and the newborn.
// Nothing changes until the proposal is approved; approving ends all
// three.
func fullFile(ctx context.Context) (*Example, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.send("newborn"); err != nil {
		return nil, err
	}
	const effective = "2026-04-01"
	ff := &FullFileView{Effective: effective, Enrolled: []string{}, InFile: []string{}, LeftOut: []LeftOutView{}}
	members, err := s.acme()
	if err != nil {
		return nil, err
	}
	for _, m := range members {
		if coverageOn(m, effective) != "not covered" {
			ff.Enrolled = append(ff.Enrolled, fullName(m))
		}
	}
	before := s.countVersions()
	if err := s.send("full-file"); err != nil {
		return nil, err
	}
	ff.VersionsBeforeApproval = s.countVersions() - before
	ov, err := s.sb.Elig.Overview(ctx, s.sb.Now())
	if err != nil {
		return nil, err
	}
	p := ov.Proposals[0]
	ff.File = p.File
	inputs := map[string]eligibility.InputView{}
	for _, in := range ov.Inputs {
		inputs[in.ID] = in
		if in.File == p.File {
			ff.InFile = append([]string{strings.SplitN(in.Title, " · ", 2)[0]}, ff.InFile...)
		}
	}
	for _, t := range p.Terms {
		d, err := s.sb.Elig.Member(ctx, t.MemberID, s.sb.Now())
		if err != nil {
			return nil, err
		}
		first := inputs[d.Versions[0].Input]
		ff.LeftOut = append(ff.LeftOut, LeftOutView{Name: t.Name, EnrolledBy: first.Title, EnrolledByFile: first.File, Before: d.Coverage})
	}
	if err := s.sb.Elig.ApproveFullFile(ctx, p.JobID, p.Set, "confirmed with Acme HR"); err != nil {
		return nil, err
	}
	for i, t := range p.Terms {
		d, err := s.sb.Elig.Member(ctx, t.MemberID, s.sb.Now())
		if err != nil {
			return nil, err
		}
		ff.LeftOut[i].After = d.Coverage
	}
	return s.finish("Arjun", &Example{FullFile: ff})
}

// acme lists Acme's current members, by card.
func (s *story) acme() ([]*eligibilityv1.Member, error) {
	ms, _, err := s.sb.Repos.Members.List(s.ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1000,
		Filter: func(m *eligibilityv1.Member) bool { return m.GetCustomerId() == "cust_acme" }})
	sort.Slice(ms, func(i, j int) bool { return ms[i].GetCardId() < ms[j].GetCardId() })
	return ms, err
}

func fullName(m *eligibilityv1.Member) string {
	return m.GetName().GetFirst() + " " + m.GetName().GetLast()
}

// cobra: the COBRA vendor takes Priya over from April 1; the employer's
// termination, sent after, doesn't end it; the employer's full file
// doesn't propose ending her coverage.
func cobra(ctx context.Context) (*Example, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.send("cobra", "termination", "full-file"); err != nil {
		return nil, err
	}
	ex := &Example{}
	if err := s.ask(ex, "Priya", s.sb.Now(), "2026-05-15"); err != nil {
		return nil, err
	}
	return s.finish("Priya", ex)
}

// exceptions: inputs that can't be applied change nothing.
func exceptions(ctx context.Context) (*Example, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.send("unknown-code", "cobra-from-employer"); err != nil {
		return nil, err
	}
	return s.finish("", &Example{})
}

// regen: the current members can always be rebuilt by replaying the
// inputs. Shown two ways: into an empty database, and under the rules as
// they were before sender authority, to see what that change did.
func regen(ctx context.Context) (*Example, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.send("new-hire", "newborn", "termination", "retro-term", "reinstatement", "address-change",
		"brightline-term", "cobra", "full-file"); err != nil {
		return nil, err
	}
	st, err := s.sb.Elig.State(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range st.Proposals {
		if err := s.sb.Elig.ApproveFullFile(ctx, p.JobID, p.SetControl, "confirmed"); err != nil {
			return nil, err
		}
	}

	// 1. An empty database given only the inputs (and the customers and
	// plans they refer to) replays to the same members.
	rebuilt, err := RebuildElsewhere(ctx, s.sb)
	if err != nil {
		return nil, err
	}

	// 2. The same inputs under the earlier rules.
	changes, err := s.sb.Elig.Compare(ctx, eligibility.BeforeSenderAuthority)
	if err != nil {
		return nil, err
	}
	var changed []map[string]any
	for _, c := range changes {
		changed = append(changed, map[string]any{
			"name": fullName(c.After), "now": spanViews(c.Before), "then": spanViews(c.After),
		})
	}
	return s.finish("", &Example{Facts: map[string]any{
		"rebuilt": rebuilt, "rulesNow": eligibility.Current.Version, "rulesThen": eligibility.BeforeSenderAuthority.Version,
		"changed": changed,
	}})
}

// Rebuilt reports a replay into a fresh database.
type Rebuilt struct {
	Inputs      int `json:"inputs"`
	Members     int `json:"members"`
	Differences int `json:"differences"`
}

// RebuildElsewhere copies only a sandbox's inputs (the input log and the
// 834 loops it refers to) into a brand-new database with the seed
// customers and plans, replays them there, and compares every member,
// including ids and card numbers, with the sandbox's.
func RebuildElsewhere(ctx context.Context, sb *Sandbox) (*Rebuilt, error) {
	empty, drop, err := repo.NewSandbox(ctx)
	if err != nil {
		return nil, err
	}
	defer drop()
	fresh, err := Prepare(empty)
	if err != nil {
		return nil, err
	}
	txns, _, err := sb.Repos.EnrollmentTransactions.List(ctx, store.ListOptions[*eligibilityv1.EnrollmentTransaction]{PageSize: 1_000_000})
	if err != nil {
		return nil, err
	}
	for _, t := range txns {
		if _, err := fresh.EnrollmentTransactions.Create(ctx, t); err != nil {
			return nil, err
		}
	}
	ins, err := sb.Elig.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range ins {
		if _, err := fresh.EligibilityInputs.Create(ctx, in); err != nil {
			return nil, err
		}
	}
	copied, _, err := fresh.EligibilityInputs.List(ctx, store.ListOptions[*eligibilityv1.EligibilityInput]{PageSize: 1_000_000})
	if err != nil {
		return nil, err
	}
	st, err := eligibility.Derive(ctx, fresh, copied)
	if err != nil {
		return nil, err
	}
	members, _, err := sb.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1_000_000})
	if err != nil {
		return nil, err
	}
	r := &Rebuilt{Inputs: len(copied), Members: len(st.Members)}
	for _, m := range members {
		if again := st.Members[m.GetId()]; again == nil || !sameMember(m, again) {
			r.Differences++
		}
	}
	if len(members) != len(st.Members) {
		r.Differences++
	}
	return r, nil
}

func sameMember(a, b *eligibilityv1.Member) bool {
	a, b = proto.Clone(a).(*eligibilityv1.Member), proto.Clone(b).(*eligibilityv1.Member)
	a.Audit, b.Audit = nil, nil
	return proto.Equal(a, b)
}

func spanViews(m *eligibilityv1.Member) []eligibility.SpanView {
	out := []eligibility.SpanView{}
	for _, c := range m.GetCoverage() {
		sv := eligibility.SpanView{From: iso(c.GetEffective().GetStart()), COBRA: c.GetCobra()}
		if e := c.GetEffective().GetEnd(); e != nil {
			sv.To = iso(e)
		}
		out = append(out, sv)
	}
	return out
}

func iso(d *commonv1.Date) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

// checkEverything sends every sample that loads, approves every proposal,
// and runs the checks.
func checkEverything(ctx context.Context) ([]eligibility.Check, string, error) {
	s, err := newStory(ctx)
	if err != nil {
		return nil, "", err
	}
	ids := []string{"new-hire", "newborn", "termination", "retro-term", "reinstatement", "address-change", "brightline-term",
		"cobra", "cobra-from-employer", "full-file", "cobra-full-file", "unknown-code"}
	if err := s.send(ids...); err != nil {
		return nil, "", err
	}
	st, err := s.sb.Elig.State(ctx)
	if err != nil {
		return nil, "", err
	}
	for _, p := range st.Proposals {
		if err := s.sb.Elig.ApproveFullFile(ctx, p.JobID, p.SetControl, "ok"); err != nil {
			return nil, "", err
		}
	}
	checks, err := s.sb.Elig.Verify(ctx)
	ins, _ := s.sb.Elig.Inputs(ctx)
	return checks, fmt.Sprintf("open enrollment for both clients, %d sample files, every full file approved: %d inputs", len(ids), len(ins)), err
}
