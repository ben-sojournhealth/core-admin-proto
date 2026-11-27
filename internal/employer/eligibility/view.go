package eligibility

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
)

// Overview is the Eligibility demo's main view.
type Overview struct {
	Today     string         `json:"today"`
	Families  []FamilyView   `json:"families"`
	Inputs    []InputView    `json:"inputs"`
	Proposals []ProposalView `json:"proposals"`
}

type FamilyView struct {
	Customer         string          `json:"customer"`
	SubscriberNumber string          `json:"subscriberNumber"`
	Name             string          `json:"name"`
	Members          []MemberSummary `json:"members"`
}

type MemberSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Relationship string `json:"relationship"`
	Card         string `json:"card"`
	// On today's date: covered, ended (had coverage), future, or none.
	Status   string `json:"status"`
	COBRA    bool   `json:"cobra"`
	Versions int    `json:"versions"`
}

type InputView struct {
	ID       string   `json:"id"`
	Sequence int64    `json:"sequence"`
	Recorded string   `json:"recorded"`
	By       string   `json:"by"`
	Kind     string   `json:"kind"`
	Title    string   `json:"title"`
	File     string   `json:"file,omitempty"`
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Members  []string `json:"members"`
}

type ProposalView struct {
	JobID      string     `json:"jobId"`
	Sender     string     `json:"sender"`
	Set        string     `json:"set"`
	File       string     `json:"file"`
	Effective  string     `json:"effective"`
	Terms      []TermView `json:"terms"`
	Approved   bool       `json:"approved"`
	ApprovedBy string     `json:"approvedBy,omitempty"`
}

type TermView struct {
	MemberID string `json:"memberId"`
	Name     string `json:"name"`
	End      string `json:"end"`
	COBRA    bool   `json:"cobra"`
}

// MemberDetail is one member: now, and every version.
type MemberDetail struct {
	MemberSummary
	Family   string          `json:"family"`
	Born     string          `json:"born"`
	Address  string          `json:"address"`
	Coverage []SpanView      `json:"coverage"`
	Versions []VersionView   `json:"versions"`
	Record   json.RawMessage `json:"record"`
}

type SpanView struct {
	From  string `json:"from"`
	To    string `json:"to,omitempty"`
	Tier  string `json:"tier"`
	Plan  string `json:"plan"`
	COBRA bool   `json:"cobra"`
}

type VersionView struct {
	Version  int32  `json:"version"`
	Recorded string `json:"recorded"`
	Input    string `json:"input,omitempty"`
	Title    string `json:"title"`
	// The file the input came in, if it came in one.
	File   string     `json:"file,omitempty"`
	Reason string     `json:"reason,omitempty"`
	Spans  []SpanView `json:"spans"`
	// What changed from the version before.
	Changes []string `json:"changes"`
}

var record = protojson.MarshalOptions{UseProtoNames: true}

// Overview builds the main view. today is the sandbox's date.
func (s *Service) Overview(ctx context.Context, today time.Time) (*Overview, error) {
	st, err := s.State(ctx)
	if err != nil {
		return nil, err
	}
	ins, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	units := map[string]*FamilyView{}
	for _, id := range st.MemberIDs() {
		m := st.Members[id]
		names[id] = fullName(m)
		vs, err := s.History(ctx, id)
		if err != nil {
			return nil, err
		}
		sum := summarize(m, today, len(vs))
		sub := subscriberNumber(m)
		key := m.GetCustomerId() + "|" + sub
		f := units[key]
		if f == nil {
			f = &FamilyView{SubscriberNumber: sub, Customer: m.GetCustomerId()}
			if c, err := s.Repos.Customers.Get(ctx, m.GetCustomerId()); err == nil {
				f.Customer = c.GetName()
			}
			units[key] = f
		}
		f.Members = append(f.Members, sum)
		if m.GetRelationship() == eligibilityv1.Relationship_RELATIONSHIP_SELF {
			f.Name = m.GetName().GetLast()
		}
	}
	o := &Overview{Today: today.Format("2006-01-02"), Families: []FamilyView{}, Inputs: []InputView{}, Proposals: []ProposalView{}}
	for _, f := range units {
		sort.Slice(f.Members, func(i, j int) bool { return f.Members[i].Card < f.Members[j].Card })
		if f.Name == "" && len(f.Members) > 0 {
			f.Name = strings.Fields(f.Members[0].Name + " ?")[1]
		}
		o.Families = append(o.Families, *f)
	}
	sort.Slice(o.Families, func(i, j int) bool {
		a, b := o.Families[i], o.Families[j]
		return a.Customer < b.Customer || a.Customer == b.Customer && a.SubscriberNumber < b.SubscriberNumber
	})

	outcomes := map[string]Outcome{}
	for _, oc := range st.Outcomes {
		outcomes[oc.InputID] = oc
	}
	for i := len(ins) - 1; i >= 0; i-- {
		in := ins[i]
		oc := outcomes[in.GetId()]
		v := InputView{ID: in.GetId(), Sequence: in.GetSequence(), Recorded: in.GetRecordedTime().AsTime().Format(time.RFC3339),
			By: in.GetRecordedBy(), Status: oc.Status, Message: oc.Message, Members: []string{}}
		v.Kind, v.Title, v.File = s.describeInput(ctx, in)
		for _, id := range oc.MemberIDs {
			v.Members = append(v.Members, or(names[id], id))
		}
		o.Inputs = append(o.Inputs, v)
	}
	for _, p := range st.Proposals {
		pv := ProposalView{JobID: p.JobID, Sender: p.Sender, Set: p.SetControl, File: s.filename(ctx, p.JobID), Effective: p.Effective.Format("2006-01-02"),
			Approved: p.Approved, ApprovedBy: p.ApprovedBy, Terms: []TermView{}}
		for _, t := range p.Terms {
			pv.Terms = append(pv.Terms, TermView{MemberID: t.MemberID, Name: t.Name, End: t.End.Format("2006-01-02"), COBRA: t.COBRA})
		}
		o.Proposals = append(o.Proposals, pv)
	}
	return o, nil
}

// Member builds one member's view.
func (s *Service) Member(ctx context.Context, id string, today time.Time) (*MemberDetail, error) {
	m, err := s.Repos.Members.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	vs, err := s.History(ctx, id)
	if err != nil {
		return nil, err
	}
	d := &MemberDetail{MemberSummary: summarize(m, today, len(vs)), Coverage: spanViews(m), Versions: []VersionView{}}
	if sub, err := s.Repos.Members.Get(ctx, m.GetSubscriberId()); err == nil {
		d.Family = fullName(sub)
	}
	if b := m.GetBirthDate(); b != nil {
		d.Born = fmt.Sprintf("%04d-%02d-%02d", b.GetYear(), b.GetMonth(), b.GetDay())
	}
	if a := m.GetAddress(); a != nil {
		d.Address = strings.TrimSpace(strings.Join(a.GetLines(), " ") + ", " + a.GetCity() + " " + a.GetState() + " " + a.GetPostalCode())
	}
	d.Record, _ = record.Marshal(m)
	ins, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[string]*eligibilityv1.EligibilityInput{}
	for _, in := range ins {
		byID[in.GetId()] = in
	}
	var prev *eligibilityv1.Member
	for _, v := range vs {
		vv := VersionView{Version: v.GetVersion(), Recorded: v.GetRecordedTime().AsTime().Format(time.RFC3339), Input: v.GetInputId(),
			Reason: v.GetReason(), Spans: spanViews(v.GetMember()), Changes: changes(prev, v.GetMember())}
		if in := byID[v.GetInputId()]; in != nil {
			_, vv.Title, vv.File = s.describeInput(ctx, in)
		} else {
			vv.Title = "Regenerated"
		}
		d.Versions = append(d.Versions, vv)
		prev = v.GetMember()
	}
	return d, nil
}

func (s *Service) describeInput(ctx context.Context, in *eligibilityv1.EligibilityInput) (kind, label, file string) {
	switch x := in.GetInput().(type) {
	case *eligibilityv1.EligibilityInput_EnrollmentTransactionId:
		t, err := s.Repos.EnrollmentTransactions.Get(ctx, x.EnrollmentTransactionId)
		if err != nil {
			return "834", x.EnrollmentTransactionId, ""
		}
		action := strings.ToLower(strings.TrimPrefix(t.GetMaintenanceType().String(), "MAINTENANCE_TYPE_"))
		return "834", fmt.Sprintf("%s %s · %s", title(t.GetName().GetFirst()), title(t.GetName().GetLast()), action),
			s.filename(ctx, t.GetSource().GetIngestionJobId())
	case *eligibilityv1.EligibilityInput_FullFileApproval:
		a := x.FullFileApproval
		t := "Approved full-file terminations"
		if a.GetNote() != "" {
			t += ": " + a.GetNote()
		}
		return "approval", t, s.filename(ctx, a.GetIngestionJobId())
	}
	return "?", in.GetId(), ""
}

func (s *Service) filename(ctx context.Context, jobID string) string {
	if j, err := s.Repos.EnrollmentIngestionJobs.Get(ctx, jobID); err == nil {
		return j.GetOriginalFilename()
	}
	return ""
}

func summarize(m *eligibilityv1.Member, today time.Time, versions int) MemberSummary {
	sum := MemberSummary{ID: m.GetId(), Name: fullName(m), Card: m.GetCardId(), Versions: versions, Status: "none",
		Relationship: title(strings.ReplaceAll(strings.TrimPrefix(m.GetRelationship().String(), "RELATIONSHIP_"), "_", " "))}
	day := dateProto(today)
	for _, c := range m.GetCoverage() {
		e := c.GetEffective()
		switch {
		case cmp(e.GetStart(), day) <= 0 && (e.GetEnd() == nil || cmp(day, e.GetEnd()) <= 0):
			sum.Status, sum.COBRA = "covered", c.GetCobra()
		case cmp(e.GetStart(), day) > 0 && sum.Status != "covered":
			sum.Status = "future"
		case sum.Status == "none":
			sum.Status = "ended"
		}
	}
	return sum
}

func spanViews(m *eligibilityv1.Member) []SpanView {
	out := []SpanView{}
	for _, c := range m.GetCoverage() {
		sv := SpanView{From: iso(c.GetEffective().GetStart()), Plan: c.GetPlanId(), COBRA: c.GetCobra(),
			Tier: title(strings.ReplaceAll(strings.TrimPrefix(c.GetTier().String(), "COVERAGE_TIER_"), "_", " "))}
		if e := c.GetEffective().GetEnd(); e != nil {
			sv.To = iso(e)
		}
		out = append(out, sv)
	}
	return out
}

// changes says, briefly, how b differs from a.
func changes(a, b *eligibilityv1.Member) []string {
	if a == nil {
		return []string{"first recorded"}
	}
	var out []string
	if fmt.Sprint(spanViews(a)) != fmt.Sprint(spanViews(b)) {
		out = append(out, "coverage")
	}
	if fullName(a) != fullName(b) {
		out = append(out, "name")
	}
	if fmt.Sprint(a.GetAddress()) != fmt.Sprint(b.GetAddress()) {
		out = append(out, "address")
	}
	if fmt.Sprint(a.GetBirthDate()) != fmt.Sprint(b.GetBirthDate()) || a.GetGender() != b.GetGender() {
		out = append(out, "demographics")
	}
	if a.GetRelationship() != b.GetRelationship() || a.GetSubscriberId() != b.GetSubscriberId() {
		out = append(out, "family")
	}
	if len(out) == 0 {
		out = append(out, "other")
	}
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

func fullName(m *eligibilityv1.Member) string {
	return strings.TrimSpace(m.GetName().GetFirst() + " " + m.GetName().GetLast())
}

func iso(d *commonv1.Date) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
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

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
