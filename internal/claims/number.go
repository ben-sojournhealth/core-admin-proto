package claims

import (
	"context"
	"fmt"
	"time"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/common/store"
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
	case claimsv1.ClaimSource_CLAIM_SOURCE_PORTAL:
		return "W"
	}
	// No source given.
	return "X"
}

// NumberPrefix is the YYJJJ-S- of claim numbers for a receipt time and
// source.
func NumberPrefix(received time.Time, src claimsv1.ClaimSource) string {
	return fmt.Sprintf("%02d%03d-%s-", received.Year()%100, received.YearDay(), sourceLetter(src))
}

// FormatNumber is a claim number: the prefix and a five-digit sequence.
func FormatNumber(prefix string, n int) string { return fmt.Sprintf("%s%05d", prefix, n) }

// assignNumber reserves a block of one for a claim that arrived alone
// (portal, paper, or a claim handed over without a number).
func (s *Service) assignNumber(ctx context.Context, c *claimsv1.Claim) error {
	prefix := NumberPrefix(c.GetReceivedTime().AsTime(), c.GetSource())
	ref := c.GetPatientControlNumber()
	if ref == "" {
		ref = c.GetId()
	}
	n, err := s.Repos.ClaimNumbers.Reserve(ctx, prefix, 1, "claim "+ref)
	if err != nil {
		return err
	}
	c.ClaimNumber, c.ClaimVersion = FormatNumber(prefix, n), 1
	return nil
}

// nextVersion numbers a replacement or void of orig: same claim number,
// one past the highest version of that number so far.
func (s *Service) nextVersion(ctx context.Context, orig, c *claimsv1.Claim) error {
	versions, _, err := s.Repos.Claims.List(ctx, store.ListOptions[*claimsv1.Claim]{PageSize: 1000, Where: []store.Cond{store.Eq(orig.GetClaimNumber(), "claimNumber")}, Filter: func(o *claimsv1.Claim) bool {
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
