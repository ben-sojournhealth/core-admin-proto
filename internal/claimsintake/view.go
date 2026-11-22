package claimsintake

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	"coreadmin/internal/claims"
	"coreadmin/internal/store"
	"coreadmin/internal/x12"
)

// JobSummary is a row in the list of loaded files.
type JobSummary struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Received string `json:"received"`
	Status   string `json:"status"`
	ICN      string `json:"icn"`
	Loaded   int64  `json:"loaded"`
	Rejected int64  `json:"rejected"`
}

// JobView is one file in full: its sets, each claim as mapped, the raw
// segments and the 999.
type JobView struct {
	JobSummary
	// The claim numbers reserved for the file: "26075-E-00010 – 00012".
	Numbers  string    `json:"numbers,omitempty"`
	Error    string    `json:"error,omitempty"`
	Sender   string    `json:"sender"`
	Receiver string    `json:"receiver"`
	Usage    string    `json:"usage"`
	Sets     []SetView `json:"sets"`
	// Claims that did not load, by reason: REJECTED (set failed the 999)
	// or NOT_LOADED.
	Outcomes map[string]int `json:"outcomes"`
	Ack      string         `json:"ack"`
	Segments []SegView      `json:"segments"`
}

type SetView struct {
	Control  string      `json:"control"`
	Accepted bool        `json:"accepted"`
	Errors   []ErrorView `json:"errors"`
	Claims   []ClaimRow  `json:"claims"`
}

type ErrorView struct {
	Segment int64  `json:"segment"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ClaimRow is one CLM loop and, if it loaded, the claim it became.
type ClaimRow struct {
	Key       string `json:"key"`
	PCN       string `json:"pcn"`
	Patient   string `json:"patient"`
	Card      string `json:"card"`
	Dependent bool   `json:"dependent"`
	Provider  string `json:"provider"`
	Charge    int64  `json:"charge"`
	Frequency string `json:"frequency"`
	Quoted    string `json:"quoted,omitempty"`
	First     int    `json:"first"`
	Last      int    `json:"last"`

	// Loaded claims carry their id so the page can link to Journey for
	// what adjudication made of them; intake shows none of that itself.
	Loaded bool   `json:"loaded"`
	ID     string `json:"id,omitempty"`
	// The Claim as ingestion produced it (protojson, proto field names),
	// before adjudication matched or decided anything.
	Mapped json.RawMessage `json:"mapped"`
	// The number reserved for this claim loop; empty in a preview.
	Number string `json:"number,omitempty"`
}

type SegView struct {
	Pos  int    `json:"pos"`
	Text string `json:"text"`
	// Key of the claim the segment belongs to.
	Claim string `json:"claim,omitempty"`
	Error bool   `json:"error,omitempty"`
}

func summary(j *ingestionv1.IngestionJob) JobSummary {
	return JobSummary{
		ID: j.GetId(), Filename: j.GetOriginalFilename(),
		Received: j.GetReceivedTime().AsTime().Format(time.RFC3339),
		Status:   strings.TrimPrefix(j.GetStatus().String(), "INGESTION_STATUS_"),
		ICN:      j.GetEnvelope().GetInterchangeControlNumber(),
		Loaded:   j.GetCounts().GetRecordsCreated(), Rejected: j.GetCounts().GetRecordsErrored(),
	}
}

// Jobs lists loaded files, newest first.
func (p *Processor) Jobs(ctx context.Context) ([]JobSummary, error) {
	jobs, _, err := p.Repos.ClaimIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].GetId() > jobs[j].GetId() })
	out := []JobSummary{}
	for _, j := range jobs {
		out = append(out, summary(j))
	}
	return out, nil
}

// Loaded reports which interchange control numbers have been loaded.
func (p *Processor) Loaded(ctx context.Context) (map[string]bool, error) {
	jobs, _, err := p.Repos.ClaimIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1000})
	out := map[string]bool{}
	for _, j := range jobs {
		if j.GetStatus() != ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED {
			out[j.GetEnvelope().GetInterchangeControlNumber()] = true
		}
	}
	return out, err
}

// View builds the full view of a job.
func (p *Processor) View(ctx context.Context, id string) (*JobView, error) {
	j, err := p.Repos.ClaimIngestionJobs.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	v := &JobView{
		JobSummary: summary(j), Sender: j.GetEnvelope().GetSenderId(), Receiver: j.GetEnvelope().GetReceiverId(),
		Usage: j.GetEnvelope().GetUsage(), Sets: []SetView{}, Segments: []SegView{}, Outcomes: map[string]int{},
		Ack: string(p.Files[j.GetAcknowledgmentUri()]),
	}
	if len(j.GetErrors()) > 0 {
		v.Error = j.GetErrors()[0].GetMessage()
	}
	loaded, _, err := p.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 10_000, Where: []store.Cond{store.Eq(id, "sourceRecord", "ingestionJobId")}})
	if err != nil {
		return nil, err
	}
	byKey := map[string]*claimsv1.Claim{}
	for _, c := range loaded {
		byKey[fmt.Sprintf("%s:%d", c.GetSourceRecord().GetTransactionSetControlNumber(), c.GetSourceRecord().GetFirstSegment())] = c
	}
	raw := p.Files[j.GetSourceUri()]
	ic, err := x12.Parse(raw)
	if err != nil || j.GetStatus() == ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED {
		rawLines(v, raw)
		return v, nil
	}
	if b := j.GetClaimNumbers(); b != nil {
		v.Numbers = claims.FormatNumber(b.GetPrefix(), int(b.GetFirst()))
		if b.GetLast() > b.GetFirst() {
			v.Numbers += fmt.Sprintf(" – %05d", b.GetLast())
		}
	}
	p.describe(ctx, v, ic, j.GetId(), byKey, j.GetClaimNumbers())
	return v, nil
}

// Preview maps a file without loading anything: no job, no claims, no
// 999 sent. It shows the claims ingestion would hand to adjudication.
func (p *Processor) Preview(ctx context.Context, raw []byte) (*JobView, error) {
	v := &JobView{JobSummary: JobSummary{Status: "PREVIEW"}, Sets: []SetView{}, Segments: []SegView{}, Outcomes: map[string]int{}}
	ic, err := x12.Parse(raw)
	if err != nil {
		v.Error = err.Error()
		rawLines(v, raw)
		return v, nil
	}
	v.ICN, v.Sender, v.Receiver, v.Usage = ic.ControlNumber(), ic.Sender(), ic.Receiver(), ic.ISA.E(15)
	if prior, err := p.loaded(ctx, ic); err != nil {
		return nil, err
	} else if prior != nil {
		v.Error = fmt.Sprintf("interchange %s from %s was already loaded from %s; sending it would be refused as a duplicate",
			ic.ControlNumber(), ic.Sender(), prior.GetOriginalFilename())
	}
	p.describe(ctx, v, ic, "", nil, nil)
	return v, nil
}

func rawLines(v *JobView, raw []byte) {
	for i, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			v.Segments = append(v.Segments, SegView{Pos: i + 1, Text: l})
		}
	}
}

// describe fills in the sets, claims and segments of a file. byKey holds
// the claims loaded from it (nil for a preview).
func (p *Processor) describe(ctx context.Context, v *JobView, ic *x12.Interchange, jobID string, byKey map[string]*claimsv1.Claim, block *ingestionv1.ClaimNumberBlock) {
	position := 0
	owner := map[int]string{}
	bad := map[int]bool{}
	for _, ch := range MapFile(ic, jobID) {
		ctl := ch.Set.ST.E(2)
		sv := SetView{Control: ctl, Accepted: len(ch.Errs) == 0, Errors: []ErrorView{}, Claims: []ClaimRow{}}
		for _, e := range ch.Errs {
			sv.Errors = append(sv.Errors, ErrorView{Segment: int64(e.Segment.Pos), Code: e.Code(), Message: e.Message})
			bad[e.Segment.Pos] = true
		}
		for _, pc := range ch.Claims {
			key := fmt.Sprintf("%s:%d", ctl, pc.First)
			for pos := pc.First; pos <= pc.Last; pos++ {
				owner[pos] = key
			}
			row := row(key, pc, byKey[key])
			if block != nil {
				row.Number = claims.FormatNumber(block.GetPrefix(), int(block.GetFirst())+position)
			}
			position++
			switch {
			case byKey == nil || row.Loaded:
			case sv.Accepted:
				v.Outcomes["NOT_LOADED"]++
			default:
				v.Outcomes["REJECTED"]++
			}
			sv.Claims = append(sv.Claims, row)
		}
		v.Sets = append(v.Sets, sv)
	}
	for _, s := range ic.Segments {
		v.Segments = append(v.Segments, SegView{Pos: s.Pos, Text: s.String(ic.Delims) + string(ic.Delims.Segment), Claim: owner[s.Pos], Error: bad[s.Pos]})
	}
}

// mapped renders a claim as ingestion produced it, with proto field names.
var mapped = protojson.MarshalOptions{UseProtoNames: true}

func row(key string, pc ParsedClaim, c *claimsv1.Claim) ClaimRow {
	in := pc.Claim
	pt := in.GetSubmittedPatient()
	row := ClaimRow{
		Key: key, PCN: in.GetPatientControlNumber(), Card: pt.GetCardId(), Dependent: pt.GetDependentLoop(),
		Patient:   title(pt.GetName().GetFirst() + " " + pt.GetName().GetLast()),
		Provider:  title(in.GetBillingProvider().GetName()),
		Charge:    in.GetTotalCharge().GetCents(),
		Frequency: map[claimsv1.ClaimFrequency]string{1: "1", 7: "7", 8: "8"}[in.GetFrequency()],
		Quoted:    in.GetPayerClaimControlNumber(), First: pc.First, Last: pc.Last,
	}
	row.Mapped, _ = mapped.Marshal(in)
	if c != nil {
		row.Loaded, row.ID = true, c.GetId()
	}
	return row
}

// title turns "MEI CHEN" into "Mei Chen".
func title(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
