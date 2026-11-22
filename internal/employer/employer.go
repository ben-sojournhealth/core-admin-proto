// Package employer implements the Employer domain's service
// (proto/coreadmin/employer/v1/employer_service.proto): what other domains
// may ask it about customers, members, coverage and plan design. Other
// domains go through the service, never the Employer domain's tables.
//
// Domains call it in-process, through API; the same service could be
// served over Connect.
package employer

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	eligibilityv1 "coreadmin/gen/coreadmin/eligibility/v1"
	employerv1 "coreadmin/gen/coreadmin/employer/v1"
	"coreadmin/gen/coreadmin/employer/v1/employerv1connect"
	"coreadmin/internal/dates"
	"coreadmin/internal/repo"
	"coreadmin/internal/store"
)

// API is how other domains call the Employer domain.
type API = employerv1connect.EmployerServiceClient

// NotFound reports whether err is the service's NOT_FOUND.
func NotFound(err error) bool { return connect.CodeOf(err) == connect.CodeNotFound }

// New is the service over the Employer domain's tables.
func New(r *repo.Repos) *Service { return &Service{r: r} }

// Service implements EmployerService over the Employer domain's tables.
type Service struct{ r *repo.Repos }

var (
	_ API                                      = (*Service)(nil)
	_ employerv1connect.EmployerServiceHandler = (*Service)(nil)
)

// status turns a store error into the service's: a missing record is
// NOT_FOUND.
func status(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return connect.NewError(connect.CodeNotFound, err)
	}
	return err
}

func (s *Service) GetCustomer(ctx context.Context, req *employerv1.GetCustomerRequest) (*employerv1.GetCustomerResponse, error) {
	c, err := s.r.Customers.Get(ctx, req.GetCustomerId())
	if err != nil {
		return nil, status(err)
	}
	return &employerv1.GetCustomerResponse{Customer: c}, nil
}

func (s *Service) GetMember(ctx context.Context, req *employerv1.GetMemberRequest) (*employerv1.GetMemberResponse, error) {
	m, err := s.r.Members.Get(ctx, req.GetMemberId())
	if err != nil {
		return nil, status(err)
	}
	return &employerv1.GetMemberResponse{Member: m}, nil
}

func (s *Service) ListMembersByCard(ctx context.Context, req *employerv1.ListMembersByCardRequest) (*employerv1.ListMembersByCardResponse, error) {
	if req.GetCardId() == "" {
		return &employerv1.ListMembersByCardResponse{}, nil
	}
	ms, _, err := s.r.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 2, Where: []store.Cond{store.Eq(req.GetCardId(), "cardId")}})
	if err != nil {
		return nil, err
	}
	return &employerv1.ListMembersByCardResponse{Members: ms}, nil
}

func (s *Service) GetHousehold(ctx context.Context, req *employerv1.GetHouseholdRequest) (*employerv1.GetHouseholdResponse, error) {
	ms, _, err := s.r.Members.List(ctx, store.ListOptions[*eligibilityv1.Member]{PageSize: 50,
		Where:  []store.Cond{store.Eq(req.GetSubscriberId(), "subscriberId")},
		Filter: func(m *eligibilityv1.Member) bool { return m.GetCustomerId() == req.GetCustomerId() },
	})
	if err != nil {
		return nil, err
	}
	return &employerv1.GetHouseholdResponse{Members: ms}, nil
}

func (s *Service) GetMedicalCoverage(ctx context.Context, req *employerv1.GetMedicalCoverageRequest) (*employerv1.GetMedicalCoverageResponse, error) {
	m, err := s.r.Members.Get(ctx, req.GetMemberId())
	if err != nil {
		return nil, status(err)
	}
	on := req.GetOn()
	res := &employerv1.GetMedicalCoverageResponse{Member: m}
	for _, sp := range m.GetCoverage() {
		if sp.GetLine() != eligibilityv1.CoverageLine_COVERAGE_LINE_MEDICAL {
			continue
		}
		if dates.In(on, sp.GetEffective()) && (res.Span == nil || res.Span.GetStatus() == eligibilityv1.CoverageStatus_COVERAGE_STATUS_TERMINATED) {
			res.Span = sp
		}
		if dates.Cmp(sp.GetEffective().GetStart(), on) <= 0 {
			res.StartedBefore = true
		}
	}
	return res, nil
}

func (s *Service) GetPlanVersion(ctx context.Context, req *employerv1.GetPlanVersionRequest) (*employerv1.GetPlanVersionResponse, error) {
	plan, err := s.r.Plans.Get(ctx, req.GetPlanId())
	if err != nil {
		return nil, status(fmt.Errorf("plan %s: %w", req.GetPlanId(), err))
	}
	res := &employerv1.GetPlanVersionResponse{Plan: plan}
	for _, v := range plan.GetVersions() {
		if dates.In(req.GetOn(), v.GetEffective()) {
			res.Version = v
		}
	}
	return res, nil
}
