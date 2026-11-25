package vendorintake

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/ids"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/network/networkdb"
)

// ErrPending: the source already has an update awaiting a decision. Its
// next file is compared with the last approved one, so the decision comes
// first.
var ErrPending = errors.New("vendorintake: the source has an update awaiting a decision")

// Processor takes in vendor provider files and applies approved updates.
type Processor struct {
	Repos *repo.Repos
	Now   func() time.Time
}

const (
	// How many row errors a job keeps as a sample.
	errorSample = 20
	// How many records are written or read at a time.
	batch = 500
)

// Ingest reads one full file for a source and returns its job and the
// update it proposes. It changes no provider data. A file that can't be
// read as the source's layout fails as a whole (the job says why) and
// proposes nothing.
func (p *Processor) Ingest(ctx context.Context, sourceID, filename string, r io.Reader) (*ingestionv1.IngestionJob, *providerv1.ProviderUpdate, error) {
	src, ok := SourceByID(sourceID)
	if !ok {
		return nil, nil, fmt.Errorf("vendorintake: no source %q", sourceID)
	}
	if err := p.pending(ctx, src); err != nil {
		return nil, nil, err
	}
	now := p.Now()
	job, err := p.Repos.VendorIngestionJobs.Create(ctx, &ingestionv1.IngestionJob{
		Id:               ids.NewV7(now),
		Kind:             ingestionv1.IngestionKind_INGESTION_KIND_VENDOR_PROVIDERS,
		Format:           ingestionv1.IngestionFormat_INGESTION_FORMAT_CSV,
		Mode:             ingestionv1.LoadMode_LOAD_MODE_FULL,
		OriginalFilename: filename,
		Status:           ingestionv1.IngestionStatus_INGESTION_STATUS_VALIDATING,
		ReceivedTime:     timestamppb.New(now),
		Counts:           &ingestionv1.IngestionCounts{},
	})
	if err != nil {
		return nil, nil, err
	}
	fail := func(code, msg string) (*ingestionv1.IngestionJob, *providerv1.ProviderUpdate, error) {
		job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED
		job.Errors = append(job.Errors, &ingestionv1.RecordError{Code: code, Message: msg})
		job.CompletedTime = timestamppb.New(p.Now())
		job, err := p.Repos.VendorIngestionJobs.Update(ctx, job)
		return job, nil, err
	}

	rd, err := newReader(r, src.Layout)
	if err != nil {
		return fail("UNREADABLE", err.Error())
	}
	db := p.Repos.DB.(*store.PGDB)
	rows, err := networkdb.NewFileRows(ctx, db, job.GetId())
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Drop(context.WithoutCancel(ctx)) }()
	if err := rows.Load(ctx, rd.next); err != nil {
		var bad *csv.ParseError
		if errors.As(err, &bad) {
			return fail("UNREADABLE", bad.Error())
		}
		return nil, nil, err
	}
	job.Counts.RecordsTotal, job.Counts.RecordsErrored, job.Errors = rd.read, rd.errored, rd.errs
	if rd.read == 0 {
		return fail("EMPTY", "the file has no rows")
	}

	var update *providerv1.ProviderUpdate
	err = p.Repos.DB.InTx(ctx, func(ctx context.Context) error {
		// One file at a time per underlying network.
		if err := p.Repos.UnderlyingNetworks.Lock(ctx, src.UnderlyingNetworkID); err != nil {
			return err
		}
		if err := p.pending(ctx, src); err != nil {
			return err
		}
		update = &providerv1.ProviderUpdate{
			Id: ids.NewV7(p.Now()), IngestionJobId: job.GetId(), SourceId: src.ID, UnderlyingNetworkId: src.UnderlyingNetworkID,
			Filename: filename, Status: providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_PROPOSED,
		}
		if update.Summary, err = p.propose(ctx, src, update.GetId(), rows); err != nil {
			return err
		}
		update.Summary.RowsRead, update.Summary.RowsErrored = rd.read, rd.errored
		if update, err = p.Repos.ProviderUpdates.Create(ctx, update); err != nil {
			return err
		}
		job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_AWAITING_APPROVAL
		job, err = p.Repos.VendorIngestionJobs.Update(ctx, job)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return job, update, nil
}

// pending fails with ErrPending if the source has a proposed update.
func (p *Processor) pending(ctx context.Context, src Source) error {
	open, _, err := p.Repos.ProviderUpdates.List(ctx, store.ListOptions[*providerv1.ProviderUpdate]{
		PageSize: 1, Where: []store.Cond{store.Eq(src.ID, "sourceId")},
		Filter: func(u *providerv1.ProviderUpdate) bool {
			return u.GetStatus() == providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_PROPOSED
		},
	})
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return fmt.Errorf("%w: %s (%s)", ErrPending, open[0].GetFilename(), src.Name)
	}
	return nil
}

// propose records every difference between the file and what the source
// states today as a change of the update, and totals them.
func (p *Processor) propose(ctx context.Context, src Source, updateID string, rows *networkdb.FileRows) (*providerv1.ProviderUpdateSummary, error) {
	counts, err := rows.Counts(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	sum := &providerv1.ProviderUpdateSummary{
		ParticipationsBefore: counts.Before, NewProviders: counts.NewProviders, NewTins: counts.NewTINs,
	}
	byAspect := map[providerv1.VendorChangeAspect]int64{}
	byState := map[string]*providerv1.StateCount{}
	var pending []*providerv1.VendorProviderChange
	flush := func() error {
		err := p.Repos.VendorProviderChanges.CreateBatch(ctx, pending)
		pending = pending[:0]
		return err
	}
	err = rows.Compare(ctx, src.ID, func(d networkdb.Difference) error {
		ch := &providerv1.VendorProviderChange{
			Id: ids.NewV7(p.Now()), ProviderUpdateId: updateID, Npi: d.NPI, Tin: d.TIN,
			ParticipationId: ParticipationID(src.ID, d.NPI, d.TIN), RowsHash: d.Hash,
		}
		if ch.Rows, err = decodeRows(d.Rows); err != nil {
			return err
		}
		if ch.PreviousRows, err = decodeRows(d.PreviousRows); err != nil {
			return err
		}
		state := stateOf(ch.GetRows())
		if len(ch.GetRows()) == 0 {
			state = stateOf(ch.GetPreviousRows())
		}
		sc := byState[state]
		if sc == nil {
			sc = &providerv1.StateCount{State: state}
			byState[state] = sc
		}
		switch {
		case d.ParticipationID == "":
			ch.Kind = providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_ADDED
			sum.Added++
			sc.Added++
		case d.Hash == "":
			ch.Kind = providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_DROPPED
			sum.Removed++
			sc.Removed++
		default:
			ch.Kind = providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_CHANGED
			ch.Aspects = aspects(src, d.NPI, d.TIN, ch.GetPreviousRows(), ch.GetRows())
			for _, a := range ch.GetAspects() {
				byAspect[a]++
			}
			sum.Changed++
			sc.Changed++
		}
		if pending = append(pending, ch); len(pending) == batch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	sum.Unchanged = counts.Pairs - sum.Added - sum.Changed
	sum.ParticipationsAfter = counts.Before + sum.Added - sum.Removed
	for a, n := range byAspect {
		sum.Aspects = append(sum.Aspects, &providerv1.AspectCount{Aspect: a, Count: n})
	}
	sort.Slice(sum.Aspects, func(i, j int) bool { return sum.Aspects[i].GetAspect() < sum.Aspects[j].GetAspect() })
	for _, sc := range byState {
		sum.States = append(sum.States, sc)
	}
	total := func(s *providerv1.StateCount) int64 { return s.GetAdded() + s.GetChanged() + s.GetRemoved() }
	sort.Slice(sum.States, func(i, j int) bool {
		if a, b := total(sum.States[i]), total(sum.States[j]); a != b {
			return a > b
		}
		return sum.States[i].GetState() < sum.States[j].GetState()
	})
	return sum, nil
}

func stateOf(rows []*providerv1.VendorProviderRow) string {
	for _, r := range latestFirst(rows) {
		if s := r.GetAddress().GetState(); s != "" {
			return s
		}
	}
	return ""
}

func decodeRows(raw []json.RawMessage) ([]*providerv1.VendorProviderRow, error) {
	var out []*providerv1.VendorProviderRow
	for _, b := range raw {
		r := &providerv1.VendorProviderRow{}
		if err := protojson.Unmarshal(b, r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// reader streams a file's rows: each as sent, with a fingerprint of its
// content. Rows that can't be used are counted and left out.
type reader struct {
	csv     *csv.Reader
	layout  Layout
	headers []string
	col     map[string]int
	// Column indexes in header order, so a row's fingerprint doesn't
	// depend on the order of the file's columns.
	sorted []int
	mapped map[int]bool
	line   int64

	read, errored int64
	errs          []*ingestionv1.RecordError
}

func newReader(r io.Reader, l Layout) (*reader, error) {
	c := csv.NewReader(bufio.NewReaderSize(r, 1<<20))
	c.FieldsPerRecord = -1
	c.LazyQuotes = true
	head, err := c.Read()
	if err == io.EOF {
		return nil, errors.New("the file is empty")
	} else if err != nil {
		return nil, err
	}
	rd := &reader{csv: c, layout: l, col: map[string]int{}, mapped: map[int]bool{}, line: 1}
	for i, h := range head {
		h = strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(h, "\xef\xbb\xbf")))
		rd.headers = append(rd.headers, h)
		rd.col[h] = i
		rd.sorted = append(rd.sorted, i)
	}
	sort.Slice(rd.sorted, func(i, j int) bool { return rd.headers[rd.sorted[i]] < rd.headers[rd.sorted[j]] })
	var missing []string
	for _, h := range []string{l.NPI, l.TIN, l.Start} {
		if _, ok := rd.col[h]; !ok {
			missing = append(missing, h)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("the file has no %s column", strings.Join(missing, ", "))
	}
	for _, h := range slices.Concat([]string{l.NPI, l.TIN, l.Last, l.First, l.Middle, l.Suffix, l.BillingName, l.City, l.State, l.Zip, l.Phone, l.Start, l.Term}, l.Specialties, l.AddressLines) {
		if i, ok := rd.col[h]; ok {
			rd.mapped[i] = true
		}
	}
	return rd, nil
}

// next is the next usable row, nil at the end of the file.
func (rd *reader) next() (*networkdb.FileRow, error) {
	for {
		rec, err := rd.csv.Read()
		if err == io.EOF {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
		rd.line++
		rd.read++
		get := func(h string) string {
			if i, ok := rd.col[h]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		l := rd.layout
		row := &providerv1.VendorProviderRow{
			RowNumber: rd.line, Npi: get(l.NPI), Tin: get(l.TIN), BillingName: get(l.BillingName),
			Phone: get(l.Phone), StartDate: get(l.Start), TermDate: get(l.Term),
		}
		if problem := check(row); problem != "" {
			rd.errored++
			if len(rd.errs) < errorSample {
				rd.errs = append(rd.errs, &ingestionv1.RecordError{
					RecordIndex: rd.line, Code: "ROW_NOT_USABLE", Message: problem, RecordKey: strings.Trim(row.GetNpi()+" / "+row.GetTin(), " /"),
				})
			}
			continue
		}
		if n := (&commonv1.PersonName{First: get(l.First), Middle: get(l.Middle), Last: get(l.Last), Suffix: get(l.Suffix)}); n.First+n.Middle+n.Last+n.Suffix != "" {
			row.Name = n
		}
		for _, h := range l.Specialties {
			if s := get(h); s != "" {
				row.Specialties = append(row.Specialties, s)
			}
		}
		ad := &commonv1.Address{City: get(l.City), State: get(l.State), PostalCode: get(l.Zip)}
		for _, h := range l.AddressLines {
			if s := get(h); s != "" {
				ad.Lines = append(ad.Lines, s)
			}
		}
		if len(ad.Lines) > 0 || ad.City+ad.State+ad.PostalCode != "" {
			row.Address = ad
		}
		// The fingerprint covers every column as sent, by header.
		h := sha256.New()
		for _, i := range rd.sorted {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			if !rd.mapped[i] && v != "" {
				if row.OtherColumns == nil {
					row.OtherColumns = map[string]string{}
				}
				row.OtherColumns[rd.headers[i]] = v
			}
			h.Write([]byte(rd.headers[i]))
			h.Write([]byte{0x1f})
			h.Write([]byte(strings.TrimSpace(v)))
			h.Write([]byte{0x1e})
		}
		doc, err := protojson.Marshal(row)
		if err != nil {
			return nil, err
		}
		return &networkdb.FileRow{NPI: row.GetNpi(), TIN: row.GetTin(), RowNumber: rd.line, Hash: hex.EncodeToString(h.Sum(nil)), JSON: doc}, nil
	}
}

// check says why a row can't be used, or "".
func check(row *providerv1.VendorProviderRow) string {
	switch {
	case row.GetNpi() == "":
		return "no NPI"
	case row.GetTin() == "":
		return "no TIN"
	case row.GetStartDate() == "":
		return "no start date"
	}
	if _, ok := parseDate(row.GetStartDate()); !ok {
		return fmt.Sprintf("start date %q is not a date", row.GetStartDate())
	}
	if row.GetTermDate() != "" {
		if _, ok := parseDate(row.GetTermDate()); !ok {
			return fmt.Sprintf("term date %q is not a date", row.GetTermDate())
		}
	}
	return ""
}
