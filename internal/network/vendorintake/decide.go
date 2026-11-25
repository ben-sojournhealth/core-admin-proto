package vendorintake

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	ingestionv1 "coreadmin/gen/coreadmin/ingestion/v1"
	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/ids"
	"coreadmin/internal/common/store"
)

// Approve applies every change of a proposed update, all or nothing:
// added pairs become participations, changed ones are derived again from
// their new rows, and dropped ones are removed for every date. A provider
// record is created for an NPI we don't have. The actor (store.WithActor)
// is recorded as who decided.
func (p *Processor) Approve(ctx context.Context, updateID string) (*providerv1.ProviderUpdate, error) {
	return p.decide(ctx, updateID, providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_APPROVED)
}

// Reject closes a proposed update without applying anything. The source's
// next file is compared with its last approved one.
func (p *Processor) Reject(ctx context.Context, updateID string) (*providerv1.ProviderUpdate, error) {
	return p.decide(ctx, updateID, providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_REJECTED)
}

func (p *Processor) decide(ctx context.Context, updateID string, to providerv1.ProviderUpdateStatus) (*providerv1.ProviderUpdate, error) {
	var u *providerv1.ProviderUpdate
	err := p.Repos.DB.InTx(ctx, func(ctx context.Context) error {
		if err := p.Repos.ProviderUpdates.Lock(ctx, updateID); err != nil {
			return err
		}
		var err error
		if u, err = p.Repos.ProviderUpdates.Get(ctx, updateID); err != nil {
			return err
		}
		if u.GetStatus() != providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_PROPOSED {
			return fmt.Errorf("vendorintake: update %s is already %s", updateID, u.GetStatus())
		}
		job, err := p.Repos.VendorIngestionJobs.Get(ctx, u.GetIngestionJobId())
		if err != nil {
			return err
		}
		if to == providerv1.ProviderUpdateStatus_PROVIDER_UPDATE_STATUS_APPROVED {
			src, ok := SourceByID(u.GetSourceId())
			if !ok {
				return fmt.Errorf("vendorintake: no source %q", u.GetSourceId())
			}
			if err := p.apply(ctx, src, u); err != nil {
				return err
			}
			s := u.GetSummary()
			job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_COMPLETED
			job.Counts.RecordsCreated, job.Counts.RecordsUpdated = s.GetAdded(), s.GetChanged()
			job.Counts.RecordsTerminated, job.Counts.RecordsUnchanged = s.GetRemoved(), s.GetUnchanged()
		} else {
			job.Status = ingestionv1.IngestionStatus_INGESTION_STATUS_FAILED
			job.Errors = append(job.Errors, &ingestionv1.RecordError{Code: "REJECTED", Message: "the update was rejected by " + store.Actor(ctx) + "; nothing was applied"})
		}
		job.CompletedTime = timestamppb.New(p.Now())
		if _, err := p.Repos.VendorIngestionJobs.Update(ctx, job); err != nil {
			return err
		}
		u.Status, u.DecidedBy, u.DecidedTime = to, store.Actor(ctx), timestamppb.New(p.Now())
		u, err = p.Repos.ProviderUpdates.Update(ctx, u)
		return err
	})
	return u, err
}

// apply writes an update's changes to participations and providers, a
// batch at a time.
func (p *Processor) apply(ctx context.Context, src Source, u *providerv1.ProviderUpdate) error {
	token := ""
	for {
		changes, next, err := p.Repos.VendorProviderChanges.List(ctx, store.ListOptions[*providerv1.VendorProviderChange]{
			PageSize: batch, PageToken: token, Where: []store.Cond{store.Eq(u.GetId(), "providerUpdateId")},
		})
		if err != nil {
			return err
		}
		var added []*providerv1.NetworkParticipation
		for _, ch := range changes {
			derived := Derive(src, ch.GetNpi(), ch.GetTin(), ch.GetRows())
			derived.ProviderUpdateId, derived.VendorChangeId, derived.SourceHash = u.GetId(), ch.GetId(), ch.GetRowsHash()
			switch ch.GetKind() {
			case providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_ADDED:
				added = append(added, derived)
			case providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_CHANGED:
				cur, err := p.Repos.Participations.Get(ctx, ch.GetParticipationId())
				if err != nil {
					return err
				}
				derived.Audit, derived.ContractId = cur.GetAudit(), cur.GetContractId()
				if _, err := p.Repos.Participations.Update(ctx, derived); err != nil {
					return err
				}
			case providerv1.VendorChangeKind_VENDOR_CHANGE_KIND_DROPPED:
				if err := p.Repos.Participations.Delete(ctx, ch.GetParticipationId()); err != nil {
					return err
				}
			}
		}
		if err := p.Repos.Participations.CreateBatch(ctx, added); err != nil {
			return err
		}
		if err := p.providers(ctx, u, changes); err != nil {
			return err
		}
		if token = next; token == "" {
			return nil
		}
	}
}

// providers creates a provider record for each NPI in the changes we have
// none for, and renames one whose name the file changed.
func (p *Processor) providers(ctx context.Context, u *providerv1.ProviderUpdate, changes []*providerv1.VendorProviderChange) error {
	rows := map[string]*providerv1.VendorProviderChange{}
	renamed := map[string]bool{}
	var npis []string
	for _, ch := range changes {
		if len(ch.GetRows()) == 0 {
			continue
		}
		if rows[ch.GetNpi()] == nil {
			rows[ch.GetNpi()] = ch
			npis = append(npis, ch.GetNpi())
		}
		for _, a := range ch.GetAspects() {
			if a == providerv1.VendorChangeAspect_VENDOR_CHANGE_ASPECT_NAME {
				rows[ch.GetNpi()], renamed[ch.GetNpi()] = ch, true
			}
		}
	}
	if len(npis) == 0 {
		return nil
	}
	have, _, err := p.Repos.Providers.List(ctx, store.ListOptions[*providerv1.Provider]{
		PageSize: 4 * len(npis), Where: []store.Cond{store.In(npis, "npi")},
	})
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, pr := range have {
		if name := nameOf(rows[pr.GetNpi()].GetRows()); !known[pr.GetNpi()] && renamed[pr.GetNpi()] && !proto.Equal(name, pr.GetName()) {
			pr.Name, pr.ProviderUpdateId = name, u.GetId()
			if _, err := p.Repos.Providers.Update(ctx, pr); err != nil {
				return err
			}
		}
		known[pr.GetNpi()] = true
	}
	var missing []*providerv1.Provider
	for _, npi := range npis {
		if known[npi] {
			continue
		}
		missing = append(missing, &providerv1.Provider{
			Id: ids.NewV7(p.Now()), Npi: npi, Kind: providerv1.ProviderKind_PROVIDER_KIND_INDIVIDUAL,
			Name: nameOf(rows[npi].GetRows()), Status: providerv1.ProviderStatus_PROVIDER_STATUS_ACTIVE, ProviderUpdateId: u.GetId(),
		})
	}
	return p.Repos.Providers.CreateBatch(ctx, missing)
}
