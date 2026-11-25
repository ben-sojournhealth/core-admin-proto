// Package servicedefs is the ServiceDefinitionService: named groups of
// services ("Advanced imaging", "Emergency room") defined by the codes a
// claim line carries, and the answer to which groups a line falls in.
//
// Nothing refers to a definition yet: plans still carry their own service
// categories. This is where plan benefits, pricing contracts and claim
// edits will ask.
package servicedefs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	"coreadmin/gen/coreadmin/claims/v1/claimsv1connect"
	"coreadmin/internal/common/dates"
	"coreadmin/internal/common/store"
)

// Service implements the ServiceDefinitionService over its store.
type Service struct {
	defs store.Store[*claimsv1.ServiceDefinition]
}

var _ claimsv1connect.ServiceDefinitionServiceHandler = (*Service)(nil)

// New returns the service over the Claims domain's definitions store.
func New(defs store.Store[*claimsv1.ServiceDefinition]) *Service {
	return &Service{defs: defs}
}

func invalidf(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

// all reads every definition, ordered by id.
func (s *Service) all(ctx context.Context) ([]*claimsv1.ServiceDefinition, error) {
	defs, _, err := s.defs.List(ctx, store.ListOptions[*claimsv1.ServiceDefinition]{PageSize: 10_000})
	return defs, err
}

func (s *Service) ListServiceDefinitions(ctx context.Context, req *claimsv1.ListServiceDefinitionsRequest) (*claimsv1.ListServiceDefinitionsResponse, error) {
	if req.GetCode() != "" && req.GetSystem() == claimsv1.CodeSystem_CODE_SYSTEM_UNSPECIFIED {
		return nil, invalidf("a code needs its code system")
	}
	defs, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	res := &claimsv1.ListServiceDefinitionsResponse{}
	for _, d := range defs {
		if req.GetCode() == "" || lists(d, req.GetSystem(), req.GetCode()) {
			res.Definitions = append(res.Definitions, d)
		}
	}
	return res, nil
}

// lists reports whether d has an entry of the system containing code, on
// any date.
func lists(d *claimsv1.ServiceDefinition, system claimsv1.CodeSystem, code string) bool {
	for _, e := range d.GetEntries() {
		if e.GetSystem() == system && contains(e, code) {
			return true
		}
	}
	return false
}

func (s *Service) GetServiceDefinition(ctx context.Context, req *claimsv1.GetServiceDefinitionRequest) (*claimsv1.GetServiceDefinitionResponse, error) {
	d, err := s.defs.Get(ctx, req.GetId())
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no service definition %q", req.GetId()))
	} else if err != nil {
		return nil, err
	}
	if on := req.GetEffectiveOn(); on != nil {
		var kept []*claimsv1.CodeEntry
		for _, e := range d.GetEntries() {
			if dates.In(on, e.GetEffective()) {
				kept = append(kept, e)
			}
		}
		d.Entries = kept
	}
	return &claimsv1.GetServiceDefinitionResponse{Definition: d}, nil
}

func (s *Service) CreateServiceDefinition(ctx context.Context, req *claimsv1.CreateServiceDefinitionRequest) (*claimsv1.CreateServiceDefinitionResponse, error) {
	d, err := normalize(req.GetDefinition())
	if err != nil {
		return nil, err
	}
	d.Audit = nil
	out, err := s.defs.Create(ctx, d)
	if errors.Is(err, store.ErrAlreadyExists) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("service definition %q already exists", d.GetId()))
	} else if err != nil {
		return nil, err
	}
	return &claimsv1.CreateServiceDefinitionResponse{Definition: out}, nil
}

func (s *Service) UpdateServiceDefinition(ctx context.Context, req *claimsv1.UpdateServiceDefinitionRequest) (*claimsv1.UpdateServiceDefinitionResponse, error) {
	d, err := normalize(req.GetDefinition())
	if err != nil {
		return nil, err
	}
	if d.GetAudit().GetRevision() == 0 {
		return nil, invalidf("the definition must carry the revision it was read at")
	}
	out, err := s.defs.Update(ctx, d)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no service definition %q", d.GetId()))
	case errors.Is(err, store.ErrConflict):
		return nil, connect.NewError(connect.CodeAborted, fmt.Errorf("service definition %q was changed since revision %d was read", d.GetId(), d.GetAudit().GetRevision()))
	case err != nil:
		return nil, err
	}
	return &claimsv1.UpdateServiceDefinitionResponse{Definition: out}, nil
}

func (s *Service) ListServiceDefinitionRevisions(ctx context.Context, req *claimsv1.ListServiceDefinitionRevisionsRequest) (*claimsv1.ListServiceDefinitionRevisionsResponse, error) {
	versions, err := s.defs.Versions(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no service definition %q", req.GetId()))
	}
	res := &claimsv1.ListServiceDefinitionRevisionsResponse{}
	for _, v := range slices.Backward(versions) {
		res.Revisions = append(res.Revisions, v.Record)
	}
	return res, nil
}

var slug = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// normalize checks a definition sent for storing and returns a tidied
// copy: codes trimmed and upper-cased, a single code's end filled in.
func normalize(in *claimsv1.ServiceDefinition) (*claimsv1.ServiceDefinition, error) {
	if in == nil {
		return nil, invalidf("no definition")
	}
	d := proto.Clone(in).(*claimsv1.ServiceDefinition)
	d.Name = strings.TrimSpace(d.GetName())
	d.Intent = strings.TrimSpace(d.GetIntent())
	if !slug.MatchString(d.GetId()) {
		return nil, invalidf("id %q: use lowercase letters, digits and hyphens", d.GetId())
	}
	if d.GetName() == "" {
		return nil, invalidf("a definition needs a name")
	}
	for i, e := range d.GetEntries() {
		if _, ok := claimsv1.CodeSystem_name[int32(e.GetSystem())]; !ok || e.GetSystem() == claimsv1.CodeSystem_CODE_SYSTEM_UNSPECIFIED {
			return nil, invalidf("entry %d: no code system", i+1)
		}
		e.Start = strings.ToUpper(strings.TrimSpace(e.GetStart()))
		e.End = strings.ToUpper(strings.TrimSpace(e.GetEnd()))
		if e.GetStart() == "" {
			return nil, invalidf("entry %d: no code", i+1)
		}
		if e.GetEnd() == "" {
			e.End = e.GetStart()
		}
		// Ranges compare as text, which only orders codes of one length.
		if len(e.GetEnd()) != len(e.GetStart()) || e.GetEnd() < e.GetStart() {
			return nil, invalidf("entry %d: %s to %s is not a range", i+1, e.GetStart(), e.GetEnd())
		}
		eff := e.GetEffective()
		if eff.GetStart() != nil && eff.GetEnd() != nil && dates.Cmp(eff.GetEnd(), eff.GetStart()) < 0 {
			return nil, invalidf("entry %d (%s): it ends before it starts", i+1, e.GetStart())
		}
		if eff.GetStart() == nil && eff.GetEnd() == nil {
			e.Effective = nil
		}
	}
	return d, nil
}

func contains(e *claimsv1.CodeEntry, code string) bool {
	return len(code) == len(e.GetStart()) && code >= e.GetStart() && code <= e.GetEnd()
}

func (s *Service) MatchServiceDefinitions(ctx context.Context, req *claimsv1.MatchServiceDefinitionsRequest) (*claimsv1.MatchServiceDefinitionsResponse, error) {
	if req.GetDateOfService() == nil {
		return nil, invalidf("a date of service is required: membership depends on it")
	}
	defs, err := s.all(ctx)
	if err != nil {
		return nil, err
	}
	res := &claimsv1.MatchServiceDefinitionsResponse{}
	for _, d := range defs {
		if matched := Match(d, req); matched != nil {
			res.Matches = append(res.Matches, &claimsv1.ServiceDefinitionMatch{Definition: d, MatchedEntries: matched})
		}
	}
	return res, nil
}

// Match returns the entries that put a line in d on its date of service,
// or nil if it is not in d. For every code system d has entries in effect
// for on that date, the line's code of that system must be in one of
// them; a definition with no entries in effect matches nothing.
func Match(d *claimsv1.ServiceDefinition, line *claimsv1.MatchServiceDefinitionsRequest) []*claimsv1.CodeEntry {
	checked := map[claimsv1.CodeSystem]bool{}
	hit := map[claimsv1.CodeSystem]bool{}
	var matched []*claimsv1.CodeEntry
	for _, e := range d.GetEntries() {
		if !dates.In(line.GetDateOfService(), e.GetEffective()) {
			continue
		}
		checked[e.GetSystem()] = true
		if c := code(line, e.GetSystem()); c != "" && contains(e, c) {
			hit[e.GetSystem()] = true
			matched = append(matched, e)
		}
	}
	if len(checked) == 0 || len(hit) != len(checked) {
		return nil
	}
	return matched
}

// code is the line's code of one system, as a definition stores it; ""
// if the line has none.
func code(line *claimsv1.MatchServiceDefinitionsRequest, system claimsv1.CodeSystem) string {
	raw := ""
	switch system {
	case claimsv1.CodeSystem_CODE_SYSTEM_PROCEDURE:
		raw = line.GetProcedureCode()
	case claimsv1.CodeSystem_CODE_SYSTEM_REVENUE:
		raw = line.GetRevenueCode()
	case claimsv1.CodeSystem_CODE_SYSTEM_PLACE_OF_SERVICE:
		raw = line.GetPlaceOfServiceCode()
	}
	return strings.ToUpper(strings.TrimSpace(raw))
}
