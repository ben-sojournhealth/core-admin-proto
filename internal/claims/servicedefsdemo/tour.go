package servicedefsdemo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/protodoc"
	"coreadmin/internal/common/repo"
)

// Tour is what the "How it works" tab shows. Every example in it is the
// real service's answer on a fresh sandbox holding the starting set, so
// the page can't drift from what the service does.
type Tour struct {
	// The group the examples follow, as it stands.
	Group GroupView `json:"group"`
	// Claim lines, and the groups MatchServiceDefinitions puts each in.
	Lines []LineExample `json:"lines"`
	// One line on two dates of service, either side of a dated code.
	Dates []LineExample `json:"dates"`
	// The group's revisions, newest first (ListServiceDefinitionRevisions).
	Revisions []RevisionView `json:"revisions"`
	// The same revisions as the rows the table holds, oldest first.
	Rows []RowView `json:"rows"`
	// The current row's data column.
	Data string `json:"data"`
	// The service's methods, with the first sentence of each one's comment.
	Endpoints []Endpoint `json:"endpoints"`
}

type GroupView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Intent  string       `json:"intent"`
	Systems []SystemView `json:"systems"`
}

// SystemView is one code system's entries in a group.
type SystemView struct {
	Name  string     `json:"name"`
	Codes []CodeView `json:"codes"`
}

type CodeView struct {
	// "99202–99215", or one code.
	Label string `json:"label"`
	// The dates of service it counts for, "" for every date.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// LineExample is a claim line and where the service put it.
type LineExample struct {
	What           string `json:"what"`
	Procedure      string `json:"procedure,omitempty"`
	PlaceOfService string `json:"placeOfService,omitempty"`
	Revenue        string `json:"revenue,omitempty"`
	DateOfService  string `json:"dateOfService"`
	// The names of the groups it is in.
	Groups []string `json:"groups"`
}

type RevisionView struct {
	Revision int64  `json:"revision"`
	Saved    string `json:"saved"`
	By       string `json:"by"`
	Codes    int    `json:"codes"`
}

// RowView is one row of the service_definitions table.
type RowView struct {
	ID         string `json:"id"`
	Revision   int64  `json:"revision"`
	TxFrom     string `json:"txFrom"`
	TxTo       string `json:"txTo"`
	RecordedBy string `json:"recordedBy"`
}

type Endpoint struct {
	Name string `json:"name"`
	Doc  string `json:"doc"`
}

const tourGroup = "office-visit"

var systemNames = []struct {
	system claimsv1.CodeSystem
	name   string
}{
	{procedure, "Procedure"}, {place, "Place of service"}, {revenue, "Revenue code"},
}

func line(what, proc, pos, rev string, y, m, d int32) (LineExample, *claimsv1.MatchServiceDefinitionsRequest) {
	on := &commonv1.Date{Year: y, Month: m, Day: d}
	return LineExample{What: what, Procedure: proc, PlaceOfService: pos, Revenue: rev, DateOfService: dates.Format(on), Groups: []string{}},
		&claimsv1.MatchServiceDefinitionsRequest{ProcedureCode: proc, PlaceOfServiceCode: pos, RevenueCode: rev, DateOfService: on}
}

// BuildTour plays the examples on a sandbox of its own.
func BuildTour(ctx context.Context) (*Tour, error) {
	repos, drop, err := repo.NewSandbox(ctx)
	if err != nil {
		return nil, err
	}
	defer drop()
	svc, err := NewIn(ctx, repos)
	if err != nil {
		return nil, err
	}
	t := &Tour{}

	got, err := svc.GetServiceDefinition(ctx, &claimsv1.GetServiceDefinitionRequest{Id: tourGroup})
	if err != nil {
		return nil, err
	}
	def := got.GetDefinition()
	t.Group = GroupView{ID: def.GetId(), Name: def.GetName(), Intent: def.GetIntent()}
	for _, s := range systemNames {
		sv := SystemView{Name: s.name, Codes: []CodeView{}}
		for _, e := range def.GetEntries() {
			if e.GetSystem() != s.system {
				continue
			}
			label := e.GetStart()
			if e.GetEnd() != e.GetStart() {
				label += "–" + e.GetEnd()
			}
			sv.Codes = append(sv.Codes, CodeView{Label: label, From: dates.Format(e.GetEffective().GetStart()), To: dates.Format(e.GetEffective().GetEnd())})
		}
		t.Group.Systems = append(t.Group.Systems, sv)
	}

	match := func(ex LineExample, req *claimsv1.MatchServiceDefinitionsRequest) (LineExample, error) {
		res, err := svc.MatchServiceDefinitions(ctx, req)
		if err != nil {
			return ex, err
		}
		for _, m := range res.GetMatches() {
			ex.Groups = append(ex.Groups, m.GetDefinition().GetName())
		}
		return ex, nil
	}
	type example struct {
		what, proc, pos, rev string
		y, m, d              int32
	}
	for _, e := range []example{
		{"An office visit, in an office", "99213", "11", "", 2026, 3, 2},
		{"The same code, in the emergency room", "99213", "23", "", 2026, 3, 2},
		{"An MRI, in the emergency room", "70551", "23", "", 2026, 3, 2},
		{"A room and board line on a hospital bill", "", "", "0120", 2026, 3, 2},
		{"Physical therapy, in an office", "97110", "11", "", 2026, 3, 2},
	} {
		ex, err := match(line(e.what, e.proc, e.pos, e.rev, e.y, e.m, e.d))
		if err != nil {
			return nil, err
		}
		t.Lines = append(t.Lines, ex)
	}
	// Place of service 10 counts for an office visit from 2022.
	for _, e := range []example{
		{"A telehealth visit at home", "99213", "10", "", 2021, 12, 31},
		{"The same visit a day later", "99213", "10", "", 2022, 1, 1},
	} {
		ex, err := match(line(e.what, e.proc, e.pos, e.rev, e.y, e.m, e.d))
		if err != nil {
			return nil, err
		}
		t.Dates = append(t.Dates, ex)
	}

	revs, err := svc.ListServiceDefinitionRevisions(ctx, &claimsv1.ListServiceDefinitionRevisionsRequest{Id: tourGroup})
	if err != nil {
		return nil, err
	}
	for _, r := range revs.GetRevisions() {
		t.Revisions = append(t.Revisions, RevisionView{
			Revision: r.GetAudit().GetRevision(), Saved: r.GetAudit().GetUpdateTime().AsTime().Format(time.DateOnly),
			By: r.GetAudit().GetUpdatedBy(), Codes: len(r.GetEntries()),
		})
	}

	// The rows themselves, as the table holds them.
	versions, err := repos.ServiceDefinitions.Versions(ctx, tourGroup)
	if err != nil {
		return nil, err
	}
	for _, v := range versions {
		row := RowView{ID: v.Record.GetId(), Revision: v.Revision, TxFrom: v.From.UTC().Format(time.DateOnly), RecordedBy: v.RecordedBy}
		if !v.To.IsZero() {
			row.TxTo = v.To.UTC().Format(time.DateOnly)
		}
		t.Rows = append(t.Rows, row)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions of %s", tourGroup)
	}
	data, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(versions[len(versions)-1].Record)
	if err != nil {
		return nil, err
	}
	t.Data = string(data)

	c, err := protodoc.Interface("coreadmin.claims.v1.ServiceDefinitionService", nil)
	if err != nil {
		return nil, err
	}
	for _, m := range c.Methods {
		doc, _, _ := strings.Cut(strings.Join(strings.Fields(m.Doc), " "), ". ")
		t.Endpoints = append(t.Endpoints, Endpoint{Name: m.Name, Doc: strings.TrimSuffix(doc, ".") + "."})
	}
	return t, nil
}
