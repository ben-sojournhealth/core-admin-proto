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
internal/demos/                Demo catalog and how demos map onto the architecture diagram
internal/journey/              Journey sandbox: the two-week workload and the claim detail view
internal/sim/                  Sandbox: seeded data, fixed clock, event log, one Action type
internal/scenario/             Scenario library; each scenario is also a test
internal/seed/                 Loads seed/ fixtures (strict protojson)
seed/                          Sample self-funded customer, plan, family, providers, fee schedules, policy
cmd/demo/                      Demo server: architecture page at /, each demo under its own path
```

## Demos

```
make demo        # then open http://localhost:8080
```

The landing page is an architecture diagram. Components with a demo are solid and clickable; dashed ones have a demo planned. Clicking a component opens its demo filtered to the relevant scenarios (e.g. Accumulators opens `/claims-lifecycle/?group=Accumulators`).

To add a demo: add it to `internal/demos/demos.go` with the components it covers (set `Status: Planned` until it exists), serve it under `/<id>/` in `cmd/demo/main.go`, and put its page in `cmd/demo/web/<id>/`. Components are the node ids in `cmd/demo/web/index.html`.

### Journey

`/journey/`: the claims portal, the examiner's view of the claims system. Work queues (pended claims by reason, review flags, payment holds) and a claim page with the decision, money breakdown, the member's accumulators and the claim's activity. Examiners override judgment pends with a note, reprocess data pends, adjust and void, clear flags and release holds.

It runs its own sandbox (`internal/journey`), pre-loaded with two weeks of claims across four families so the queues start with work in them. Reset returns to that starting point.

### Claims Lifecycle

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
- **Claim identity.** `id` is an internal UUIDv7 (`internal/ids`), assigned at intake and never shown. People see `claim_number`, `YYJJJ-S-NNNNN` (year, day received, source, daily sequence). A replacement or void keeps the original's number with the next version: `26075-E-00012-2`. The Claims Lifecycle sandbox gives originals short ids (`clm_A`) for its scenario scripts and names later versions `clm_A-2`; the sandbox resolves those names to the real ids.
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
