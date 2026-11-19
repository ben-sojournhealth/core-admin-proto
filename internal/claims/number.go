package claims

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/store"
)

// DisplayNumber is the claim number as people see it: the bare number for
// the original, with a version suffix from 2 on (26075-E-00012-2).
func DisplayNumber(c *claimsv1.Claim) string {
	if c.GetClaimNumber() == "" {
		return ""
	}
	if c.GetClaimVersion() <= 1 {
		return c.GetClaimNumber()
	}
	return fmt.Sprintf("%s-%d", c.GetClaimNumber(), c.GetClaimVersion())
}

// Label is how this service names a claim to people.
func (s *Service) Label(c *claimsv1.Claim) string { return s.ref(c) }

// ref is how this service names a claim in its log: the demo's Ref if it
// set one, else the claim number, else the internal id.
func (s *Service) ref(c *claimsv1.Claim) string {
	if s.Ref != nil {
		return s.Ref(c)
	}
	if n := DisplayNumber(c); n != "" {
		return n
	}
	return c.GetId()
}

func sourceLetter(src claimsv1.ClaimSource) string {
	switch src {
	case claimsv1.ClaimSource_CLAIM_SOURCE_EDI_837:
		return "E"
	case claimsv1.ClaimSource_CLAIM_SOURCE_PAPER:
		return "P"
	}
	return "A"
}

// assignNumber gives a claim the next number for its receipt day and
// source. Like a database sequence, a number is handed out once even if
// the claim is never saved. The sequence is seeded by scanning existing
// claims, which is fine for the in-memory store; a real store would keep
// a per-day counter.
func (s *Service) assignNumber(ctx context.Context, c *claimsv1.Claim) error {
	t := c.GetReceivedTime().AsTime()
	prefix := fmt.Sprintf("%02d%03d-%s-", t.Year()%100, t.YearDay(), sourceLetter(c.GetSource()))
	same, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 100_000, Filter: func(o *claimsv1.Claim) bool {
		return strings.HasPrefix(o.GetClaimNumber(), prefix)
	}})
	if err != nil {
		return err
	}
	next := s.issued[prefix] + 1
	for _, o := range same {
		if n, err := strconv.Atoi(strings.TrimPrefix(o.GetClaimNumber(), prefix)); err == nil && n >= next {
			next = n + 1
		}
	}
	if s.issued == nil {
		s.issued = map[string]int{}
	}
	s.issued[prefix] = next
	c.ClaimNumber = fmt.Sprintf("%s%05d", prefix, next)
	c.ClaimVersion = 1
	return nil
}

// nextVersion numbers a replacement or void of orig: same claim number,
// one past the highest version of that number so far.
func (s *Service) nextVersion(ctx context.Context, orig, c *claimsv1.Claim) error {
	versions, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1000, Filter: func(o *claimsv1.Claim) bool {
		return o.GetClaimNumber() == orig.GetClaimNumber()
	}})
	if err != nil {
		return err
	}
	top := orig.GetClaimVersion()
	for _, o := range versions {
		top = max(top, o.GetClaimVersion())
	}
	c.ClaimNumber, c.ClaimVersion = orig.GetClaimNumber(), max(top, 1)+1
	return nil
}
