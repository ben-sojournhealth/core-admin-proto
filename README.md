# core-admin-proto

A prototype core administration system for a health plan payer and TPA. It covers customers and plan design, eligibility, provider data, claims adjudication, stop-loss and file ingestion.

## Layout

```
proto/coreadmin/<pkg>/v1/                Data models (<pkg>.proto), and each domain's service: what other domains may ask it (employer/v1, network/v1, claims/v1 *_service.proto)
gen/                                     Generated Go + Connect code, and descriptors.binpb with the .proto comments (not committed; `make gen`)

internal/employer/                       Employer domain. EmployerService: what other domains may ask it
internal/employer/enrollmentintake/      834 enrollment intake: maps member loops to EnrollmentTransactions, writes the 999, sample files
internal/employer/eligibility/           Eligibility derived from an append-only input log; every member version kept; regeneration; Verify
internal/employer/eligibilitydemo/       Eligibility sandboxes and the walkthrough's live examples

internal/claims/                         Claims domain. ClaimsService operations (process, reprocess, adjust, void, holds, flags)
internal/claims/claimsintake/            837P claims intake: maps files to claims, writes the 999, sample files
internal/claims/adjudication/            Claims pipeline (eligibility → pricing → cost share), pends, denials, remark codes
internal/claims/remittance/              835s from adjudicated claims: build, balance, reverse, render; previews
internal/claims/remittancedemo/          Remittance demo: samples that adjudicate claims and build their 835

internal/network/                        Network domain. NetworkService: what other domains may ask it

internal/finance/stoploss/               Finance domain: stop-loss tracking interfaces

internal/journey/                        Journey (admin portal) sandbox: the two-week workload and the claim detail view
internal/sim/                            Sandbox: seeded data, fixed clock, event log, one Action type
internal/scenario/                       Scenario library; each scenario is also a test
internal/demos/                          Demo catalog, by domain, and how demos map onto the domain map
internal/seed/                           Loads seed/ fixtures (strict protojson)

internal/common/                         Shared by every domain:
internal/common/store/                   Store[T] and Transactor; the versioned, insert-only Postgres implementation
internal/common/repo/                    One store per resource type; Tables: each domain's Postgres tables and their access-pattern indexes
internal/common/pgdb/                    The local Postgres everything runs on
internal/common/x12/                     X12 reader/writer (envelopes and segments, no transaction knowledge)
internal/common/x12ack/                  999 acknowledgments, shared by every X12 intake
internal/common/protodoc/                The .proto comments at run time, for field tooltips in the demos
internal/common/ids/                     UUIDv7 ids
internal/common/dates/                   Date and date-range helpers
seed/                                    Sample self-funded customer, plan, family, providers, fee schedules, policy
cmd/demo/                                Demo server: the domain map at /, each demo under its own path
```

Each domain is a folder under `internal/`. A domain's code uses only its own tables and asks other domains through their services; a test enforces it (demos, named `*demo`, are exempt, like Journey and the simulator).

## Demos

```
make demo        # then open http://localhost:8080
```

The landing page is the domain map. Components with a demo are solid and clickable; dashed ones have a demo planned. Clicking a component opens its demo filtered to the relevant scenarios (e.g. Accumulators opens `/claims-lifecycle/?group=Accumulators`). Lines between domains appear when a component is hovered, or all at once with *Show all connections*. Lines with a protobuf interface are drawn in teal; clicking one opens the service, which methods each caller uses over that line, and the source of every message those calls carry, grouped by protobuf package (`demos.Interfaces` lists them; a test checks every method exists and every domain-service method is on some line). Lines without one (X12 files, interfaces not defined yet) aren't clickable.

To add a demo: add it to its domain's file in `internal/demos/` (`claims.go`, `employer.go`, ...) with the components it covers (set `Status: Planned` until it exists), serve its API from a file of its own in `cmd/demo/` registered in `registrations`, and put its page in `cmd/demo/web/<id>/`. Components are the node ids in `cmd/demo/web/index.html`.

### Journey

`/journey/`: the admin portal, our staff's view of the system; today it covers claims. Work queues (pended claims by reason, review flags, payment holds) and a claim page with the decision, money breakdown, the member's accumulators and the claim's activity. Examiners override judgment pends with a note, reprocess data pends, adjust and void, clear flags and release holds.

It runs its own sandbox (`internal/journey`), pre-loaded with two weeks of claims across four families so the queues start with work in them. Claims Intake loads files into the same sandbox. Reset returns to that starting point.

## Domains

Each domain has a README of its own:

| Domain | | Key resources |
|---|---|---|
| Employer | [internal/employer](internal/employer/README.md) | Customer, Group, Plan → PlanVersion, Member, CoverageSpan, EnrollmentTransaction, EligibilityInput, MemberVersion |
| Claims | [internal/claims](internal/claims/README.md) | Claim, ClaimLine, Adjudication, LineAdjudication, ClaimPayment, FamilyAccumulators, FeeSchedule, Remittance |
| Network | [internal/network](internal/network/README.md) | Provider, Network, UnderlyingNetwork, Contract |
| Finance | [internal/finance](internal/finance/README.md) | StopLossPolicy, exposures, filings |

## Conventions

- **Protos are the source of truth.** Persisted documents are the protojson encoding of the top-level message. Fixtures use the canonical lowerCamelCase field names and enum string names.
- **Money** is `common.v1.Money{cents}` (int64). Percentages are integer basis points (`2000` = 20%). There is no floating point anywhere.
- **Dates** use `common.v1.Date`, and every `DateRange` is inclusive at both ends. An unset `end` means open-ended.
- **Tenancy.** Customer-scoped resources carry `customer_id`. Providers and networks are shared across customers.
- **Audit.** Every top-level resource has `audit` (`AuditInfo`). The store stamps it, and `audit.revision` provides optimistic concurrency.
- **Lint.** `buf lint` runs with the STANDARD rules. Packages are versioned as `coreadmin.<domain>.v1`.

## Storage

Everything runs on Postgres: the demo, the tests, and the throwaway sandboxes the Remittance demo and the walkthrough build per request. There is no in-memory store. One local Postgres serves them all (`internal/common/pgdb`): an embedded one on port 5433, shared by every checkout and worktree, its data outside all of them (`~/Library/Application Support/coreadmin/pg` on macOS). `make pg` starts it, and the demo or a test run starts it if it isn't running; it keeps running until `make pg-stop`. Each checkout has its own databases: `coreadmin` and `coreadmin_test` in the main checkout, `coreadmin_<worktree>` and `coreadmin_test_<worktree>` in a worktree, so demos and tests in different worktrees never touch each other's data. Each domain owns a schema holding its tables, and reads and writes only that schema (each domain declares its tables in its own `<domain>db` package; `repo.Tables` collects them):

- `employer`: customers, groups, plans, members, the 834 ingestion jobs, enrollment transactions, eligibility inputs and member versions
- `claims`: claims, family accumulators, fee schedules, the 837 ingestion jobs, remittances and claim number blocks
- `network`: providers, networks, underlying networks and contracts
- `finance`: stop-loss policies, exposures and filings

No foreign keys cross schemas; a reference to another domain's record is a value. A domain's code asks other domains through their services, defined in proto and called in-process: `EmployerService` (`employer_service.proto`: customers, members, medical coverage on a date and the network it uses, the plan version in force) and `NetworkService` (`network_service.proto`: a provider, its participations and its tier in a network on a date, contracts). It never touches their stores; a test enforces it. The services are generated with Connect in simple mode, so the same service could be served over the network unchanged. Answers use the latest data we have, about the date asked: a retro-termination recorded today changes the answer for a January date of service. Demos, the simulator and seed loading still set up every domain through `repo.Repos`.

A sandbox is a prefix on those schemas (`journey_claims`, `journey_employer`, ...); production has no prefix. The demo uses this checkout's demo database, with long-lived sandboxes `lifecycle`, `journey` and `enrollment` (recreated on start and on Reset) and a throwaway `s_*` sandbox per request. Tests use this checkout's test database, with a fresh sandbox per test, dropped when it ends (`repo/repotest`).

**Nothing is overwritten.** Every table is versioned: `id`, `revision`, `tx_from`, `tx_to`, `recorded_by`, `data jsonb`. A write inserts a row; the only UPDATE closes the version it replaces (sets `tx_to`); a delete closes the current version without a successor. An exclusion constraint makes the database refuse two versions of a record valid at the same moment, so every table is its own audit log, and `Store.Versions` reads a record's history. Reads see current versions. Transactions are Postgres transactions at read committed: a write names the revision it read, so of two concurrent writes the second fails with a conflict and is retried, and `Lock` is `SELECT ... FOR UPDATE`. Each table's business keys (one current record per key) are still to be defined.

**Access patterns.** Lookups name the fields they depend on with `store.Eq`, `store.Prefix` or `store.Contains` (a `Where` on the list query); anything else is a Go filter on top. Postgres answers the `Where` from a partial index on current versions, on the same expression over `data`. Every index is listed, with the lookup it serves, in its domain's `<domain>db.Tables`, and a test loads realistic data and checks Postgres's plan for each lookup uses its index.

## Usage

Requires Go and [buf](https://buf.build). The codegen plugins are pinned as Go tools in `go.mod`. Generated code isn't committed: run `make gen` after cloning, and after changing or merging protos (`make test`, `make demo` and `make check` run it first).

```
make gen     # generate gen/ from proto/
make lint    # buf lint + go vet
make test
make check   # regenerate, lint, test
```

## Worktrees

Each domain can be worked on in its own git worktree:

```
git worktree add ../core-admin-claims -b claims
cd ../core-admin-claims && make gen && make test
go run ./cmd/demo -addr localhost:8081
```

Every worktree shares the one local Postgres but gets its own databases (`coreadmin_<worktree>`, `coreadmin_test_<worktree>`), so demos and tests never touch each other's data. `CLAUDE.md` carries the project's working rules into every worktree. Files every domain shares, and where branches meet: the domain map (`cmd/demo/web/index.html`), `cmd/demo/main.go`'s `registrations`, `internal/demos/interfaces.go`, and `seed/`.
