package claims

import (
	"context"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/internal/ids"
)

// Receive takes in a claim exactly as submitted, e.g. mapped from an 837.
// It looks nothing up: matching the member, provider and (for a
// replacement or void) the original happens in adjudication, which runs
// here for the first time and again on every reprocess. Every claim is
// saved, numbered and visible to examiners, however little of it matches;
// what doesn't match pends.
func (s *Service) Receive(ctx context.Context, in *claimsv1.Claim) (*claimsv1.Claim, error) {
	c := proto.Clone(in).(*claimsv1.Claim)
	if c.GetId() == "" {
		c.Id = ids.NewV7(s.Now())
	}
	s.prepareNew(c)
	return s.register(ctx, c, "")
}
