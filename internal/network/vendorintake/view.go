package vendorintake

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/store"
)

// The views the demo page draws.

type StateView struct {
	Sources []SourceView `json:"sources"`
	Samples []SampleView `json:"samples"`
	Updates []UpdateView `json:"updates"`
}

type SourceView struct {
	Source
	// Participations the source states today.
	Participations int64 `json:"participations"`
	// The update awaiting a decision, if any.
	Pending string `json:"pending,omitempty"`
}

type SampleView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Summary  string `json:"summary"`
	SourceID string `json:"sourceId"`
	Sent     bool   `json:"sent"`
}

type UpdateView struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Filename  string `json:"filename"`
	Status    string `json:"status"`
	Received  string `json:"received"`
	DecidedBy string `json:"decidedBy,omitempty"`
	// Empty for a file that failed: Problem says why.
	Problem string       `json:"problem,omitempty"`
	Summary *SummaryView `json:"summary,omitempty"`
	// Rows left out, a sample.
	RowErrors []RowError   `json:"rowErrors,omitempty"`
	Changes   []ChangeView `json:"changes,omitempty"`
}

type SummaryView struct {
	RowsRead     int64        `json:"rowsRead"`
	RowsErrored  int64        `json:"rowsErrored"`
	Before       int64        `json:"before"`
	After        int64        `json:"after"`
	Added        int64        `json:"added"`
	Changed      int64        `json:"changed"`
	Removed      int64        `json:"removed"`
	Unchanged    int64        `json:"unchanged"`
	NewProviders int64        `json:"newProviders"`
	NewTINs      int64        `json:"newTins"`
	Aspects      []CountView  `json:"aspects"`
	States       []StateCount `json:"states"`
}

type CountView struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type StateCount struct {
	State   string `json:"state"`
	Added   int64  `json:"added"`
	Changed int64  `json:"changed"`
	Removed int64  `json:"removed"`
}

type RowError struct {
	Row     int64  `json:"row"`
	Key     string `json:"key"`
	Message string `json:"message"`
}

type ChangeView struct {
	NPI     string   `json:"npi"`
	TIN     string   `json:"tin"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Aspects []string `json:"aspects"`
	// What the pair derives before and after; nil when there is none.
	Before *PartView `json:"before,omitempty"`
	After  *PartView `json:"after,omitempty"`
	Rows   int       `json:"rows"`
}

// PartView is a participation.
type PartView struct {
	NPI         string   `json:"npi,omitempty"`
	Name        string   `json:"name,omitempty"`
	TIN         string   `json:"tin"`
	BillingName string   `json:"billingName"`
	Network     string   `json:"network,omitempty"`
	Periods     []string `json:"periods"`
	Current     bool     `json:"current"`
	Specialties []string `json:"specialties"`
	Locations   []string `json:"locations"`
}

func trim(e fmt.Stringer, prefix string) string { return strings.TrimPrefix(e.String(), prefix) }

func personName(n *commonv1.PersonName) string {
	return strings.Join(strings.Fields(strings.Join([]string{n.GetFirst(), n.GetMiddle(), n.GetLast(), n.GetSuffix()}, " ")), " ")
}

func partView(p *providerv1.NetworkParticipation, today *commonv1.Date) *PartView {
	v := &PartView{NPI: p.GetNpi(), TIN: p.GetTin(), BillingName: p.GetBillingName(), Specialties: p.GetSpecialties(), Periods: []string{}, Locations: []string{}}
	for _, r := range p.GetPeriods() {
		end := dates.Format(r.GetEnd())
		if end == "" {
			end = "open"
		}
		v.Periods = append(v.Periods, dates.Format(r.GetStart())+" → "+end)
		v.Current = v.Current || dates.In(today, r)
	}
	for _, l := range p.GetLocations() {
		ad := l.GetAddress()
		v.Locations = append(v.Locations, strings.Join(append(append([]string{}, ad.GetLines()...), ad.GetCity()+", "+ad.GetState()+" "+ad.GetPostalCode()), ", "))
	}
	return v
}

func (p *Processor) updateView(ctx context.Context, u *providerv1.ProviderUpdate) (UpdateView, error) {
	v := UpdateView{ID: u.GetId(), Source: u.GetSourceId(), Filename: u.GetFilename(),
		Status: trim(u.GetStatus(), "PROVIDER_UPDATE_STATUS_"), DecidedBy: u.GetDecidedBy()}
	job, err := p.Repos.VendorIngestionJobs.Get(ctx, u.GetIngestionJobId())
	if err != nil {
		return v, err
	}
	v.Received = job.GetReceivedTime().AsTime().Format(time.RFC3339)
	for _, e := range job.GetErrors() {
		if e.GetCode() == "ROW_NOT_USABLE" {
			v.RowErrors = append(v.RowErrors, RowError{Row: e.GetRecordIndex(), Key: e.GetRecordKey(), Message: e.GetMessage()})
		}
	}
	s := u.GetSummary()
	v.Summary = &SummaryView{RowsRead: s.GetRowsRead(), RowsErrored: s.GetRowsErrored(), Before: s.GetParticipationsBefore(), After: s.GetParticipationsAfter(),
		Added: s.GetAdded(), Changed: s.GetChanged(), Removed: s.GetRemoved(), Unchanged: s.GetUnchanged(), NewProviders: s.GetNewProviders(), NewTINs: s.GetNewTins(),
		Aspects: []CountView{}, States: []StateCount{}}
	for _, a := range s.GetAspects() {
		v.Summary.Aspects = append(v.Summary.Aspects, CountView{Name: trim(a.GetAspect(), "VENDOR_CHANGE_ASPECT_"), Count: a.GetCount()})
	}
	for _, st := range s.GetStates() {
		v.Summary.States = append(v.Summary.States, StateCount{State: st.GetState(), Added: st.GetAdded(), Changed: st.GetChanged(), Removed: st.GetRemoved()})
	}
	return v, nil
}

// State is every source, the sample files and every file received,
// newest first.
func (p *Processor) State(ctx context.Context) (*StateView, error) {
	st := &StateView{Updates: []UpdateView{}}
	sent := map[string]bool{}
	updates, _, err := p.Repos.ProviderUpdates.List(ctx, store.ListOptions[*providerv1.ProviderUpdate]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	byJob := map[string]bool{}
	bySource := map[string]*SourceView{}
	for _, s := range Sources {
		st.Sources = append(st.Sources, SourceView{Source: s})
	}
	for i := range st.Sources {
		bySource[st.Sources[i].ID] = &st.Sources[i]
	}
	// Ids are time-ordered: the last approved update of a source says how
	// many participations it has.
	for _, u := range updates {
		v, err := p.updateView(ctx, u)
		if err != nil {
			return nil, err
		}
		st.Updates = append(st.Updates, v)
		byJob[u.GetIngestionJobId()], sent[u.GetFilename()] = true, true
		if sv := bySource[u.GetSourceId()]; sv != nil {
			switch u.GetStatus() {
			case providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_APPROVED:
				sv.Participations = u.GetSummary().GetParticipationsAfter()
			case providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_PROPOSED:
				sv.Pending = u.GetId()
			}
		}
	}
	// Files that failed proposed nothing.
	jobs, _, err := p.Repos.VendorIngestionJobs.List(ctx, store.ListOptions[*ingestionv1.IngestionJob]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if byJob[j.GetId()] || len(j.GetErrors()) == 0 {
			continue
		}
		sent[j.GetOriginalFilename()] = true
		st.Updates = append(st.Updates, UpdateView{ID: j.GetId(), Filename: j.GetOriginalFilename(), Status: "FAILED",
			Received: j.GetReceivedTime().AsTime().Format(time.RFC3339), Problem: j.GetErrors()[0].GetMessage()})
	}
	sort.SliceStable(st.Updates, func(i, j int) bool { return st.Updates[i].Received > st.Updates[j].Received })
	for _, s := range Samples() {
		st.Samples = append(st.Samples, SampleView{ID: s.ID, Title: s.Title, Summary: s.Summary, SourceID: s.SourceID, Sent: sent[s.Filename]})
	}
	return st, nil
}

// changesShown caps the changes one view lists; the summary counts them
// all.
const changesShown = 100

// Update is one update with its first changes of a kind (ADDED, CHANGED,
// DROPPED; any when empty).
func (p *Processor) Update(ctx context.Context, id, kind string) (*UpdateView, error) {
	u, err := p.Repos.ProviderUpdates.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	v, err := p.updateView(ctx, u)
	if err != nil {
		return nil, err
	}
	src, _ := SourceByID(u.GetSourceId())
	today := dates.FromTime(p.Now())
	changes, _, err := p.Repos.VendorProviderChanges.List(ctx, store.ListOptions[*providerv1.VendorProviderChange]{
		PageSize: changesShown, Where: []store.Cond{store.Eq(id, "providerUpdateId")},
		Filter: func(c *providerv1.VendorProviderChange) bool {
			return kind == "" || trim(c.GetKind(), "VENDOR_CHANGE_KIND_") == kind
		},
	})
	if err != nil {
		return nil, err
	}
	v.Changes = []ChangeView{}
	for _, c := range changes {
		cv := ChangeView{NPI: c.GetNpi(), TIN: c.GetTin(), Kind: trim(c.GetKind(), "VENDOR_CHANGE_KIND_"), Rows: len(c.GetRows()), Aspects: []string{}}
		for _, a := range c.GetAspects() {
			cv.Aspects = append(cv.Aspects, trim(a, "VENDOR_CHANGE_ASPECT_"))
		}
		if rows := c.GetPreviousRows(); len(rows) > 0 {
			cv.Before, cv.Name = partView(Derive(src, c.GetNpi(), c.GetTin(), rows), today), personName(nameOf(rows))
		}
		if rows := c.GetRows(); len(rows) > 0 {
			cv.After, cv.Name = partView(Derive(src, c.GetNpi(), c.GetTin(), rows), today), personName(nameOf(rows))
		}
		v.Changes = append(v.Changes, cv)
	}
	return &v, nil
}

// LookupView is the roll-up for an NPI (the TINs it bills under) or a TIN
// (the NPIs under it).
type LookupView struct {
	// "npi", "tin" or "" when nothing matches.
	Kind  string     `json:"kind"`
	Query string     `json:"query"`
	Name  string     `json:"name"`
	Parts []PartView `json:"parts"`
	// Parts is capped; Total is how many there are, up to a page.
	More bool `json:"more"`
}

// Lookup reads the roll-up for an NPI or a TIN from provider data as it
// stands: approved updates only.
func (p *Processor) Lookup(ctx context.Context, q string) (*LookupView, error) {
	q = strings.TrimSpace(q)
	out := &LookupView{Query: q, Parts: []PartView{}}
	if q == "" {
		return out, nil
	}
	today := dates.FromTime(p.Now())
	networks := map[string]string{}
	network := func(id string) string {
		if _, ok := networks[id]; !ok {
			networks[id] = id
			if n, err := p.Repos.UnderlyingNetworks.Get(ctx, id); err == nil {
				networks[id] = n.GetName()
			}
		}
		return networks[id]
	}
	for _, by := range []string{"npi", "tin"} {
		parts, next, err := p.Repos.Participations.List(ctx, store.ListOptions[*providerv1.NetworkParticipation]{PageSize: changesShown, Where: []store.Cond{store.Eq(q, by)}})
		if err != nil {
			return nil, err
		}
		if len(parts) == 0 {
			continue
		}
		out.Kind, out.More = by, next != ""
		var npis []string
		for _, pt := range parts {
			npis = append(npis, pt.GetNpi())
		}
		providers, _, err := p.Repos.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{PageSize: 4 * len(npis), Where: []store.Cond{store.In(npis, "npi")}})
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		for _, pr := range providers {
			names[pr.GetNpi()] = personName(pr.GetName())
		}
		for _, pt := range parts {
			v := partView(pt, today)
			v.Name, v.Network = names[pt.GetNpi()], network(pt.GetUnderlyingNetworkId())
			out.Parts = append(out.Parts, *v)
		}
		out.Name = names[q]
		if by == "tin" {
			out.Name = parts[0].GetBillingName()
		}
		return out, nil
	}
	return out, nil
}

// Examples are an NPI with several TINs and a TIN with several NPIs from
// provider data as it stands, for the lookup's suggestions.
func (p *Processor) Examples(ctx context.Context) ([]string, error) {
	parts, _, err := p.Repos.Participations.List(ctx, store.ListOptions[*providerv1.NetworkParticipation]{PageSize: 400})
	if err != nil {
		return nil, err
	}
	tins, npis := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, pt := range parts {
		if tins[pt.GetNpi()] == nil {
			tins[pt.GetNpi()] = map[string]bool{}
		}
		if npis[pt.GetTin()] == nil {
			npis[pt.GetTin()] = map[string]bool{}
		}
		tins[pt.GetNpi()][pt.GetTin()], npis[pt.GetTin()][pt.GetNpi()] = true, true
	}
	best := func(m map[string]map[string]bool) string {
		top := ""
		for k, v := range m {
			if top == "" || len(v) > len(m[top]) || len(v) == len(m[top]) && k < top {
				top = k
			}
		}
		return top
	}
	var out []string
	for _, x := range []string{best(tins), best(npis)} {
		if x != "" {
			out = append(out, x)
		}
	}
	return out, nil
}
