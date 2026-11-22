// Package eligibility keeps members and their coverage.
//
// Eligibility is derived, not edited. Everything that can change it is
// recorded as an EligibilityInput, in order, and never changed: 834
// member loops from intake, and approvals of full-file terminations.
// Members and coverage are what replaying the inputs gives (see Derive).
// Each input asserts something about a member for a range of dates, and
// for any date the latest-recorded assertion wins, so data that flips
// back and forth (a retro term, a reinstatement, a corrected date) is a
// series of assertions rather than edits.
//
// Whenever the derived member changes, a MemberVersion is saved with the
// time it was recorded and the input that caused it. The versions are
// the audit trail: the latest version recorded at or before a time is
// what we believed then. Regenerating replays every input under the
// current rules; a difference is saved as new versions, never by
// rewriting old ones, because claims were adjudicated against them.
//
// Nothing here adjusts claims. A retroactive change shows up in the
// member's versions; what to do about claims already paid is decided
// elsewhere, by people, for now.
package eligibility

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// Service records inputs and keeps members derived from them.
type Service struct {
	Repos *repo.Repos
	Now   func() time.Time
}

// Receive records an 834 member loop as an input and applies it. It is
// what enrollment intake hands each loop to.
func (s *Service) Receive(ctx context.Context, t *eligibilityv1.EnrollmentTransaction) (*eligibilityv1.EnrollmentTransaction, error) {
	// Recorded as by whoever sent the file.
	if sender := t.GetSource().GetSenderId(); sender != "" {
		ctx = store.WithActor(ctx, sender)
	}
	var saved *eligibilityv1.EnrollmentTransaction
	err := s.retry(ctx, func(ctx context.Context) error {
		var err error
		if saved, err = s.Repos.EnrollmentTransactions.Create(ctx, t); err != nil {
			return err
		}
		_, err = s.record(ctx, &eligibilityv1.EligibilityInput{
			Input: &eligibilityv1.EligibilityInput_EnrollmentTransactionId{EnrollmentTransactionId: saved.GetId()},
		})
		return err
	})
	return saved, err
}

// ApproveFullFile approves the terminations a full file proposed.
func (s *Service) ApproveFullFile(ctx context.Context, jobID, setControl, note string) error {
	st, err := s.State(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, p := range st.Proposals {
		if p.JobID == jobID && p.SetControl == setControl {
			if p.Approved {
				return fmt.Errorf("eligibility: that full file's terminations were already approved")
			}
			found = true
		}
	}
	if !found {
		return fmt.Errorf("eligibility: no terminations proposed for that full file")
	}
	return s.retry(ctx, func(ctx context.Context) error {
		_, err := s.record(ctx, &eligibilityv1.EligibilityInput{Input: &eligibilityv1.EligibilityInput_FullFileApproval{
			FullFileApproval: &eligibilityv1.FullFileApproval{IngestionJobId: jobID, TransactionSetControlNumber: setControl, Note: note},
		}})
		return err
	})
}

// retry runs f in a transaction, again if another input took the same
// sequence number first.
func (s *Service) retry(ctx context.Context, f func(ctx context.Context) error) error {
	var err error
	for range 5 {
		err = s.Repos.InTx(ctx, f)
		if !errors.Is(err, store.ErrAlreadyExists) && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return err
}

// record appends an input with the next sequence number and applies it.
func (s *Service) record(ctx context.Context, in *eligibilityv1.EligibilityInput) (*eligibilityv1.EligibilityInput, error) {
	inputs, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	n := int64(len(inputs)) + 1
	in.Id, in.Sequence = fmt.Sprintf("in_%08d", n), n
	in.RecordedTime, in.RecordedBy = timestamppb.New(s.Now()), store.Actor(ctx)
	if in, err = s.Repos.EligibilityInputs.Create(ctx, in); err != nil {
		return nil, err
	}
	st, err := Derive(ctx, s.Repos, append(inputs, in))
	if err != nil {
		return nil, err
	}
	for _, id := range st.MemberIDs() {
		if _, err := s.save(ctx, st.Members[id], in.GetId(), in.GetSequence(), ""); err != nil {
			return nil, err
		}
	}
	return in, nil
}

// save writes a derived member and a new version of it, if it differs
// from the current one. Reports whether it did.
func (s *Service) save(ctx context.Context, m *eligibilityv1.Member, inputID string, seq int64, reason string) (bool, error) {
	cur, err := s.Repos.Members.Get(ctx, m.GetId())
	switch {
	case errors.Is(err, store.ErrNotFound):
		if _, err := s.Repos.Members.Create(ctx, m); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	default:
		if sameMember(cur, m) {
			return false, nil
		}
		m.Audit = cur.GetAudit()
		if _, err := s.Repos.Members.Update(ctx, m); err != nil {
			return false, err
		}
	}
	history, err := s.History(ctx, m.GetId())
	if err != nil {
		return false, err
	}
	v := int32(len(history)) + 1
	snap := proto.Clone(m).(*eligibilityv1.Member)
	snap.Audit = nil
	_, err = s.Repos.MemberVersions.Create(ctx, &eligibilityv1.MemberVersion{
		Id: fmt.Sprintf("%s:%04d", m.GetId(), v), MemberId: m.GetId(), Version: v,
		RecordedTime: timestamppb.New(s.Now()), InputId: inputID, InputSequence: seq,
		RulesVersion: RulesVersion, Reason: reason, Member: snap,
	})
	return true, err
}

func sameMember(a, b *eligibilityv1.Member) bool {
	a, b = proto.Clone(a).(*eligibilityv1.Member), proto.Clone(b).(*eligibilityv1.Member)
	a.Audit, b.Audit = nil, nil
	return proto.Equal(a, b)
}

// Inputs lists every input in sequence order.
func (s *Service) Inputs(ctx context.Context) ([]*eligibilityv1.EligibilityInput, error) {
	ins, _, err := s.Repos.EligibilityInputs.List(ctx, store.ListOptions[*eligibilityv1.EligibilityInput]{PageSize: 1_000_000})
	return ins, err // ids sort in sequence order
}

// State derives eligibility from every input: the current members, what
// each input did, and the full-file proposals.
func (s *Service) State(ctx context.Context) (*State, error) {
	ins, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	return Derive(ctx, s.Repos, ins)
}

// History lists a member's versions, oldest first.
func (s *Service) History(ctx context.Context, memberID string) ([]*eligibilityv1.MemberVersion, error) {
	vs, _, err := s.Repos.MemberVersions.List(ctx, store.ListOptions[*eligibilityv1.MemberVersion]{PageSize: 100_000,
		Where: []store.Cond{store.Eq(memberID, "memberId")}})
	sort.Slice(vs, func(i, j int) bool { return vs[i].GetVersion() < vs[j].GetVersion() })
	return vs, err
}

// AsOf is the member as we believed at time t: the latest version
// recorded at or before it. Nil when the member didn't exist yet.
func (s *Service) AsOf(ctx context.Context, memberID string, t time.Time) (*eligibilityv1.MemberVersion, error) {
	vs, err := s.History(ctx, memberID)
	if err != nil {
		return nil, err
	}
	var out *eligibilityv1.MemberVersion
	for _, v := range vs {
		if !v.GetRecordedTime().AsTime().After(t) {
			out = v
		}
	}
	return out, nil
}

// Change is a member that regenerating would change.
type Change struct {
	MemberID      string
	Before, After *eligibilityv1.Member
}

// Compare replays every input under rules and reports the members that
// would differ from those on file. It writes nothing: it shows what
// adopting a rules change would do.
func (s *Service) Compare(ctx context.Context, rules Rules) ([]Change, error) {
	ins, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	st, err := DeriveWith(ctx, s.Repos, ins, rules)
	if err != nil {
		return nil, err
	}
	var out []Change
	for _, id := range st.MemberIDs() {
		cur, err := s.Repos.Members.Get(ctx, id)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if cur == nil || !sameMember(cur, st.Members[id]) {
			out = append(out, Change{MemberID: id, Before: cur, After: st.Members[id]})
		}
	}
	return out, nil
}

// Regenerate replays every input under the current rules and compares the
// result with the members on file. With dryRun it only reports the
// differences; otherwise each becomes a new version, recorded now, with
// reason "regenerated". Earlier versions are never touched.
func (s *Service) Regenerate(ctx context.Context, dryRun bool) ([]Change, error) {
	var out []Change
	err := s.Repos.InTx(ctx, func(ctx context.Context) error {
		out = nil
		st, err := s.State(ctx)
		if err != nil {
			return err
		}
		for _, id := range st.MemberIDs() {
			m := st.Members[id]
			cur, err := s.Repos.Members.Get(ctx, id)
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			if cur != nil && sameMember(cur, m) {
				continue
			}
			out = append(out, Change{MemberID: id, Before: cur, After: m})
			if !dryRun {
				if _, err := s.save(ctx, m, "", 0, "regenerated"); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return out, err
}
