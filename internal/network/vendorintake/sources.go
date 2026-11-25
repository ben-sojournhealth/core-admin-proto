// Package vendorintake takes in a network vendor's full provider files.
//
// A file belongs to one source (a vendor's feed, e.g. Cigna's PPO
// individual-provider file) and is the source's whole statement, history
// included. Intake keeps each row as sent, compares the file with what
// the source's last approved file said, and proposes the differences as
// one ProviderUpdate. Nothing changes provider data until that update is
// approved as a whole; approving it derives the participations (the NPI
// to TIN roll-up) from the rows.
//
// Files are far larger than memory (20 GB and growing): rows are streamed
// into a working table and compared in Postgres, and only the differences
// are stored.
package vendorintake

import (
	"context"
	"errors"

	providerv1 "coreadmin/gen/coreadmin/provider/v1"
	"coreadmin/internal/common/repo"
	"coreadmin/internal/common/store"
)

// Source is one stream of full files from a vendor. A file speaks only
// for its own source: it can add to, change and remove that source's
// participations and no one else's. Several sources can feed one
// underlying network (a vendor's individual and facility files).
type Source struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// The underlying network its providers participate in.
	UnderlyingNetworkID string `json:"underlyingNetworkId"`
	Layout              Layout `json:"-"`
}

// Layout names the file's columns, by header. Every other column is kept
// as sent in the row's other_columns.
type Layout struct {
	NPI, TIN                    string
	Last, First, Middle, Suffix string
	BillingName                 string
	Specialties                 []string
	AddressLines                []string
	City, State, Zip, Phone     string
	Start, Term                 string
}

// placeholderLayout stands in until we have the vendor's file
// specification: swap the headers here when it arrives.
var placeholderLayout = Layout{
	NPI: "NPI", TIN: "TIN",
	Last: "LAST_NAME", First: "FIRST_NAME", Middle: "MIDDLE_NAME", Suffix: "SUFFIX",
	BillingName:  "GROUP_NAME",
	Specialties:  []string{"SPECIALTY_1", "SPECIALTY_2", "SPECIALTY_3", "SPECIALTY_4"},
	AddressLines: []string{"ADDRESS_1", "ADDRESS_2"},
	City:         "CITY", State: "STATE", Zip: "ZIP", Phone: "PHONE",
	Start: "EFF_DATE", Term: "TERM_DATE",
}

// The underlying networks rented from Cigna. Cigna holds the rates and
// reprices the claims.
const (
	CignaPPO = "net_cigna_ppo"
	CignaOAP = "net_cigna_oap"
)

// Sources are the vendor files we take. The facility files are separate
// sources, not taken yet.
var Sources = []Source{
	{ID: "cigna-ppo-individual", Name: "Cigna PPO · individuals", UnderlyingNetworkID: CignaPPO, Layout: placeholderLayout},
	{ID: "cigna-oap-individual", Name: "Cigna OAP · individuals", UnderlyingNetworkID: CignaOAP, Layout: placeholderLayout},
}

// SourceByID finds a source.
func SourceByID(id string) (Source, bool) {
	for _, s := range Sources {
		if s.ID == id {
			return s, true
		}
	}
	return Source{}, false
}

// Setup creates the underlying networks the sources feed, if missing.
func Setup(ctx context.Context, r *repo.Repos) error {
	for _, n := range []*providerv1.UnderlyingNetwork{
		{Id: CignaPPO, Name: "Cigna PPO", Vendor: "Cigna"},
		{Id: CignaOAP, Name: "Cigna Open Access Plus", Vendor: "Cigna"},
	} {
		n.Kind = providerv1.UnderlyingNetworkKind_UNDERLYING_NETWORK_KIND_RENTED
		n.Pricing = providerv1.UnderlyingNetworkPricing_UNDERLYING_NETWORK_PRICING_VENDOR_REPRICED
		if _, err := r.UnderlyingNetworks.Create(ctx, n); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
			return err
		}
	}
	return nil
}
