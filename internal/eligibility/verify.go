package eligibility

import (
	"context"
	"fmt"
	"sort"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	"coreadmin/internal/store"
)

// Check is one property that must hold of the stored eligibility.
type Check struct {
	Name string `json:"name"`
	// What it means, in a sentence.
	Meaning string `json:"meaning"`
	OK      bool   `json:"ok"`
	// What was checked, or the first thing that failed.
	Detail string `json:"detail"`
}

// Verify checks the stored inputs, members and versions against each
// other. Every check must pass; a failure means the audit trail and the
// inputs disagree.
func (s *Service) Verify(ctx context.Context) ([]Check, error) {
	ins, err := s.Inputs(ctx)
	if err != nil {
		return nil, err
	}
	members, _, err := s.Repos.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 100_000})
	if err != nil {
		return nil, err
	}
	all, _, err := s.Repos.MemberVersions.List(ctx, store.ListOptions[*eligibilityv1.MemberVersion]{PageSize: 1_000_000})
	if err != nil {
		return nil, err
	}
	byMember := map[string][]*eligibilityv1.MemberVersion{}
	for _, v := range all {
		byMember[v.GetMemberId()] = append(byMember[v.GetMemberId()], v)
	}
	for _, vs := range byMember {
		sort.Slice(vs, func(i, j int) bool { return vs[i].GetVersion() < vs[j].GetVersion() })
	}
	inputIDs := map[string]bool{}
	for _, in := range ins {
		inputIDs[in.GetId()] = true
	}
	var out []Check

	// 1. Inputs are numbered 1..n with no gaps.
	c := Check{Name: "Inputs are a gapless sequence", Meaning: "Nothing was removed from or inserted into the input log.", OK: true}
	for i, in := range ins {
		if in.GetSequence() != int64(i+1) || in.GetId() != fmt.Sprintf("in_%08d", i+1) {
			c.OK, c.Detail = false, fmt.Sprintf("position %d holds %s (sequence %d)", i+1, in.GetId(), in.GetSequence())
			break
		}
	}
	if c.OK {
		c.Detail = fmt.Sprintf("%d inputs, numbered 1 to %d", len(ins), len(ins))
	}
	out = append(out, c)

	// 2. Every version traces to an input, or says it was regenerated;
	// versions are numbered 1..n and recorded in order.
	c = Check{Name: "Every version has a cause", Meaning: "Each change to a member points at the input that caused it, in the order recorded.", OK: true}
	n := 0
	for id, vs := range byMember {
		for i, v := range vs {
			n++
			switch {
			case v.GetVersion() != int32(i+1):
				c.OK, c.Detail = false, fmt.Sprintf("%s: version %d at position %d", id, v.GetVersion(), i+1)
			case v.GetReason() != "regenerated" && !inputIDs[v.GetInputId()]:
				c.OK, c.Detail = false, fmt.Sprintf("%s v%d names input %q, which doesn't exist", id, v.GetVersion(), v.GetInputId())
			case i > 0 && v.GetRecordedTime().AsTime().Before(vs[i-1].GetRecordedTime().AsTime()):
				c.OK, c.Detail = false, fmt.Sprintf("%s v%d was recorded before v%d", id, v.GetVersion(), i)
			}
		}
	}
	if c.OK {
		c.Detail = fmt.Sprintf("%d versions of %d members", n, len(byMember))
	}
	out = append(out, c)

	// 3. The latest version of each member is the member on file.
	c = Check{Name: "The latest version is what's on file", Meaning: "The current members claims read are exactly the last recorded versions.", OK: true}
	for _, m := range members {
		vs := byMember[m.GetId()]
		if len(vs) == 0 || !sameMember(vs[len(vs)-1].GetMember(), m) {
			c.OK, c.Detail = false, fmt.Sprintf("%s differs from its latest version", m.GetId())
			break
		}
	}
	if c.OK {
		c.Detail = fmt.Sprintf("%d members", len(members))
	}
	out = append(out, c)

	// 4. Replaying every input reproduces the members on file.
	c = Check{Name: "Replay reproduces today", Meaning: "Throwing the members away and replaying every input gives back exactly what's on file.", OK: true}
	st, err := Derive(ctx, s.Repos, ins)
	if err != nil {
		return nil, err
	}
	onFile := map[string]*eligibilityv1.Member{}
	for _, m := range members {
		onFile[m.GetId()] = m
	}
	for _, id := range st.MemberIDs() {
		if m := onFile[id]; m == nil || !sameMember(m, st.Members[id]) {
			c.OK, c.Detail = false, fmt.Sprintf("replaying gives a different %s", id)
			break
		}
	}
	if c.OK && len(st.Members) != len(members) {
		c.OK, c.Detail = false, fmt.Sprintf("replay gives %d members, %d on file", len(st.Members), len(members))
	}
	if c.OK {
		c.Detail = fmt.Sprintf("replayed %d inputs into %d members, no differences", len(ins), len(members))
	}
	out = append(out, c)

	// 5. At every point in the log, replaying the inputs so far gives the
	// versions recorded so far: the audit trail is the replay, step by
	// step, not just at the end.
	c = Check{Name: "History matches replay at every step", Meaning: "For each input, replaying only the inputs up to it gives exactly the versions we recorded at that moment.", OK: true}
	regenerated := false
	for _, v := range all {
		if v.GetReason() == "regenerated" {
			regenerated = true
		}
	}
	if regenerated {
		c.Detail = "skipped: some versions were regenerated, so history reflects rules changes too"
	}
	for k := 1; k <= len(ins) && c.OK && !regenerated; k++ {
		prefix, err := Derive(ctx, s.Repos, ins[:k])
		if err != nil {
			return nil, err
		}
		for _, id := range prefix.MemberIDs() {
			var at *eligibilityv1.MemberVersion
			for _, v := range byMember[id] {
				if v.GetInputSequence() <= int64(k) {
					at = v
				}
			}
			if at == nil || !sameMember(at.GetMember(), prefix.Members[id]) {
				c.OK, c.Detail = false, fmt.Sprintf("after input %d, %s's recorded version differs from the replay", k, id)
				break
			}
		}
	}
	if c.OK && !regenerated {
		c.Detail = fmt.Sprintf("replayed the log %d times, once per prefix", len(ins))
	}
	out = append(out, c)
	return out, nil
}
