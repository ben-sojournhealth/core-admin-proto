package claimsintake

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/ids"
	"coreadmin/internal/repo"
	"coreadmin/internal/store"
	"coreadmin/internal/x12"
	"coreadmin/internal/x12ack"
)

// Receiver takes in a claim exactly as submitted (claims.Service).
// Everything after that (matching, numbering, adjudication) is its
// business, not intake's.
type Receiver interface {
	Receive(ctx context.Context, c *claimsv1.Claim) (*claimsv1.Claim, error)
}

// Processor loads 837 files. Files and the 999s written for them are kept
// in Files, a stand-in for object storage keyed by URI.
type Processor struct {
	Repos  *repo.Repos
	Claims Receiver
	Now    func() time.Time
	Logf   func(format string, args ...any)
	Files  map[string][]byte
	// ReceiverID is our id in the ISA08 of inbound files, and our ISA06 on
	// the 999.
	ReceiverID string
}

func (p *Processor) logf(format string, args ...any) {
	if p.Logf != nil {
		p.Logf(format, args...)
	}
}

// Ingest loads one file and returns its job. A file that cannot be read
// as X12, or that repeats an interchange already loaded, fails as a whole
// and loads nothing.
func (p *Processor) Ingest(ctx context.Context, filename string, raw []byte) (*ingestionv1.IngestionJob, error) {
	now := p.Now()
	job := &ingestionv1.IngestionJob{
		Id:               ids.NewV7(now),
		Kind:             ingestionv1.IngestionKind_INGESTION_KIND_CLAIMS,
		Format:           ingestionv1.IngestionFormat_INGESTION_FORMAT_X12_837,
		OriginalFilename: filename,
		Status:           ingestionv1.IngestionStatus_INGESTION_STATUS_VALIDATING,
		ReceivedTime:     timestamppb.New(now),
		Counts:           &ingestionv1.IngestionCounts{},
	}
	job.SourceUri = fmt.Sprintf("mem://intake/%s/%s", job.GetId(), filename)
	p.Files[job.GetSourceUri()] = raw
	p.logf("Received file %s.", filename)

	fail := func(code, msg string) (*ingestionv1.IngestionJob, error) {
		job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED
		job.Errors = append(job.Errors, &ingestionv1.RecordError{Code: code, Message: msg})
		job.CompletedTime = timestamppb.New(p.Now())
		p.logf("File %s failed: %s. Nothing was loaded.", filename, msg)
		return p.Repos.ClaimIngestionJobs.Create(ctx, job)
	}

	ic, err := x12.Parse(raw)
	if err != nil {
		return fail("UNREADABLE", err.Error())
	}
	job.Envelope = &ingestionv1.X12Envelope{
		SenderId: ic.Sender(), ReceiverId: ic.Receiver(),
		InterchangeControlNumber: ic.ControlNumber(), Usage: ic.ISA.E(15),
	}
	if len(ic.Groups) > 0 {
		job.Envelope.GroupControlNumber = ic.Groups[0].GS.E(6)
		job.Envelope.ImplementationGuide = ic.Groups[0].GS.E(8)
	}
	if prior, err := p.loaded(ctx, ic); err != nil {
		return nil, err
	} else if prior != nil {
		return fail("DUPLICATE_INTERCHANGE", fmt.Sprintf("interchange %s from %s was already loaded from %s on %s",
			ic.ControlNumber(), ic.Sender(), prior.GetOriginalFilename(), prior.GetReceivedTime().AsTime().Format("Jan 2 15:04")))
	}
	if r := ic.Receiver(); p.ReceiverID != "" && r != p.ReceiverID {
		return fail("WRONG_RECEIVER", fmt.Sprintf("the file is addressed to %s, not %s", r, p.ReceiverID))
	}

	// Check every set first, so the 999 covers the whole file, then load
	// the accepted ones.
	all := MapFile(ic, job.GetId())

	// Reserve a claim number for every claim loop in the file, in file
	// order, rejected sets included: a number with no claim then means
	// that loop didn't load, and the job says why.
	if err := p.number(ctx, job, all); err != nil {
		return nil, err
	}
	job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_LOADING
	if job, err = p.Repos.ClaimIngestionJobs.Create(ctx, job); err != nil {
		return nil, err
	}

	ack := &x12ack.Builder{IC: ic, Sender: p.ReceiverID, Now: p.Now()}
	for _, ch := range all {
		res := &ingestionv1.TransactionSetResult{
			ControlNumber: ch.Set.ST.E(2), Accepted: len(ch.Errs) == 0, ClaimCount: int32(len(ch.Claims)),
		}
		job.TransactionSets = append(job.TransactionSets, res)
		ack.Add(ch.Set, ch.Errs)
		job.Counts.RecordsTotal += int64(len(ch.Claims))
		for _, e := range ch.Errs {
			res.Errors = append(res.Errors, &ingestionv1.RecordError{
				RecordIndex: int64(e.Segment.Pos), Code: e.Code(), Message: e.Message,
			})
		}
		if !res.Accepted {
			job.Counts.RecordsErrored += int64(len(ch.Claims))
			p.logf("Transaction set %s rejected on the 999: %s. Its %d claim(s) were not loaded.",
				ch.Set.ST.E(2), ch.Errs[0].Message, len(ch.Claims))
			continue
		}
		for _, pc := range ch.Claims {
			c := pc.Claim
			if _, err := p.Claims.Receive(ctx, c); err != nil {
				job.Counts.RecordsErrored++
				job.Errors = append(job.Errors, &ingestionv1.RecordError{
					RecordIndex: int64(pc.First), Code: "NOT_LOADED", Message: err.Error(), RecordKey: c.GetPatientControlNumber(),
				})
				p.logf("Claim %s (segment %d) was not loaded: %v.", c.GetPatientControlNumber(), pc.First, err)
				continue
			}
			job.Counts.RecordsCreated++
		}
	}

	job.AcknowledgmentUri = job.GetSourceUri() + ".999"
	p.Files[job.GetAcknowledgmentUri()] = []byte(ack.String())
	job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED
	if job.GetCounts().GetRecordsErrored() > 0 {
		job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED_WITH_ERRORS
	}
	job.CompletedTime = timestamppb.New(p.Now())
	p.logf("File %s loaded: %d claim(s) taken in, %d not loaded. 999 returned to %s.",
		filename, job.GetCounts().GetRecordsCreated(), job.GetCounts().GetRecordsErrored(), ic.Sender())
	return p.Repos.ClaimIngestionJobs.Update(ctx, job)
}

// number reserves the file's block of claim numbers and gives each claim
// loop its number by position.
func (p *Processor) number(ctx context.Context, job *ingestionv1.IngestionJob, all []Checked) error {
	count := 0
	for _, ch := range all {
		count += len(ch.Claims)
	}
	if count == 0 {
		return nil
	}
	prefix := claims.NumberPrefix(job.GetReceivedTime().AsTime(), claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837)
	first, err := p.Repos.ClaimNumbers.Reserve(ctx, prefix, count, job.GetOriginalFilename()+" ("+job.GetId()+")")
	if err != nil {
		return err
	}
	job.ClaimNumbers = &ingestionv1.ClaimNumberBlock{Prefix: prefix, First: int32(first), Last: int32(first + count - 1)}
	i := 0
	for _, ch := range all {
		for _, pc := range ch.Claims {
			pc.Claim.ClaimNumber, pc.Claim.ClaimVersion = claims.FormatNumber(prefix, first+i), 1
			i++
		}
	}
	p.logf("Reserved claim numbers %s to %s for %s.", claims.FormatNumber(prefix, first), claims.FormatNumber(prefix, first+count-1), job.GetOriginalFilename())
	return nil
}

// Checked is one transaction set: the claims mapped from it and the errors
// the 999 reports for it. Any error rejects the whole set.
type Checked struct {
	Set    *x12.Set
	Claims []ParsedClaim
	Errs   []x12ack.Error
}

// MapFile checks and maps every transaction set in an interchange. Each
// claim is the 837 as submitted, plus where it came from: the
// clearinghouse that sent it and its place in the file (jobID is empty for
// a preview). Mapping is deterministic, so mapping a stored file again
// gives the same claims.
func MapFile(ic *x12.Interchange, jobID string) []Checked {
	var all []Checked
	for _, g := range ic.Groups {
		for _, set := range g.Sets {
			ch := Checked{Set: set}
			if set.ST.E(1) != "837" || g.GS.E(8) != Guide837P {
				ch.Errs = append(ch.Errs, x12ack.Error{Segment: set.ST, SegmentCode: "8", Element: 3, ElementCode: "7",
					Message: fmt.Sprintf("transaction %s %s is not an 837P (%s)", set.ST.E(1), g.GS.E(8), Guide837P)})
			} else {
				ch.Claims, ch.Errs = Map837P(ic.Delims, set)
			}
			for _, prob := range set.Problems {
				ch.Errs = append(ch.Errs, x12ack.Error{Segment: set.SE, Message: prob})
			}
			for _, pc := range ch.Claims {
				pc.Claim.ClearinghouseId = ic.Sender()
				pc.Claim.SourceRecord = &claimsv1.SourceRecord{
					IngestionJobId: jobID, InterchangeControlNumber: ic.ControlNumber(),
					TransactionSetControlNumber: set.ST.E(2), FirstSegment: int32(pc.First), LastSegment: int32(pc.Last),
				}
			}
			all = append(all, ch)
		}
	}
	return all
}

// loaded returns the job that already loaded this interchange, if any.
func (p *Processor) loaded(ctx context.Context, ic *x12.Interchange) (*ingestionv1.IngestionJob, error) {
	jobs, _, err := p.Repos.ClaimIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1, Where: []store.Cond{
		store.Eq(ic.ControlNumber(), "envelope", "interchangeControlNumber"), store.Eq(ic.Sender(), "envelope", "senderId"),
	}, Filter: func(j *ingestionv1.IngestionJob) bool {
		return j.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED &&
			j.GetEnvelope().GetSenderId() == ic.Sender() &&
			j.GetEnvelope().GetInterchangeControlNumber() == ic.ControlNumber()
	}})
	if err != nil || len(jobs) == 0 {
		return nil, err
	}
	return jobs[0], nil
}
