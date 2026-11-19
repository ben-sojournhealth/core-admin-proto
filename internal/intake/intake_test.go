package intake_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/intake"
	"coreadmin/internal/repo"
	"coreadmin/internal/x12"
)

// received records what intake hands over, and nothing else happens: these
// tests are about what intake produces, not what adjudication makes of it.
type received struct{ claims []*claimsv1.Claim }

func (r *received) Receive(_ context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error) {
	r.claims = append(r.claims, proto.Clone(c).(*claimsv1.Claim))
	return c, nil
}

func setup() (*intake.Processor, *received) {
	r := &received{}
	clock := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	return &intake.Processor{
		Repos: repo.NewInMemory(), Claims: r, Files: map[string][]byte{}, ReceiverID: intake.OurID,
		Now: func() time.Time { clock = clock.Add(time.Second); return clock },
	}, r
}

func sample(t *testing.T, id string) intake.Sample {
	t.Helper()
	for _, x := range intake.Samples() {
		if x.ID == id {
			return x
		}
	}
	t.Fatalf("no sample %s", id)
	return intake.Sample{}
}

// ingest loads a sample and returns its job and the claims handed over.
func ingest(t *testing.T, p *intake.Processor, r *received, id string) (*ingestionv1.IngestionJob, []*claimsv1.Claim) {
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
	p, r := setup()
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
		"patient as sent":     {c.GetSubmittedPatient().GetName().GetFirst(), "JORDAN"},
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
	// Intake matches nothing: those fields are adjudication's.
	for _, c := range got {
		if c.GetMemberId() != "" || c.GetCustomerId() != "" || c.GetClaimNumber() != "" || c.GetOriginalClaimId() != "" {
			t.Errorf("%s: intake set matched fields: member %q customer %q number %q original %q", c.GetPatientControlNumber(),
				c.GetMemberId(), c.GetCustomerId(), c.GetClaimNumber(), c.GetOriginalClaimId())
		}
	}
}

func TestMapsPatientLoop(t *testing.T) {
	p, r := setup()
	_, got := ingest(t, p, r, "dependent")
	c := got[0]
	pt := c.GetSubmittedPatient()
	// The member id stays the subscriber's, as sent; the patient is only
	// described.
	if c.GetMemberCardId() != "ACM100101" || pt.GetCardId() != "ACM100101" || !pt.GetDependentLoop() ||
		pt.GetName().GetFirst() != "RILEY" || pt.GetBirthDate().GetYear() != 2015 || pt.GetBirthDate().GetDay() != 21 {
		t.Errorf("card %q, patient %v", c.GetMemberCardId(), pt)
	}
}

func TestMapsCorrectionsAndVoids(t *testing.T) {
	p, r := setup()
	for _, tc := range []struct {
		sample string
		freq   claimsv1.ClaimFrequency
		quoted string
		auth   string
	}{
		{"corrected", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26063-A-00001", ""},
		{"corrected-no-match", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26040-E-00417", ""},
		{"corrected-pended", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_REPLACEMENT, "26068-A-00001", "PA-1002"},
		{"void", claimsv1.ClaimFrequency_CLAIM_FREQUENCY_VOID, "26064-A-00001", ""},
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
	p, r := setup()
	job, got := ingest(t, p, r, "bad-set")
	if job.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED_WITH_ERRORS || len(got) != 1 ||
		got[0].GetPatientControlNumber() != "PAT-40790" {
		t.Fatalf("job %s, handed over %d", job.GetStatus(), len(got))
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
	p, r := setup()
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
	p, r := setup()
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
