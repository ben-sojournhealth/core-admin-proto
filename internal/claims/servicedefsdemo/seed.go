// Package servicedefsdemo sets up the Service Definitions demo: a sandbox
// with a starting set of definitions, edited through the real
// ServiceDefinitionService.
package servicedefsdemo

import (
	"context"
	"time"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/claims/servicedefs"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

const (
	procedure = claimsv1.CodeSystem_CODE_SYSTEM_PROCEDURE
	revenue   = claimsv1.CodeSystem_CODE_SYSTEM_REVENUE
	place     = claimsv1.CodeSystem_CODE_SYSTEM_PLACE_OF_SERVICE
)

func codes(system claimsv1.CodeSystem, start, end string) *claimsv1.CodeEntry {
	return &claimsv1.CodeEntry{System: system, Start: start, End: end}
}

func code(system claimsv1.CodeSystem, c string) *claimsv1.CodeEntry { return codes(system, c, c) }

// from dates an entry: it counts for dates of service from this day on.
func from(e *claimsv1.CodeEntry, y, m, d int32) *claimsv1.CodeEntry {
	e.Effective = &commonv1.DateRange{Start: &commonv1.Date{Year: y, Month: m, Day: d}}
	return e
}

// until dates an entry: it counts for dates of service up to this day.
func until(e *claimsv1.CodeEntry, y, m, d int32) *claimsv1.CodeEntry {
	e.Effective = &commonv1.DateRange{End: &commonv1.Date{Year: y, Month: m, Day: d}}
	return e
}

// Seed is the starting set: the service categories the seeded plans carry
// today (seed/plans), lifted out as definitions of their own.
func Seed() []*claimsv1.ServiceDefinition {
	return []*claimsv1.ServiceDefinition{
		{Id: "emergency-room", Name: "Emergency room", Intent: "Care given in a hospital emergency department.",
			Entries: []*claimsv1.CodeEntry{code(place, "23")}},
		{Id: "preventive-care", Name: "Preventive care", Intent: "Routine preventive visits for well patients.",
			Entries: []*claimsv1.CodeEntry{codes(procedure, "99381", "99397")}},
		{Id: "office-visit", Name: "Office visit", Intent: "Evaluation and management visits in an office or by telehealth.",
			Entries: []*claimsv1.CodeEntry{
				codes(procedure, "99202", "99215"),
				// CPT deleted 99201 for dates of service from January 1, 2021.
				until(code(procedure, "99201"), 2020, 12, 31),
				code(place, "11"), code(place, "02"),
				// CMS added place of service 10 (telehealth in the patient's
				// home) for dates of service from January 1, 2022.
				from(code(place, "10"), 2022, 1, 1),
			}},
		{Id: "advanced-imaging", Name: "Advanced imaging (CT/MRI)", Intent: "CT and MRI scans.",
			Entries: []*claimsv1.CodeEntry{
				codes(procedure, "70450", "70498"), codes(procedure, "70540", "70559"), codes(procedure, "72125", "72158"),
			}},
		{Id: "outpatient-surgery", Name: "Outpatient surgery", Intent: "Surgical procedures.",
			Entries: []*claimsv1.CodeEntry{codes(procedure, "20000", "69999")}},
		{Id: "inpatient-facility", Name: "Inpatient facility", Intent: "Room and board on an inpatient stay.",
			Entries: []*claimsv1.CodeEntry{codes(revenue, "0100", "0219")}},
	}
}

// An edit to a seeded definition, on the day it was made.
type edit struct {
	on     time.Time
	by     string
	change func(d *claimsv1.ServiceDefinition)
}

func day(y int, m time.Month, d, hour int) time.Time {
	return time.Date(y, m, d, hour, 0, 0, 0, time.UTC)
}

// created is when the starting set was first stored.
var created = day(2019, time.March, 4, 15)

// officeVisitAsCreated is Office visit as it was first stored; its edits
// below bring it to what Seed lists.
var officeVisitAsCreated = &claimsv1.ServiceDefinition{
	Id: "office-visit", Name: "Office visit", Intent: "Evaluation and management visits in an office.",
	Entries: []*claimsv1.CodeEntry{codes(procedure, "99201", "99215"), code(place, "11")},
}

// officeVisitEdits is Office visit's edit history.
var officeVisitEdits = []edit{
	{day(2020, time.March, 20, 14), "ops", func(d *claimsv1.ServiceDefinition) {
		d.Entries = append(d.Entries, code(place, "02"))
	}},
	// 99201 leaves the range ahead of its deletion on January 1, 2021.
	{day(2020, time.November, 16, 17), "ops", func(d *claimsv1.ServiceDefinition) {
		d.Entries = []*claimsv1.CodeEntry{
			codes(procedure, "99202", "99215"), until(code(procedure, "99201"), 2020, 12, 31),
			code(place, "11"), code(place, "02"),
		}
	}},
	{day(2021, time.December, 6, 16), "ops", func(d *claimsv1.ServiceDefinition) {
		d.Entries = append(d.Entries, from(code(place, "10"), 2022, 1, 1))
	}},
	{day(2022, time.February, 10, 19), "ops", func(d *claimsv1.ServiceDefinition) {
		d.Intent = "Evaluation and management visits in an office or by telehealth."
	}},
}

// NewIn returns the service over repos, which must be empty, with the
// starting set created through it. Office visit is created as it first
// was and then edited, each edit stored on its own day, so it has an edit
// history to look at; it ends as Seed lists it.
func NewIn(ctx context.Context, repos *repo.Repos) (*servicedefs.Service, error) {
	svc := servicedefs.New(repos.ServiceDefinitions)
	// The store stamps what it writes with its clock: set it to each day.
	db := repos.DB.(*store.PGDB)
	clock := db.Now
	defer func() { db.Now = clock }()
	at := func(t time.Time) { db.Now = func() time.Time { return t } }

	at(created)
	seed := store.WithActor(ctx, "seed")
	for _, d := range Seed() {
		if d.GetId() == officeVisitAsCreated.GetId() {
			d = officeVisitAsCreated
		}
		if _, err := svc.CreateServiceDefinition(seed, &claimsv1.CreateServiceDefinitionRequest{Definition: d}); err != nil {
			return nil, err
		}
	}
	for _, e := range officeVisitEdits {
		at(e.on)
		res, err := svc.GetServiceDefinition(ctx, &claimsv1.GetServiceDefinitionRequest{Id: officeVisitAsCreated.GetId()})
		if err != nil {
			return nil, err
		}
		d := res.GetDefinition()
		e.change(d)
		if _, err := svc.UpdateServiceDefinition(store.WithActor(ctx, e.by), &claimsv1.UpdateServiceDefinitionRequest{Definition: d}); err != nil {
			return nil, err
		}
	}
	return svc, nil
}
