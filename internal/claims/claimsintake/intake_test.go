package claimsintake_test

import (
	"context"
	"coreadmin/internal/common/repo/repotest"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claims/claimsintake"
	"coreadmin/internal/common/x12"
)

// received records what intake hands over, and nothing else happens: these
// tests are about what intake produces, not what adjudication makes of it.
type received struct{ claims []*claimsv1.Claim }

func (r *received) Receive(_ context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error) {
	r.claims = append(r.claims, proto.Clone(c).(*claimsv1.Claim))
	return c, nil
}

func setup(t *testing.T) (*claimsintake.Processor, *received) {
	r := &received{}
	clock := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	return &claimsintake.Processor{
		Repos: repotest.New(t), Claims: r, Files: map[string][]byte{}, ReceiverID: claimsintake.OurID,
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	}, r
}

func sample(t *testing.T, id string) claimsintake.Sample {
	t.Helper()
	for _, x := range claimsintake.Samples() {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("no sample %s", id)
	return claimsintake.Sample{}
}

// ingest loads a sample and returns its job and the claims handed over.
func ingest(t *testing.T, p *claimsintake.Processor, r *received, id string) (*ingestionv1.IngestionJob, []*claimsv1.Claim) {
	t.Helper()
	before := len(r.claims)
	x := sample(t, id)
	job, err := p.Ingest(context.Background(), x.Filename, []byte(x.Text))
	if err != nil {
		t.Fatal(err)
	}
	return job, r.claims[before:]
}

func TestMapsClaimsVerbatim(t *testing.T) {
	p, r := setup(t)
	job, got := ingest(t, p, r, "clean")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED || len(got) != 3 {
		t.Fatalf("job %s, %d claims", job.GetStatus(), len(got))
	}
	c := got[0]
	checks := map[string][2]any{
		"type":                {c.GetType(), claimsv1.ClaimType_CLAIM_TYPE_PROFESSIONAL},
		"frequency":           {c.GetFrequency(), claimsv1.ClaimFrequency_CLAIM_FREQUENCY_ORIGINAL},
		"source":              {c.GetSource(), claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837},
		"pcn":                 {c.GetPatientControlNumber(), "RIV-88213"},
		"member id":           {c.GetMemberCardId(), "ACM100101"},
		"billing npi":         {c.GetBillingProvider().GetNpi(), "1234567893"},
		"billing tin":         {c.GetBillingProvider().GetTin(), "311234567"},
		"rendering taxonomy":  {c.GetRenderingProvider().GetTaxonomyCode(), "207Q00000X"},
		"diagnosis":           {strings.Join(c.GetDiagnosisCodes(), ","), "J069"},
		"total charge":        {c.GetTotalCharge().GetCents(), int64(18000)},
		"clearinghouse":       {c.GetClearinghouseId(), "CLEARPATH"},
		"clearinghouse time":  {c.GetClearinghouseReceivedTime().AsTime().Format(time.RFC3339), "2026-03-15T22:10:00Z"},
		"trace":               {c.GetClearinghouseTraceNumber(), "CP101RIV-88213"},
		"source job":          {c.GetSourceRecord().GetIngestionJobId(), job.GetId()},
		"source set":          {c.GetSourceRecord().GetTransactionSetControlNumber(), "0001"},
		"subscriber as sent":  {c.GetSubmittedSubscriber().GetName().GetFirst(), "JORDAN"},
		"no separate patient": {c.GetSubmittedPatient() == nil, true},
		"subscriber ssn":      {c.GetSubmittedSubscriber().GetSsn(), "999101001"},
		"line procedure":      {c.GetLines()[0].GetProcedureCode(), "99213"},
		"line pos":            {c.GetLines()[0].GetPlaceOfServiceCode(), "11"},
		"line units":          {c.GetLines()[0].GetUnitsMilli(), int64(1000)},
		"line charge":         {c.GetLines()[0].GetCharge().GetCents(), int64(18000)},
		"line pointer":        {c.GetLines()[0].GetDiagnosisPointers()[0], int32(1)},
		"line dos":            {c.GetLines()[0].GetServiceDates().GetStart().GetDay(), int32(12)},
		"third claim lines":   {len(got[2].GetLines()), 2},
		"third claim from":    {got[2].GetServiceDates().GetStart().GetDay(), int32(12)},
		"third claim through": {got[2].GetServiceDates().GetEnd().GetDay(), int32(13)},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %v, want %v", name, c[0], c[1])
		}
	}
	// Intake matches nothing: those fields are adjudication's. It does
	// number each claim loop, by its position in the file.
	for i, c := range got {
		if c.GetMemberId() != "" || c.GetCustomerId() != "" || c.GetOriginalClaimId() != "" {
			t.Errorf("%s: intake set matched fields: member %q customer %q original %q", c.GetPatientControlNumber(),
				c.GetMemberId(), c.GetCustomerId(), c.GetOriginalClaimId())
		}
		if want := fmt.Sprintf("26075-E-%05d", i+1); c.GetClaimNumber() != want || c.GetClaimVersion() != 1 {
			t.Errorf("%s: number %s-%d, want %s", c.GetPatientControlNumber(), c.GetClaimNumber(), c.GetClaimVersion(), want)
		}
	}
	if b := job.GetClaimNumbers(); b.GetPrefix() != "26075-E-" || b.GetFirst() != 1 || b.GetLast() != 3 {
		t.Errorf("block %v", b)
	}
}

func TestMapsPatientLoop(t *testing.T) {
	p, r := setup(t)
	_, got := ingest(t, p, r, "dependent")
	c := got[0]
	pt := c.GetSubmittedPatient()
	// The member id stays the subscriber's, as sent; the patient is only
	// described.
	if c.GetMemberCardId() != "ACM100101" || c.GetSubmittedSubscriber().GetMemberNumber() != "ACM100101" ||
		c.GetSubmittedSubscriber().GetName().GetFirst() != "JORDAN" || pt.GetMemberNumber() != "" ||
		pt.GetName().GetFirst() != "RILEY" || pt.GetBirthDate().GetYear() != 2015 || pt.GetBirthDate().GetDay() != 21 {
		t.Errorf("card %q, patient %v", c.GetMemberCardId(), pt)
	}
}

func TestMapsCorrectionsAndVoids(t *testing.T) {
	p, r := setup(t)
	for _, tc := range []struct {
		sample string
		freq   claimsv1.ClaimFrequency
		quoted string
		auth   string
	}{
		{"corrected", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26063-E-00001", ""},
		{"corrected-no-match", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26040-E-00417", ""},
		{"corrected-pended", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26068-E-00001", "PA-1002"},
		{"void", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID, "26064-E-00001", ""},
	} {
		_, got := ingest(t, p, r, tc.sample)
		c := got[0]
		// The quoted number is kept as sent; which claim it means is for
		// adjudication to work out.
		if c.GetFrequency() != tc.freq || c.GetPayerClaimControlNumber() != tc.quoted ||
			c.GetOriginalClaimId() != "" || c.GetOriginalLink() != nil || strings.Join(c.GetAuthorizationNumbers(), ",") != tc.auth {
			t.Errorf("%s: freq %s quoted %q original %q link %v auth %v", tc.sample, c.GetFrequency(),
				c.GetPayerClaimControlNumber(), c.GetOriginalClaimId(), c.GetOriginalLink(), c.GetAuthorizationNumbers())
		}
	}
}

func TestRejectedSetLoadsNothing(t *testing.T) {
	p, r := setup(t)
	job, got := ingest(t, p, r, "bad-set")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED_WITH_ERRORS || len(got) != 1 ||
		got[0].GetPatientControlNumber() != "PAT-40790" {
		t.Fatalf("job %s, handed over %d", job.GetStatus(), len(got))
	}
	// Both claim loops got numbers; the rejected set's is the gap.
	if b := job.GetClaimNumbers(); b.GetFirst() != 1 || b.GetLast() != 2 || got[0].GetClaimNumber() != "26075-E-00001" {
		t.Errorf("block %v, loaded claim %s", b, got[0].GetClaimNumber())
	}
	sets := job.GetTransactionSets()
	if !sets[0].GetAccepted() || sets[1].GetAccepted() || sets[1].GetErrors()[0].GetCode() != "IK4-1" {
		t.Errorf("sets %v", sets)
	}
	ack := string(p.Files[job.GetAcknowledgmentUri()])
	for _, want := range []string{"AK1*HC*108*005010X222A1", "AK2*837*0001", "IK5*A", "AK2*837*0002", "IK3*CLM*17**8", "IK4*2**1", "IK5*R*5", "AK9*P*2*2*1"} {
		if !strings.Contains(ack, want) {
			t.Errorf("999 lacks %s:\n%s", want, ack)
		}
	}
	ic, err := x12.Parse([]byte(ack))
	if err != nil || len(ic.Groups[0].Sets[0].Problems) > 0 {
		t.Errorf("999 envelope: %v %v", err, ic.Groups[0].Sets[0].Problems)
	}

	job, got = ingest(t, p, r, "bad-envelope")
	if len(got) != 0 || job.GetTransactionSets()[0].GetAccepted() || !strings.Contains(string(p.Files[job.GetAcknowledgmentUri()]), "IK5*R*4") {
		t.Errorf("bad envelope: handed over %d, sets %v", len(got), job.GetTransactionSets())
	}
}

func TestDuplicateFileLoadsNothing(t *testing.T) {
	p, r := setup(t)
	ingest(t, p, r, "clean")
	job, got := ingest(t, p, r, "clean")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED || len(got) != 0 ||
		job.GetErrors()[0].GetCode() != "DUPLICATE_INTERCHANGE" {
		t.Errorf("resend: %s, handed over %d, %v", job.GetStatus(), len(got), job.GetErrors())
	}
}

// Preview maps without side effects, and shows the same claims a load
// would hand over.
func TestPreviewLoadsNothing(t *testing.T) {
	p, r := setup(t)
	x := sample(t, "clean")
	v, err := p.Preview(context.Background(), []byte(x.Text))
	if err != nil {
		t.Fatal(err)
	}
	if jobs, _ := p.Jobs(context.Background()); len(jobs) != 0 || len(r.claims) != 0 {
		t.Fatalf("preview left %d jobs, %d claims", len(jobs), len(r.claims))
	}
	var first map[string]any
	if err := json.Unmarshal(v.Sets[0].Claims[0].Mapped, &first); err != nil {
		t.Fatal(err)
	}
	if n := len(v.Sets[0].Claims); n != 3 || first["patient_control_number"] != "RIV-88213" {
		t.Errorf("preview: %d claims, %v", n, first)
	}
}

// mapAll maps every transaction set of a file, without the envelope checks.
func mapAll(t *testing.T, text string) []*claimsv1.Claim {
	t.Helper()
	ic, err := x12.Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	var out []*claimsv1.Claim
	for _, g := range ic.Groups {
		for _, set := range g.Sets {
			parsed, errs := claimsintake.Map837P(ic.Delims, set)
			if len(errs) > 0 {
				continue // a sample that is meant to be rejected
			}
			for _, pc := range parsed {
				out = append(out, pc.Claim)
			}
		}
	}
	return out
}

// Every segment and element the samples send lands in a field: nothing is
// left in unmapped_segments.
func TestSamplesLeaveNothingUnmapped(t *testing.T) {
	for _, x := range claimsintake.Samples() {
		for _, c := range mapAll(t, x.Text) {
			if u := c.GetUnmappedSegments(); len(u) > 0 {
				t.Errorf("%s, claim %s: unmapped %q", x.ID, c.GetPatientControlNumber(), u)
			}
		}
	}
}

func TestMapsTheWholeClaim(t *testing.T) {
	got := mapAll(t, sample(t, "rich").Text)
	if len(got) != 1 {
		t.Fatalf("%d claims", len(got))
	}
	c := got[0]
	l1, l2 := c.GetLines()[0], c.GetLines()[1]
	other := c.GetOtherInsurance()[0]
	adj := l1.GetOtherPayerAdjudications()[0]
	checks := map[string][2]any{
		"transaction type":      {c.GetSubmittedTransaction().GetType(), claimsv1.TransactionType_TRANSACTION_TYPE_CLAIM},
		"batch reference":       {c.GetSubmittedTransaction().GetReferenceId(), "B1100001"},
		"submitter":             {c.GetSubmittedTransaction().GetSubmitterName(), "CLEARPATH EDI"},
		"submitter phone":       {c.GetSubmittedTransaction().GetSubmitterContacts()[0].GetNumbers()[0].GetValue(), "5555550100"},
		"receiver":              {c.GetSubmittedTransaction().GetReceiverId(), "COREADMIN"},
		"billing taxonomy":      {c.GetBillingProvider().GetTaxonomyCode(), "207Q00000X"},
		"billing entity":        {c.GetBillingProvider().GetEntityTypeCode(), "1"},
		"billing last name":     {c.GetBillingProvider().GetPersonName().GetLast(), "CHEN"},
		"billing street":        {strings.Join(c.GetBillingProvider().GetAddress().GetLines(), "|"), "100 MAIN ST|SUITE 200"},
		"billing zip":           {c.GetBillingProvider().GetAddress().GetPostalCode(), "627010000"},
		"billing tax id kind":   {c.GetBillingProvider().GetTaxIdQualifier(), "EI"},
		"billing ids":           {fmt.Sprint(len(c.GetBillingProvider().GetSecondaryIds())), "2"}, // state license, and its id with the payer
		"billing email":         {c.GetBillingProvider().GetContacts()[0].GetNumbers()[1].GetValue(), "BILLING@CHENFM.EXAMPLE"},
		"pay-to address":        {c.GetPayToAddress().GetLines()[0], "PO BOX 4410"},
		"payer sequence":        {c.GetSubmittedCoverage().GetPayerResponsibility(), claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_SECONDARY},
		"group number":          {c.GetSubmittedCoverage().GetGroupNumber(), "GRP0042"},
		"filing indicator":      {c.GetSubmittedCoverage().GetClaimFilingIndicatorCode(), "CI"},
		"payer":                 {c.GetSubmittedPayer().GetName() + "/" + c.GetSubmittedPayer().GetId(), "ACME HEALTH PLAN/ACMEHP"},
		"payer city":            {c.GetSubmittedPayer().GetAddress().GetCity(), "CHICAGO"},
		"subscriber middle":     {c.GetSubmittedSubscriber().GetName().GetMiddle(), "A"},
		"subscriber street":     {c.GetSubmittedSubscriber().GetAddress().GetLines()[0], "42 ELM ST"},
		"member id qualifier":   {c.GetSubmittedSubscriber().GetMemberNumberQualifier(), "MI"},
		"patient relationship":  {c.GetSubmittedPatient().GetRelationship(), claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_CHILD},
		"patient city":          {c.GetSubmittedPatient().GetAddress().GetCity(), "SPRINGFIELD"},
		"place of service":      {c.GetPlaceOfServiceCode(), "11"},
		"accept assignment":     {c.GetIndicators().GetAcceptAssignment(), claimsv1.AcceptAssignment_ACCEPT_ASSIGNMENT_ASSIGNED},
		"benefits assignment":   {c.GetIndicators().GetBenefitsAssignment(), claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_YES},
		"release of info":       {c.GetIndicators().GetReleaseOfInformationCode(), "Y"},
		"related cause":         {c.GetRelatedCauses().GetCauses()[0], claimsv1.RelatedCause_RELATED_CAUSE_OTHER_ACCIDENT},
		"accident date":         {c.GetDates().GetAccident().GetDay(), int32(10)},
		"onset date":            {c.GetDates().GetOnset().GetDay(), int32(10)},
		"last seen date":        {c.GetDates().GetLastSeen().GetDay(), int32(12)},
		"attachment":            {c.GetAttachments()[0].GetReportTypeCode() + "/" + c.GetAttachments()[0].GetControlNumber(), "OZ/ATT88301"},
		"patient paid":          {c.GetPatientPaid().GetCents(), int64(3000)},
		"referral number":       {c.GetReferences().GetReferralNumber(), "REF-2207"},
		"referral as auth":      {strings.Join(c.GetAuthorizationNumbers(), ","), "REF-2207"},
		"medical record number": {c.GetReferences().GetMedicalRecordNumber(), "MRN-77120"},
		"note":                  {c.GetNotes()[0].GetText(), "FELL FROM PLAYGROUND EQUIPMENT AT SCHOOL"},
		"diagnoses":             {strings.Join(c.GetDiagnosisCodes(), ","), "S52501A,W091XXA"},
		"diagnosis qualifiers":  {strings.Join(c.GetDiagnosisCodeQualifiers(), ","), "ABK,ABF"},
		"referring provider":    {c.GetReferringProvider().GetNpi(), "1122334455"},
		"service facility":      {c.GetServiceFacility().GetName() + "/" + c.GetServiceFacility().GetAddress().GetLines()[0], "SPRINGFIELD URGENT CARE/900 LAKE AVE"},
		"other payer sequence":  {other.GetPayerResponsibility(), claimsv1.PayerResponsibility_PAYER_RESPONSIBILITY_PRIMARY},
		"other relationship":    {other.GetRelationship(), claimsv1.PatientRelationship_PATIENT_RELATIONSHIP_CHILD},
		"other group":           {other.GetGroupNumber(), "GRP7781"},
		"other payer paid":      {other.GetPayerPaid().GetCents(), int64(18000)},
		"other remaining":       {other.GetRemainingPatientLiability().GetCents(), int64(7000)},
		"other benefits":        {other.GetBenefitsAssignment(), claimsv1.BenefitsAssignment_BENEFITS_ASSIGNMENT_YES},
		"other subscriber":      {other.GetSubscriberName().GetFirst() + "/" + other.GetSubscriberId(), "SAM/BLU5550099"},
		"other payer":           {other.GetPayer().GetName() + "/" + other.GetPayer().GetId(), "BLUE RIVER HEALTH/BRH01"},
		"other adjudicated":     {other.GetAdjudicationDate().GetDay(), int32(15)},
		"other claim number":    {other.GetClaimControlNumber(), "BRH-2026-118840"},
		"line modifier":         {strings.Join(l1.GetModifiers(), ","), "25"},
		"line pointers":         {fmt.Sprint(l1.GetDiagnosisPointers()), "[1 2]"},
		"line unit basis":       {l1.GetUnitBasisCode(), "UN"},
		"line control number":   {l1.GetReferences().GetLineItemControlNumber(), "L1-88301"},
		"line note":             {l1.GetNotes()[0].GetText(), "SEEN FOR FOREARM INJURY AFTER FALL"},
		"line other payer":      {adj.GetPayerId() + "/" + adj.GetProcedureCode(), "BRH01/99214"},
		"line other paid":       {adj.GetPaid().GetCents(), int64(12000)},
		"line other adjustment": {adj.GetAdjustments()[0].GetGroupCode() + "-" + adj.GetAdjustments()[0].GetReasonCode() + " " + fmt.Sprint(adj.GetAdjustments()[0].GetAmount().GetCents()), "CO-45 8000"},
		"line other remaining":  {adj.GetRemainingPatientLiability().GetCents(), int64(5000)},
		"drug ndc":              {l2.GetNdc(), "00409379501"},
		"drug quantity":         {l2.GetDrug().GetQuantity() + l2.GetDrug().GetUnitCode(), "2ML"},
		"prescription":          {l2.GetDrug().GetPrescriptionNumber(), "RX4471"},
		"ordering provider":     {l2.GetOrderingProvider().GetNpi(), "1234567893"},
		"line 2 units":          {l2.GetUnitsMilli(), int64(2000)},
		"line 2 other paid":     {l2.GetOtherPayerAdjudications()[0].GetPaid().GetCents(), int64(6000)},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: got %v, want %v", name, c[0], c[1])
		}
	}
}

// What the mapper has no field for is kept on the claim, verbatim: a
// segment it does not know, a qualifier it does not know, and a known
// segment carrying an element it does not read. Segments sent above the
// claim are kept on every claim beneath them.
func TestKeepsWhatItCannotMap(t *testing.T) {
	text := sample(t, "clean").Text
	for old, add := range map[string]string{
		"REF*EI*311234567~\n":        "CUR*85*USD~\n",               // 2010AA: no field for currency
		"HI*ABK:J069~\n":             "REF*ZZ*MYSTERY~\nXYZ*1*2~\n", // 2300: unknown qualifier, unknown segment
		"DTP*472*D8*20260312~\nLX*2": "NTE*ADD*A NOTE*EXTRA~\nLX*2", // 2400: an element past the ones read
	} {
		if !strings.Contains(text, old) {
			t.Fatalf("sample has no %q", old)
		}
		text = strings.Replace(text, old, strings.Replace(old, "LX*2", "", 1)+add, 1)
	}
	got := mapAll(t, text)
	if len(got) != 3 {
		t.Fatalf("%d claims", len(got))
	}
	for i, want := range []string{
		"CUR*85*USD|REF*ZZ*MYSTERY|XYZ*1*2",
		"",
		"NTE*ADD*A NOTE*EXTRA",
	} {
		if u := strings.Join(got[i].GetUnmappedSegments(), "|"); u != want {
			t.Errorf("claim %d: unmapped %q, want %q", i+1, u, want)
		}
	}
	// The part of the note that was read is still mapped.
	if n := got[2].GetLines()[0].GetNotes(); len(n) != 1 || n[0].GetText() != "A NOTE" {
		t.Errorf("note %v", n)
	}
}
