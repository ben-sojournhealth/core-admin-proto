package enrollmentintake

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
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
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

// JobView is one file in full: its sets, each member loop as mapped, the
// raw segments and the 999.
type JobView struct {
	JobSummary
	Error    string `json:"error,omitempty"`
	Sender   string `json:"sender"`
	Receiver string `json:"receiver"`
	Usage    string `json:"usage"`
	// BGN08 of the first set: change, verify or replace (full file).
	Mode    string    `json:"mode"`
	Sponsor string    `json:"sponsor"`
	Sets    []SetView `json:"sets"`
	// Member loops that did not load, by reason: REJECTED (set failed the
	// 999) or NOT_LOADED.
	Outcomes map[string]int `json:"outcomes"`
	Ack      string         `json:"ack"`
	Segments []SegView      `json:"segments"`
}

type SetView struct {
	Control  string      `json:"control"`
	Accepted bool        `json:"accepted"`
	Errors   []ErrorView `json:"errors"`
	Members  []MemberRow `json:"members"`
}

type ErrorView struct {
	Segment int64  `json:"segment"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MemberRow is one INS loop and whether it loaded.
type MemberRow struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	Subscriber   bool   `json:"subscriber"`
	Relationship string `json:"relationship"`
	SubNumber    string `json:"subNumber"`
	// Maintenance type, read: Add, Change, Term, Reinstate, Audit.
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	// Codes that mapped to UNKNOWN, e.g. "INS03 099".
	Unrecognized []string `json:"unrecognized,omitempty"`
	COBRA        bool     `json:"cobra"`
	// The coverage loops, read: "HLT FAM from 2026-04-01".
	Coverage []string `json:"coverage"`
	First    int      `json:"first"`
	Last     int      `json:"last"`
	Loaded   bool     `json:"loaded"`
	// The EnrollmentTransaction as intake produced it (protojson, proto
	// field names).
	Mapped json.RawMessage `json:"mapped"`
}

type SegView struct {
	Pos  int    `json:"pos"`
	Text string `json:"text"`
	// Key of the member loop the segment belongs to.
	Member string `json:"member,omitempty"`
	Error  bool   `json:"error,omitempty"`
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

func (p *Processor) jobs(ctx context.Context) ([]*ingestionv1.IngestionJob, error) {
	jobs, _, err := p.Repos.IngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1000, Filter: func(j *ingestionv1.IngestionJob) bool {
		return j.GetKind() == ingestionv1.IngestionKind_INGESTION_KIND_ENROLLMENT
	}})
	return jobs, err
}

// Jobs lists loaded files, newest first.
func (p *Processor) Jobs(ctx context.Context) ([]JobSummary, error) {
	jobs, err := p.jobs(ctx)
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
	jobs, err := p.jobs(ctx)
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
	j, err := p.Repos.IngestionJobs.Get(ctx, id)
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
	loaded, _, err := p.Repos.EnrollmentTransactions.List(ctx, store.ListOptions[*eligibilityv1.EnrollmentTransaction]{PageSize: 100_000,
		Where: []store.Cond{store.Eq(id, "source", "ingestionJobId")}})
	if err != nil {
		return nil, err
	}
	byKey := map[string]bool{}
	for _, t := range loaded {
		byKey[fmt.Sprintf("%s:%d", t.GetSource().GetTransactionSetControlNumber(), t.GetSource().GetFirstSegment())] = true
	}
	raw := p.Files[j.GetSourceUri()]
	ic, err := x12.Parse(raw)
	if err != nil || j.GetStatus() == ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED {
		rawLines(v, raw)
		return v, nil
	}
	describeFile(v, ic, j.GetId(), byKey)
	return v, nil
}

// Preview maps a file without loading anything: no job, no transactions,
// no 999 sent. It shows the member loops intake would hand to
// eligibility.
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
	describeFile(v, ic, "", nil)
	return v, nil
}

func rawLines(v *JobView, raw []byte) {
	for i, l := range strings.Split(string(raw), "\n") {
		if l != "" {
			v.Segments = append(v.Segments, SegView{Pos: i + 1, Text: l})
		}
	}
}

// describeFile fills in the sets, member loops and segments of a file.
// loaded holds the keys of the loops that loaded (nil for a preview).
func describeFile(v *JobView, ic *x12.Interchange, jobID string, loaded map[string]bool) {
	owner := map[int]string{}
	bad := map[int]bool{}
	for _, ch := range MapFile(ic, jobID) {
		ctl := ch.Set.ST.E(2)
		sv := SetView{Control: ctl, Accepted: len(ch.Errs) == 0, Errors: []ErrorView{}, Members: []MemberRow{}}
		for _, e := range ch.Errs {
			sv.Errors = append(sv.Errors, ErrorView{Segment: int64(e.Segment.Pos), Code: e.Code(), Message: e.Message})
			bad[e.Segment.Pos] = true
		}
		for _, pm := range ch.Members {
			key := fmt.Sprintf("%s:%d", ctl, pm.First)
			for pos := pm.First; pos <= pm.Last; pos++ {
				owner[pos] = key
			}
			row := memberRow(key, pm)
			row.Loaded = loaded[key]
			switch {
			case loaded == nil || row.Loaded:
			case sv.Accepted:
				v.Outcomes["NOT_LOADED"]++
			default:
				v.Outcomes["REJECTED"]++
			}
			if v.Sponsor == "" {
				h := pm.Txn.GetHeader()
				v.Sponsor = title(h.GetSponsor().GetName())
				v.Mode = map[eligibilityv1.EnrollmentAction]string{
					eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_CHANGE:  "change",
					eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_VERIFY:  "verify",
					eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_REPLACE: "full",
					eligibilityv1.EnrollmentAction_ENROLLMENT_ACTION_UNKNOWN: "unknown",
				}[h.GetAction()]
			}
			sv.Members = append(sv.Members, row)
		}
		v.Sets = append(v.Sets, sv)
	}
	for _, s := range ic.Segments {
		v.Segments = append(v.Segments, SegView{Pos: s.Pos, Text: s.String(ic.Delims) + string(ic.Delims.Segment), Member: owner[s.Pos], Error: bad[s.Pos]})
	}
}

// mapped renders a transaction as intake produced it, with proto field
// names.
var mapped = protojson.MarshalOptions{UseProtoNames: true}

var (
	actions = map[eligibilityv1.MaintenanceType]string{
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_ADDITION:      "Add",
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_CHANGE:        "Change",
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_TERMINATION:   "Term",
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_REINSTATEMENT: "Reinstate",
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_AUDIT:         "Audit",
		eligibilityv1.MaintenanceType_MAINTENANCE_TYPE_UNKNOWN:       "Unknown",
	}
	relationships = map[string]string{"18": "Self", "01": "Spouse", "19": "Child", "53": "Domestic partner"}
	reasons       = map[string]string{"02": "birth", "07": "termination of benefits", "08": "termination of employment", "28": "initial enrollment"}
	dateNames     = map[string]string{"348": "from", "349": "through"}
)

func memberRow(key string, pm ParsedMember) MemberRow {
	t := pm.Txn
	row := MemberRow{
		Key: key, Name: title(t.GetName().GetFirst() + " " + t.GetName().GetLast()), Subscriber: t.GetSubscriber(),
		Relationship: or(relationships[t.GetRelationshipCode()], t.GetRelationshipCode()),
		Action:       actions[t.GetMaintenanceType()],
		Reason:       reasons[t.GetMaintenanceReasonCode()],
		COBRA:        t.GetBenefitStatus() == eligibilityv1.BenefitStatus_BENEFIT_STATUS_COBRA, Coverage: []string{}, First: pm.First, Last: pm.Last,
	}
	for _, u := range t.GetUnrecognizedCodes() {
		row.Unrecognized = append(row.Unrecognized, u.GetElement()+" "+u.GetValue())
	}
	for _, r := range t.GetRefs() {
		if r.GetQualifier() == "0F" {
			row.SubNumber = r.GetValue()
		}
	}
	for _, c := range t.GetCoverages() {
		s := strings.TrimSpace(c.GetInsuranceLineCode() + " " + c.GetCoverageLevelCode())
		for _, d := range c.GetDates() {
			if n := dateNames[d.GetQualifier()]; n != "" {
				s += " " + n + " " + iso(d.GetDate())
			}
		}
		row.Coverage = append(row.Coverage, s)
	}
	row.Mapped, _ = mapped.Marshal(t)
	return row
}

func iso(d *commonv1.Date) string {
	return fmt.Sprintf("%04d-%02d-%02d", d.GetYear(), d.GetMonth(), d.GetDay())
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// title turns "MEI CHEN" into "Mei Chen".
func title(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
