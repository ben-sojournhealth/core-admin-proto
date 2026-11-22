// Package financedb declares the Finance domain's tables, in its own
// Postgres schema, and the stores over them. Only Finance code uses them.
package financedb

import (
	stoplossv1 "coreadmin/gen/coreadmin/stoploss/v1"
	"coreadmin/internal/common/store"
)

// Schema is the Finance domain's Postgres schema.
const Schema = "finance"

// Tables are the Finance domain's tables.
var Tables = []store.Table{
	{Schema: Schema, Name: "stop_loss_policies"},
	{Schema: Schema, Name: "specific_exposures"},
	{Schema: Schema, Name: "aggregate_exposures"},
	{Schema: Schema, Name: "stop_loss_filings"},
}

// FinanceStores are the stores over the Finance domain's tables.
type FinanceStores struct {
	StopLossPolicies   store.Store[*stoplossv1.StopLossPolicy]
	SpecificExposures  store.Store[*stoplossv1.SpecificExposure]
	AggregateExposures store.Store[*stoplossv1.AggregateExposure]
	StopLossFilings    store.Store[*stoplossv1.StopLossFiling]
}

// New returns the stores in db, which must have Tables (store.PGDB.Migrate).
func New(db *store.PGDB) FinanceStores {
	return FinanceStores{
		StopLossPolicies:   store.NewPG[*stoplossv1.StopLossPolicy](db, Schema, "stop_loss_policies"),
		SpecificExposures:  store.NewPG[*stoplossv1.SpecificExposure](db, Schema, "specific_exposures"),
		AggregateExposures: store.NewPG[*stoplossv1.AggregateExposure](db, Schema, "aggregate_exposures"),
		StopLossFilings:    store.NewPG[*stoplossv1.StopLossFiling](db, Schema, "stop_loss_filings"),
	}
}
