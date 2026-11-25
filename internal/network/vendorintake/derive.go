package vendorintake

import (
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/dates"
)

// ParticipationID is the id of the participation a source states for an
// NPI and TIN.
func ParticipationID(sourceID, npi, tin string) string { return sourceID + "/" + npi + "/" + tin }

var dateLayouts = []string{"2006-01-02", "01/02/2006", "1/2/2006", "20060102"}

// parseDate reads a date as vendors write them.
func parseDate(s string) (*commonv1.Date, bool) {
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, strings.TrimSpace(s)); err == nil {
			return dates.FromTime(t), true
		}
	}
	return nil, false
}

// Derive is the participation a source's rows for one NPI and TIN state:
// a pure function of the rows, so the same file always gives the same
// provider data.
//
//   - Periods: each distinct start and term date among the rows, earliest
//     first. A full file carries past periods as rows of their own.
//   - Specialties: every one listed, in file order (extra rows continue
//     the list).
//   - Locations and the billing name: from the rows, the latest period's
//     first.
func Derive(src Source, npi, tin string, rows []*providerv1.VendorProviderRow) *providerv1.NetworkParticipation {
	p := &providerv1.NetworkParticipation{
		Id: ParticipationID(src.ID, npi, tin), SourceId: src.ID, UnderlyingNetworkId: src.UnderlyingNetworkID,
		Npi: npi, Tin: tin,
	}
	for _, r := range rows {
		for _, s := range r.GetSpecialties() {
			if !slices.Contains(p.Specialties, s) {
				p.Specialties = append(p.Specialties, s)
			}
		}
	}
	for _, r := range latestFirst(rows) {
		if p.BillingName == "" {
			p.BillingName = r.GetBillingName()
		}
		start, _ := parseDate(r.GetStartDate())
		period := &commonv1.DateRange{Start: start}
		if r.GetTermDate() != "" {
			period.End, _ = parseDate(r.GetTermDate())
		}
		if !slices.ContainsFunc(p.Periods, func(x *commonv1.DateRange) bool { return proto.Equal(x, period) }) {
			p.Periods = append(p.Periods, period)
		}
		if r.GetAddress() == nil && r.GetPhone() == "" {
			continue
		}
		loc := &providerv1.ServiceLocation{Address: r.GetAddress(), Phone: r.GetPhone()}
		if !slices.ContainsFunc(p.Locations, func(x *providerv1.ServiceLocation) bool { return proto.Equal(x, loc) }) {
			p.Locations = append(p.Locations, loc)
		}
	}
	sort.SliceStable(p.Periods, func(i, j int) bool { return dates.Cmp(p.Periods[i].GetStart(), p.Periods[j].GetStart()) < 0 })
	return p
}

// latestFirst orders rows by start date, latest first, then file order.
func latestFirst(rows []*providerv1.VendorProviderRow) []*providerv1.VendorProviderRow {
	out := slices.Clone(rows)
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := parseDate(out[i].GetStartDate())
		b, _ := parseDate(out[j].GetStartDate())
		return dates.Cmp(a, b) > 0
	})
	return out
}

// nameOf is the provider's name on the rows: the latest period's.
func nameOf(rows []*providerv1.VendorProviderRow) *commonv1.PersonName {
	for _, r := range latestFirst(rows) {
		if r.GetName().GetLast() != "" || r.GetName().GetFirst() != "" {
			return r.GetName()
		}
	}
	return nil
}

// aspects says what differs between the rows a pair had and the rows it
// has now, in terms of what they derive.
func aspects(src Source, npi, tin string, before, now []*providerv1.VendorProviderRow) []providerv1.VendorChangeAspect {
	a, b := Derive(src, npi, tin, before), Derive(src, npi, tin, now)
	var out []providerv1.VendorChangeAspect
	if !slices.EqualFunc(a.GetPeriods(), b.GetPeriods(), func(x, y *commonv1.DateRange) bool { return proto.Equal(x, y) }) {
		if termed(a, b) {
			out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_TERMED)
		} else {
			out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_PERIODS)
		}
	}
	if !slices.Equal(a.GetSpecialties(), b.GetSpecialties()) {
		out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_SPECIALTIES)
	}
	if !slices.EqualFunc(a.GetLocations(), b.GetLocations(), func(x, y *providerv1.ServiceLocation) bool { return proto.Equal(x, y) }) {
		out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_LOCATION)
	}
	if a.GetBillingName() != b.GetBillingName() {
		out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_BILLING_NAME)
	}
	if !proto.Equal(nameOf(before), nameOf(now)) {
		out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_NAME)
	}
	if len(out) == 0 {
		out = append(out, providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_OTHER)
	}
	return out
}

// termed reports whether a period that was open now has an end, and no
// period is open any more.
func termed(before, now *providerv1.NetworkParticipation) bool {
	open := func(p *providerv1.NetworkParticipation) bool {
		return slices.ContainsFunc(p.GetPeriods(), func(r *commonv1.DateRange) bool { return r.GetEnd() == nil })
	}
	return open(before) && !open(now)
}
