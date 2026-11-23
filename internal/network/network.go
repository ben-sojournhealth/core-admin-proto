// Package network implements the Network domain's service
// (proto/coreadmin/network/v1/network_service.proto): what other domains
// may ask it about providers, where they stand in a network, and contracts. Other
// domains go through the service, never the Network domain's tables.
package network

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "coreadmin/gen/coreadmin/common/v1"
	networkv1 "coreadmin/gen/coreadmin/network/v1"
	"coreadmin/gen/coreadmin/network/v1/networkv1connect"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// API is how other domains call the Network domain.
type API = networkv1connect.NetworkServiceClient

// NotFound reports whether err is the service's NOT_FOUND.
func NotFound(err error) bool { return connect.CodeOf(err) == connect.CodeNotFound }

// New is the service over the Network domain's tables.
func New(r *repo.Repos) *Service { return &Service{r: r} }

// Service implements NetworkService over the Network domain's tables.
type Service struct{ r *repo.Repos }

var (
	_ API                                    = (*Service)(nil)
	_ networkv1connect.NetworkServiceHandler = (*Service)(nil)
)

func (s *Service) GetProvider(ctx context.Context, req *networkv1.GetProviderRequest) (*networkv1.GetProviderResponse, error) {
	res := &networkv1.GetProviderResponse{}
	if req.GetNpi() == "" {
		return res, nil
	}
	ps, _, err := s.r.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{PageSize: 1, Where: []store.Cond{store.Eq(req.GetNpi(), "npi")}})
	if err != nil || len(ps) == 0 {
		return res, err
	}
	res.Provider = ps[0]
	for _, np := range res.Provider.GetParticipations() {
		if (req.GetTin() == "" || np.GetTin() == req.GetTin()) && dates.In(req.GetOn(), np.GetEffective()) {
			res.Participations = append(res.Participations, np)
		}
	}
	if req.GetNetworkId() == "" {
		return res, nil
	}
	n, err := s.r.Networks.Get(ctx, req.GetNetworkId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	} else if err != nil {
		return nil, err
	}
	res.TierId, res.Participation = place(n, res.Participations, req.GetOn())
	return res, nil
}

// place says where a provider with these participations stands in a
// network on a date: the network's components are tried in order, and the
// first one the provider participates in gives the tier and the
// participation. Neither if the provider is in none of them.
func place(n *providerv1.Network, parts []*providerv1.NetworkParticipation, on *commonv1.Date) (string, *providerv1.NetworkParticipation) {
	for _, c := range n.GetComponents() {
		if !dates.In(on, c.GetEffective()) {
			continue
		}
		for _, np := range parts {
			if np.GetUnderlyingNetworkId() == c.GetUnderlyingNetworkId() {
				return c.GetTierId(), np
			}
		}
	}
	return "", nil
}

func (s *Service) GetContract(ctx context.Context, req *networkv1.GetContractRequest) (*networkv1.GetContractResponse, error) {
	c, err := s.r.Contracts.Get(ctx, req.GetContractId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, err)
	} else if err != nil {
		return nil, err
	}
	return &networkv1.GetContractResponse{Contract: c}, nil
}
