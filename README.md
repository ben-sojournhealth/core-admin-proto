# core-admin-proto

A prototype core administration system for a health plan payer and TPA. It covers customers and plan design, eligibility, provider data, claims adjudication, stop-loss and file ingestion.

## Layout

```
proto/coreadmin/<domain>/v1/   Data models (<domain>.proto) and APIs (<domain>_service.proto)
gen/                           Generated Go + Connect code (committed; `make gen`)
internal/store/                Store[T] interface + in-memory protojson implementation
internal/repo/                 One store per resource type
internal/adjudication/         Claims pipeline interfaces (eligibility → pricing → cost share)
internal/stoploss/             Stop-loss tracking interfaces
internal/claims/               ClaimsService operations (process, reprocess, adjust, void, holds, flags)
internal/sim/                  Sandbox: seeded data, fixed clock, event log, one Action type
internal/scenario/             Scenario library; each scenario is also a test
internal/seed/                 Loads seed/ fixtures (strict protojson)
seed/                          Sample self-funded customer, plan, family, providers, fee schedules, policy
cmd/demo/                      Web UI for the sandbox
```

## Sandbox UI

```
make demo        # then open http://localhost:8080
```

A single page for making things happen to the system and watching its state change: process claims, clear pends, adjust and void, run and release payment runs, simulate bank events, change eligibility and fee schedule data. It shows the family's accumulator record (with its revision), every claim's line-by-line decision and stored protojson, and a plain-language event log. State is in memory; Reset starts over.

The left panel is the scenario library (`internal/scenario/library.go`). Each scenario is a script of actions with the outcome expected after every step, so it works as a guided walkthrough in the UI and as a test (`go test ./internal/scenario`). To add an edge case, add a `Scenario` there.

What is real and what is simplified:

- Real: the adjudication engine (`adjudication.DefaultEngine`), the accumulator ledger with its revision check, and the claims operations. Professional claims with one line; no coordination of benefits, benefit limits or bundling.
- Simplified: the payment system (`internal/claims/payment.go`) only writes payment state back onto claims; remittances and netting are log lines. Eligibility and fee schedule changes are direct edits standing in for file loads.

| Domain | Key resources |
|---|---|
| customer | Customer (tenant / plan sponsor), Group |
| plan | Plan → PlanVersion: network tiers, accumulators, service categories, benefits |
| eligibility | Member, CoverageSpan, FamilyAccumulators (one record per family, plan and benefit period) |
| provider | Provider, Network, Contract, FeeSchedule |
| claims | Claim, ClaimLine, Adjudication, LineAdjudication (pricing, benefit, cost shares, derived CAS), ClaimPayment |
| stoploss | StopLossPolicy (specific, aggregate, lasers, contract basis), exposures, filings |
| ingestion | IngestionJob (834, 837, roster, fee schedule) |

## Claim lifecycle

```
status:         RECEIVED ─► PENDED ─► (ReprocessClaim) ─► APPROVED | DENIED ─► ADJUSTED | VOIDED
payment.state:  SELECTED ─► ISSUED ─► CLEARED | RETURNED | VOIDED
```

- `status` is the decision only. Payment is tracked in `payment` and never changes the status.
- A claim is finalized at `APPROVED` or `DENIED`. Accumulators post then, once, in a single conditional write to the family's accumulator record (no locks; a concurrent claim for the same family recalculates).
- Only `PENDED` claims are reprocessed. A finalized claim changes only through a replacement or void claim that references it (`AdjustClaim`, `VoidClaim`), and not while its payment is `SELECTED`.
- When an adjustment changes accumulator postings, later claims for the same family get a review flag; they are not changed automatically.
- An adjudication result is output only. Nothing reads a previous result; the accumulator ledger, stop-loss and remittance reverse from their own records by claim id.
- Every line result must pass `adjudication.CheckLineBalanced`.

## Conventions

- **Protos are the source of truth.** Persisted documents are the protojson encoding of the top-level message. Fixtures use the canonical lowerCamelCase field names and enum string names.
- **Money** is `common.v1.Money{cents}` (int64). Percentages are integer basis points (`2000` = 20%). There is no floating point anywhere.
- **Dates** use `common.v1.Date`, and every `DateRange` is inclusive at both ends. An unset `end` means open-ended.
- **Tenancy.** Customer-scoped resources carry `customer_id`. Providers and networks are shared across customers.
- **Audit.** Every top-level resource has `audit` (`AuditInfo`). The store stamps it, and `audit.revision` provides optimistic concurrency.
- **Lint.** `buf lint` runs with the STANDARD rules. Packages are versioned as `coreadmin.<domain>.v1`.

## Usage

Requires Go and [buf](https://buf.build). The codegen plugins are pinned as Go tools in `go.mod`.

```
make gen     # regenerate gen/ from proto/
make lint    # buf lint + go vet
make test
make check   # lint, regenerate, test, and fail if gen/ is stale
```
