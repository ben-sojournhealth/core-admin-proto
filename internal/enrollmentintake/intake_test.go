package enrollmentintake_test

import (
	"context"
	"coreadmin/internal/repo/repotest"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/enrollmentintake"
	"coreadmin/internal/x12"
)

// received records what intake hands over, and nothing else happens:
// these tests are about what intake produces, not how eligibility applies
// it.
type received struct {
	txns []*eligibilityv1.EnrollmentTransaction
}

func (r *received) Receive(_ context.Context, t *eligibilityv1.EnrollmentTransaction) (*eligibilityv1.EnrollmentTransaction, error) {
	r.txns = append(r.txns, proto.Clone(t).(*eligibilityv1.EnrollmentTransaction))
	return t, nil
}

func setup(t *testing.T) (*enrollmentintake.Processor, *received) {
	r := &received{}
	clock := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	return &enrollmentintake.Processor{
		Repos: repotest.New(t), Eligibility: r, Files: map[string][]byte{}, ReceiverID: enrollmentintake.OurID,
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	}, r
}

func sample(t *testing.T, id string) enrollmentintake.Sample {
	t.Helper()
	for _, x := range enrollmentintake.Samples() {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("no sample %s", id)
	return enrollmentintake.Sample{}
}

func ingest(t *testing.T, p *enrollmentintake.Processor, r *received, name, text string) (*ingestionv1.IngestionJob, []*eligibilityv1.EnrollmentTransaction) {
	t.Helper()
	before := len(r.txns)
	job, err := p.Ingest(context.Background(), name, []byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return job, r.txns[before:]
}

func ingestSample(t *testing.T, p *enrollmentintake.Processor, r *received, id string) (*ingestionv1.IngestionJob, []*eligibilityv1.EnrollmentTransaction) {
	x := sample(t, id)
	return ingest(t, p, r, x.Filename, x.Text)
}

func ref(refs []*eligibilityv1.QualifiedRef, q string) string {
	for _, r := range refs {
		if r.GetQualifier() == q {
			return r.GetValue()
		}
	}
	return ""
}

func date(dates []*eligibilityv1.QualifiedDate, q string) string {
	for _, d := range dates {
		if d.GetQualifier() == q {
			x := d.GetDate()
			return time.Date(int(x.GetYear()), time.Month(x.GetMonth()), int(x.GetDay()), 0, 0, 0, 0, time.UTC).Format("20060102")
		}
	}
	return ""
}

func check(t *testing.T, checks map[string][2]any) {
	t.Helper()
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %v, want %v", name, c[0], c[1])
		}
	}
}

func TestMapsMemberLoopsVerbatim(t *testing.T) {
	p, r := setup(t)
	job, got := ingestSample(t, p, r, "new-hire")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED || len(got) != 3 {
		t.Fatalf("job %s, %d transactions", job.GetStatus(), len(got))
	}
	sam, linh := got[0], got[1]
	h := sam.GetHeader()
	cov := sam.GetCoverages()[0]
	check(t, map[string][2]any{
		"subscriber":        {sam.GetSubscriber(), true},
		"relationship":      {sam.GetRelationshipCode(), "18"},
		"maintenance":       {sam.GetMaintenanceType(), eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_ADDITION},
		"reason":            {sam.GetMaintenanceReasonCode(), "28"},
		"benefit status":    {sam.GetBenefitStatus(), eligibilityv1.BenefitStatus_BENEFIT_STATUS_ACTIVE},
		"employment":        {sam.GetEmploymentStatusCode(), "FT"},
		"subscriber number": {ref(sam.GetRefs(), "0F"), "E10501"},
		"group number":      {ref(sam.GetRefs(), "1L"), "ACME-PPO-01"},
		"eligibility begin": {date(sam.GetDates(), "356"), "20260401"},
		"employment begin":  {date(sam.GetDates(), "336"), "20260316"},
		"name":              {sam.GetName().GetLast() + "," + sam.GetName().GetFirst(), "NGUYEN,SAM"},
		"id qualifier":      {sam.GetMemberIdQualifier(), "34"},
		"member id":         {sam.GetMemberId(), "999105001"},
		"phone":             {ref(sam.GetContacts(), "TE"), "9375550111"},
		"street":            {strings.Join(sam.GetAddress().GetLines(), " "), "42 ELM ST"},
		"zip":               {sam.GetAddress().GetPostalCode(), "45402"},
		"born":              {sam.GetBirthDate().GetYear(), int32(1990)},
		"gender":            {sam.GetGenderCode(), "M"},
		"coverage line":     {cov.GetInsuranceLineCode(), "HLT"},
		"coverage plan":     {cov.GetPlanCoverageDescription(), "PPO"},
		"coverage level":    {cov.GetCoverageLevelCode(), "FAM"},
		"coverage begin":    {date(cov.GetDates(), "348"), "20260401"},
		"sponsor":           {h.GetSponsor().GetName(), "ACME MANUFACTURING"},
		"sponsor tax id":    {h.GetSponsor().GetIdQualifier() + h.GetSponsor().GetId(), "FI831234567"},
		"payer":             {h.GetPayer().GetName(), "COREADMIN TPA"},
		"action":            {h.GetAction(), eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_CHANGE},
		"purpose":           {h.GetPurpose(), eligibilityv1.EnrollmentPurpose_ENROLLMENT_PURPOSE_ORIGINAL},
		"coverage maint":    {cov.GetMaintenanceType(), eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_ADDITION},
		"all recognized":    {len(sam.GetUnrecognizedCodes()) + len(h.GetUnrecognizedCodes()), 0},
		"master policy":     {ref(h.GetRefs(), "38"), "ACME-PPO"},
		"file date":         {date(h.GetDates(), "007"), "20260316"},
		"sender":            {sam.GetSource().GetSenderId(), "ACMEHR"},
		"source job":        {sam.GetSource().GetIngestionJobId(), job.GetId()},
		"source set":        {sam.GetSource().GetTransactionSetControlNumber(), "0001"},
		"spouse":            {linh.GetSubscriber(), false},
		"spouse relation":   {linh.GetRelationshipCode(), "01"},
		"spouse sub number": {ref(linh.GetRefs(), "0F"), "E10501"},
		"nothing left over": {len(sam.GetUnmappedSegments()) + len(h.GetUnmappedSegments()), 0},
	})
	if sam.GetSource().GetFirstSegment() >= sam.GetSource().GetLastSegment() || sam.GetSource().GetLastSegment() >= linh.GetSource().GetFirstSegment() {
		t.Errorf("segment ranges: %v then %v", sam.GetSource(), linh.GetSource())
	}
}

func TestMapsTermsCOBRAAndFullFiles(t *testing.T) {
	p, r := setup(t)
	_, term := ingestSample(t, p, r, "termination")
	_, retro := ingestSample(t, p, r, "retro-term")
	_, cobra := ingestSample(t, p, r, "cobra")
	_, full := ingestSample(t, p, r, "full-file")
	_, baby := ingestSample(t, p, r, "newborn")
	check(t, map[string][2]any{
		"term maintenance":    {term[0].GetMaintenanceType(), eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION},
		"term reason":         {term[0].GetMaintenanceReasonCode(), "08"},
		"term end":            {date(term[0].GetDates(), "357"), "20260331"},
		"term coverage end":   {date(term[0].GetCoverages()[0].GetDates(), "349"), "20260331"},
		"term coverage maint": {term[0].GetCoverages()[0].GetMaintenanceType(), eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION},
		"retro end":           {date(retro[0].GetDates(), "357"), "20260131"},
		"cobra status":        {cobra[0].GetBenefitStatus(), eligibilityv1.BenefitStatus_BENEFIT_STATUS_COBRA},
		"cobra event":         {cobra[0].GetCobraQualifyingEventCode(), "1"},
		"cobra begin":         {date(cobra[0].GetDates(), "340"), "20260401"},
		"full file":           {full[0].GetHeader().GetAction(), eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_REPLACE},
		"full members":        {len(full), 4},
		"audit":               {full[2].GetMaintenanceType(), eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_AUDIT},
		"newborn alone":       {len(baby), 1},
		"newborn dependent":   {baby[0].GetSubscriber(), false},
		"newborn reason":      {baby[0].GetMaintenanceReasonCode(), "02"},
		"newborn family":      {ref(baby[0].GetRefs(), "0F"), "E10101"},
	})
	// An address change carries no coverage loop.
	_, addr := ingestSample(t, p, r, "address-change")
	if len(addr[0].GetCoverages()) != 0 || addr[0].GetAddress().GetLines()[0] != "1150 RIVERSIDE DR" {
		t.Errorf("address change: %v", addr[0])
	}
}

// Segments intake doesn't map are kept raw, so nothing is dropped.
func TestKeepsUnmappedSegments(t *testing.T) {
	p, r := setup(t)
	text := strings.Replace(sample(t, "newborn").Text, "DMG*", "LUI*LE*SPA~\nDMG*", 1)
	text = strings.Replace(text, "N1*P5*", "QTY*TO*1~\nN1*P5*", 1)
	text = strings.Replace(text, "000000202", "000000299", -1)
	// SE01 grows by the two segments added.
	ic, _ := x12.Parse([]byte(sample(t, "newborn").Text))
	n := len(ic.Groups[0].Sets[0].Body) + 2
	text = strings.Replace(text, "SE*"+itoa(n)+"*", "SE*"+itoa(n+2)+"*", 1)
	_, got := ingest(t, p, r, "extra.834", text)
	if len(got) != 1 {
		t.Fatalf("%d transactions", len(got))
	}
	if u := got[0].GetUnmappedSegments(); len(u) != 1 || u[0] != "LUI*LE*SPA~" {
		t.Errorf("member unmapped: %v", u)
	}
	if u := got[0].GetHeader().GetUnmappedSegments(); len(u) != 1 || u[0] != "QTY*TO*1~" {
		t.Errorf("header unmapped: %v", u)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRejectedSetLoadsNothing(t *testing.T) {
	p, r := setup(t)
	job, got := ingestSample(t, p, r, "bad-set")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED_WITH_ERRORS || len(got) != 1 ||
		got[0].GetName().GetFirst() != "RILEY" {
		t.Fatalf("job %s, handed over %d", job.GetStatus(), len(got))
	}
	sets := job.GetTransactionSets()
	if !sets[0].GetAccepted() || sets[1].GetAccepted() || sets[1].GetErrors()[0].GetCode() != "IK4-8" {
		t.Errorf("sets %v", sets)
	}
	ack := string(p.Files[job.GetAcknowledgmentUri()])
	for _, want := range []string{"AK1*BE*208*005010X220A1", "AK2*834*0001", "IK5*A", "AK2*834*0002", "IK3*DTP*", "IK4*3**8*20261340", "IK5*R*5", "AK9*P*2*2*1"} {
		if !strings.Contains(ack, want) {
			t.Errorf("999 lacks %s:\n%s", want, ack)
		}
	}

	job, got = ingestSample(t, p, r, "bad-envelope")
	if len(got) != 0 || job.GetTransactionSets()[0].GetAccepted() || !strings.Contains(string(p.Files[job.GetAcknowledgmentUri()]), "IK5*R*4") {
		t.Errorf("bad envelope: handed over %d, sets %v", len(got), job.GetTransactionSets())
	}
}

// A code intake doesn't recognize passes the 999 and arrives as UNKNOWN,
// with the code kept as sent: eligibility decides what to do with it.
func TestUnrecognizedCodeIsKept(t *testing.T) {
	p, r := setup(t)
	job, got := ingestSample(t, p, r, "unknown-code")
	if !job.GetTransactionSets()[0].GetAccepted() || len(got) != 1 {
		t.Fatalf("sets %v, handed over %d", job.GetTransactionSets(), len(got))
	}
	u := got[0].GetUnrecognizedCodes()
	if got[0].GetMaintenanceType() != eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_UNKNOWN ||
		len(u) != 1 || u[0].GetElement() != "INS03" || u[0].GetValue() != "099" {
		t.Errorf("maintenance %s, unrecognized %v", got[0].GetMaintenanceType(), u)
	}
	// Recognized codes in the same loop are unaffected.
	if got[0].GetBenefitStatus() != eligibilityv1.BenefitStatus_BENEFIT_STATUS_ACTIVE {
		t.Errorf("benefit status %s", got[0].GetBenefitStatus())
	}
}

// A member loop without its subscriber number can't be tied to anyone, so
// the set is rejected (IK3 code 3, required segment missing).
func TestMissingSubscriberNumberRejects(t *testing.T) {
	p, r := setup(t)
	text := strings.Replace(sample(t, "newborn").Text, "REF*0F*E10101~\n", "", 1)
	text = strings.Replace(text, "000000202", "000000298", -1)
	ic, _ := x12.Parse([]byte(sample(t, "newborn").Text))
	n := len(ic.Groups[0].Sets[0].Body) + 2
	text = strings.Replace(text, "SE*"+itoa(n)+"*", "SE*"+itoa(n-1)+"*", 1)
	job, got := ingest(t, p, r, "noref.834", text)
	if len(got) != 0 || job.GetTransactionSets()[0].GetErrors()[0].GetCode() != "IK3-3" {
		t.Errorf("handed over %d, errors %v", len(got), job.GetTransactionSets()[0].GetErrors())
	}
}

func TestDuplicateFileLoadsNothing(t *testing.T) {
	p, r := setup(t)
	ingestSample(t, p, r, "new-hire")
	job, got := ingestSample(t, p, r, "new-hire")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED || len(got) != 0 ||
		job.GetErrors()[0].GetCode() != "DUPLICATE_INTERCHANGE" {
		t.Errorf("resend: %s, handed over %d, %v", job.GetStatus(), len(got), job.GetErrors())
	}
}

func TestPreviewLoadsNothing(t *testing.T) {
	p, r := setup(t)
	v, err := p.Preview(context.Background(), []byte(sample(t, "new-hire").Text))
	if err != nil {
		t.Fatal(err)
	}
	if jobs, _ := p.Jobs(context.Background()); len(jobs) != 0 || len(r.txns) != 0 {
		t.Fatalf("preview left %d jobs, %d transactions", len(jobs), len(r.txns))
	}
	var first map[string]any
	if err := json.Unmarshal(v.Sets[0].Members[0].Mapped, &first); err != nil {
		t.Fatal(err)
	}
	if n := len(v.Sets[0].Members); n != 3 || first["maintenance_type"] != "MAINTENANCE_TYPE_ADDITION" || v.Mode != "change" || v.Sponsor != "Acme Manufacturing" {
		t.Errorf("preview: %d members, %v, mode %s, sponsor %s", n, first["maintenance_type"], v.Mode, v.Sponsor)
	}
}
