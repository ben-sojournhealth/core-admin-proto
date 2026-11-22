// Package eligibilitydemo runs eligibility on sandboxes for the demos: the
// shared Enrollment Intake / Eligibility sandbox, and the walkthrough,
// whose every example plays real 834 samples through a fresh one.
package eligibilitydemo

import (
	"context"
	"fmt"
	"time"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
	"coreadmin/internal/employer/eligibility"
	"coreadmin/internal/employer/enrollmentintake"
	"coreadmin/internal/seed"
	seeddata "coreadmin/seed"
)

// Sandbox is eligibility with the seed customers, plans and groups, no
// members, and each client's open-enrollment file loaded. Its clock
// starts at 9:00 on March 16, 2026 and moves a minute per step.
type Sandbox struct {
	Repos  *repo.Repos
	Elig   *eligibility.Service
	Intake *enrollmentintake.Processor
	clock  time.Time
}

// Prepare loads the seed customers, plans and groups into repos, which
// must be empty, and removes the seed members.
func Prepare(repos *repo.Repos) (*repo.Repos, error) {
	ctx := context.Background()
	if _, err := seed.Load(ctx, repos, seeddata.FS); err != nil {
		return nil, err
	}
	seeded, _, err := repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1000})
	if err != nil {
		return nil, err
	}
	for _, m := range seeded {
		if err := repos.Members.Delete(ctx, m.GetId()); err != nil {
			return nil, err
		}
	}
	return repos, nil
}

// NewSandboxIn sets up a sandbox in repos, which must be empty.
func NewSandboxIn(empty *repo.Repos) (*Sandbox, error) {
	repos, err := Prepare(empty)
	if err != nil {
		return nil, err
	}
	s := &Sandbox{Repos: repos, clock: time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)}
	s.Elig = &eligibility.Service{Repos: repos, Now: s.tick}
	s.Intake = &enrollmentintake.Processor{Repos: repos, Eligibility: s.Elig, Files: map[string][]byte{}, ReceiverID: enrollmentintake.OurID, Now: s.tick}
	ctx := context.Background()
	for _, b := range enrollmentintake.Baselines() {
		if err := s.send(ctx, b); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Sandbox) tick() time.Time { s.clock = s.clock.Add(time.Minute); return s.clock }

// Now is the sandbox's current time, without moving it.
func (s *Sandbox) Now() time.Time { return s.clock }

// Send loads a sample 834.
func (s *Sandbox) Send(ctx context.Context, sampleID string) error {
	for _, x := range enrollmentintake.Samples() {
		if x.ID == sampleID {
			return s.send(ctx, x)
		}
	}
	return fmt.Errorf("no sample %s", sampleID)
}

func (s *Sandbox) send(ctx context.Context, x enrollmentintake.Sample) error {
	job, err := s.Intake.Ingest(ctx, x.Filename, []byte(x.Text))
	if err != nil {
		return err
	}
	if job.GetCounts().GetRecordsErrored() > 0 {
		return fmt.Errorf("%s: %d member loop(s) not loaded", x.Filename, job.GetCounts().GetRecordsErrored())
	}
	return nil
}

// Member finds a current member by first name.
func (s *Sandbox) Member(ctx context.Context, first string) (*eligibilityv1.Member, error) {
	ms, _, err := s.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 1000,
		Filter: func(m *eligibilityv1.Member) bool { return m.GetName().GetFirst() == first }})
	if err != nil {
		return nil, err
	}
	if len(ms) != 1 {
		return nil, fmt.Errorf("%d members named %s", len(ms), first)
	}
	return ms[0], nil
}
