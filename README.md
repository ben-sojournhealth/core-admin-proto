# core-admin-proto

A prototype core administration system for a health plan payer and TPA. It covers customers and plan design, eligibility, provider data, claims adjudication, stop-loss and file ingestion.

## Layout

```
proto/coreadmin/<domain>/v1/   Data models (<domain>.proto) and APIs (<domain>_service.proto)
gen/                           Generated Go + Connect code, and descriptors.binpb with the .proto comments (committed; `make gen`)
internal/store/                Store[T] and Transactor; the versioned, insert-only Postgres implementation
internal/repo/                 One store per resource type; Tables: the Postgres tables and their access-pattern indexes
internal/pgdb/                 The local Postgres everything runs on
internal/adjudication/         Claims pipeline interfaces (eligibility → pricing → cost share)
internal/stoploss/             Stop-loss tracking interfaces
internal/claims/               ClaimsService operations (process, reprocess, adjust, void, holds, flags)
internal/demos/                Demo catalog and how demos map onto the architecture diagram
internal/journey/              Journey sandbox: the two-week workload and the claim detail view
internal/claimsintake/         837P claims intake: maps files to claims, writes the 999, sample files
internal/enrollmentintake/     834 enrollment intake: maps member loops to EnrollmentTransactions, writes the 999, sample files
internal/eligibility/          Eligibility derived from an append-only input log; every member version kept; regeneration; Verify
internal/eligibilitydemo/      Eligibility sandboxes and the walkthrough's live examples
internal/x12ack/               999 acknowledgments, shared by every X12 intake
internal/protodoc/             The .proto comments at run time, for field tooltips in the demos
internal/x12/                  X12 reader/writer (envelopes and segments, no transaction knowledge)
internal/remittance/           835s from adjudicated claims: build, balance, reverse, render; previews
internal/remittancedemo/       Remittance demo: samples that adjudicate claims and build their 835
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

It runs its own sandbox (`internal/journey`), pre-loaded with two weeks of claims across four families so the queues start with work in them. Claims Intake loads files into the same sandbox. Reset returns to that starting point.

### Remittance

`/remittance/`: from adjudication result to 835. Each sample adjudicates its claims on a fresh sandbox and builds the remittance: a copay, a preventive visit, everything to the deductible, two lines, a denial and a partial denial, a batch of claims on one payment, a corrected claim and a void reversing what an earlier 835 reported, and a void with nothing to net against. The adjudication results sit beside the 835; hovering a service line lights up its segments. Payments go by ACH or check. Nothing is kept between runs.

### Enrollment Intake

`/enrollment-intake/`: 834 files from employers, the enrollment counterpart of Claims Intake. Pick a sample (a new hire with family, a newborn, a termination and a retroactive one, an address change, COBRA continuation, a full file, an unrecognized maintenance type, a bad set, a bad envelope), edit it or paste your own, and see the mapped records live, the 999 per transaction set, and the raw file with each member's segments.

Hovering a field of the mapped record explains it: the tooltips are the comments in `enrollment.proto`, served from a descriptor set `make gen` builds, and a test fails if any field lacks one.

Intake captures each member loop (INS) as an `EnrollmentTransaction`, exactly as sent: every DTP and REF with its qualifier, and any segment it doesn't map kept raw in `unmapped_segments`. The small, fixed code sets eligibility acts on (purpose, action, maintenance type, benefit status) are enums with two empty states: UNSPECIFIED when the code wasn't sent, UNKNOWN when it was sent but isn't one intake recognizes. An unknown code is kept as sent in `unrecognized_codes` and doesn't fail the 999. Longer code lists (relationship, reason, coverage level) stay as sent. It looks nothing up. Which customer, member and plan a loop means, and what it changes, is for eligibility to decide when it applies the loop; for now loops wait in an inbox. The demo has its own sandbox.

### Eligibility

`/eligibility/`: members as derived from every 834 loaded in Enrollment Intake (the two demos share a sandbox with two clients, Acme and Brightline, each starting from an open-enrollment full file, and one COBRA vendor serving both).

Eligibility is derived, never edited:
- **Inputs.** Every 834 member loop, and every approval of full-file terminations, is an `EligibilityInput`, recorded in sequence and never changed.
- **Assertions.** Each input asserts something about a member for a range of dates ("covered on the PPO, family tier, from Apr 1"; "not covered after Jan 31"). For any date, the latest-recorded assertion wins, so a retro term, a reinstatement and a corrected date are a series of assertions, not edits.
- **Versions.** Whenever a derived member changes, a `MemberVersion` is saved with when it was recorded and the input that caused it. The latest version recorded at or before a time is what we believed then. A full file that only confirms what's on file creates no versions.
- **Senders.** Each customer has a list of who may send its enrollment and as what (`internal/eligibility/senders.go`): its employer (HR system) and, if it uses one, a COBRA administrator; a customer without one runs COBRA on its own 834s. One COBRA vendor can serve many customers, and customers can use different vendors. A file from a sender that isn't set up is an exception; a COBRA administrator sends only COBRA; an employer can't enroll COBRA when a vendor runs it; and an employer termination doesn't end coverage the COBRA administrator established.
- **Full files.** Members a full file leaves out are proposed for termination and change only when someone approves. A full file speaks only for the members its sender established, so the employer's file doesn't reconcile the vendor's continuees, and the vendor's file doesn't touch employees.
- **Regeneration.** Replaying every input under the current rules reproduces the members; a difference becomes new versions recorded now, never a rewrite of old ones. Member ids and card numbers depend only on the inputs, so replays agree, even into an empty database. `Service.Compare` replays under other rules (e.g. `BeforeSenderAuthority`) to show who a rules change would affect before adopting it.

**Walkthrough** at `/eligibility/how.html`: a slide deck explaining the approach (two kinds of time, assertions, full files, senders, regeneration). Every example in it runs live on real 834 samples in a fresh sandbox (`internal/eligibilitydemo`), and its claims are pinned by a test.

**Checks** (`Service.Verify`): the input log has no gaps; every version names its cause; the latest version is what's on file; replaying every input reproduces the members; and, for every input, replaying only the inputs up to it reproduces exactly the versions recorded at that moment. The test suite runs them after sending every sample in 12 random orders, and checks they catch a member edited behind eligibility's back.

Claims read the current members, as before. Nothing adjusts claims when eligibility changes retroactively; that's decided by people for now. `internal/eligibility/schema.sql` sketches the Postgres tables.

### Claims Intake

`/claims-intake/`: 837P files from the clearinghouse. Pick a sample (or edit one, or paste your own), send it, and see the 999 verdict per transaction set, each claim as intake mapped it (as YAML), the raw file with each claim's segments, and the 999 we return. The page stops where intake does: it doesn't show adjudication results. Claims load into Journey's sandbox, and each loaded claim links to Journey to see how it adjudicated.

Intake runs once per file and only reads. It copies the submitted fields onto the claim exactly as sent and looks nothing up. Everything that's matched to our data happens in adjudication, which reruns on every reprocess:

- **999.** Rejects only what can't be read reliably: envelope errors (SE count, control numbers), missing required segments or elements, bad codes. A rejected transaction set loads nothing; other sets in the file still load. A file whose ISA13 we've already loaded from that sender is a duplicate and loads nothing.
- **Every readable claim is saved and visible to examiners**, even when nothing matches. The clearinghouse received date is BHT04/05 (one clearinghouse for now; no per-clearinghouse configuration).
- **Adjudication matches on every run, from the submitted fields only.** Member: by the 837 member id, or for a dependent sent in the 2000C patient loop, by first name and birth date in that member's household. Original (frequency 7/8): by `original_link` if set, otherwise by the quoted REF*F8 resolved to the claim's current version. What a run matched is recorded on that run's `Adjudication` (and copied to the claim's output-only `member_id`, `customer_id`, `original_claim_id`), never written into the submitted fields. So a rerun after enrollment catches up, or after an examiner links an original, matches again.
- **Claim numbers are assigned on receipt and never change.** Intake reserves a number for every claim loop in a file before loading it (see Storage). A provider's corrected claim or void gets its own number and links to its original by what adjudication matched, on its first run or later; Journey shows the relationship as a badge: "Corrects 26063-A-00001-2", or "Correction · original not linked" until it's matched. Journey's "Other versions" follows the links, so one claim's history can span numbers.
- **Unmatched pends.** No member: `MEMBER_NOT_FOUND`. No original: `ORIGINAL_NOT_FOUND`. An original that's still pended, in an unreleased payment run, or already being replaced: the claim pends until that settles, and reprocessing finishes it (including finalizing a pended void).

### Claims Lifecycle

A single page for making things happen to the system and watching its state change: process claims, clear pends, adjust and void, run and release payment runs, simulate bank events, change eligibility and fee schedule data. It shows the family's accumulator record (with its revision), every claim's line-by-line decision and stored protojson, and a plain-language event log. State is in Postgres (schema `lifecycle`); Reset starts over.

The left panel is the scenario library (`internal/scenario/library.go`). Each scenario is a script of actions with the outcome expected after every step, so it works as a guided walkthrough in the UI and as a test (`go test ./internal/scenario`). To add an edge case, add a `Scenario` there.

What is real and what is simplified:

- Real: the adjudication engine (`adjudication.DefaultEngine`), the accumulator ledger with its revision check, and the claims operations. Professional claims; no coordination of benefits, benefit limits or bundling.
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
- **Transactions.** `Repos.InTx` runs work across every store as one transaction: reads see its own writes, and everything commits together or not at all. Each claim operation is one transaction, so receiving, reprocessing, adjusting or voiding a claim saves the claim, its accumulator postings, the original's retirement and any review flags together. Payment run selection and release are one transaction per run. A commit fails with `ErrConflict` if a record it wrote was changed by someone else first; adjudication then recalculates from the start. Activity log lines from a transaction appear only once it commits. `Store.Lock` claims a record for the rest of a transaction without changing it (Postgres's `SELECT … FOR UPDATE`). Adjudication locks a replacement's or void's original before checking it, so two corrections of the same claim in flight at once can't both become its open replacement: the second to commit conflicts, recalculates and sees the first. The in-memory database (`store.MemoryDB`) buffers writes and checks revisions and locks at commit; the Postgres store will do the same with a real transaction.
- **Remittance.** An 835 (`remittance.v1.Remittance`) covers one payment to one payee from one customer's plan, since a self-funded plan pays from the employer's own account. It is built from each line's adjudication, whose CAS adjustments already balance, and checked against the 835 balancing rules before it exists. It is stored as sent: when a claim is replaced or voided, the reversal (CLP02 22) repeats what the provider was told with the signs flipped, rather than recomputing it, and the payment is the net. A preview is the same build with a placeholder check, for looking at before a claim is finalized; it is never stored. CARCs and remark codes are decided in adjudication, from one table of denial reporting; a CARC that needs a remark code (96 needs N130) without one pends the line (`REMARK_CODE_REQUIRED`) until a rerun gets it right, and the 835 check refuses it too. Not built yet: recoveries that exceed the payment (PLB), and wiring into payment runs.
- **Lint.** `buf lint` runs with the STANDARD rules. Packages are versioned as `coreadmin.<domain>.v1`.

## Storage

Everything runs on Postgres: the demo, the tests, and the throwaway sandboxes the Remittance demo and the walkthrough build per request. There is no in-memory store. One local Postgres serves them all (`internal/pgdb`): an embedded one, data in `./data/pg`, port 5433. `make pg` starts it, and the demo or a test run starts it if it isn't running; it keeps running until `make pg-stop`. The demo uses the database `coreadmin`, with a schema per long-lived sandbox (`lifecycle`, `journey`, `enrollment`, recreated on start and on Reset) and a throwaway `s_*` schema per request. Tests use `coreadmin_test`, with a fresh schema per test, dropped when it ends (`repo/repotest`).

**Nothing is overwritten.** Every table is versioned: `id`, `revision`, `tx_from`, `tx_to`, `recorded_by`, `data jsonb`. A write inserts a row; the only UPDATE closes the version it replaces (sets `tx_to`); a delete closes the current version without a successor. An exclusion constraint makes the database refuse two versions of a record valid at the same moment, so every table is its own audit log, and `Store.Versions` reads a record's history. Reads see current versions. Transactions are Postgres transactions at read committed: a write names the revision it read, so of two concurrent writes the second fails with a conflict and is retried, and `Lock` is `SELECT ... FOR UPDATE`. Each table's business keys (one current record per key) are still to be defined.

**Access patterns.** Lookups name the fields they depend on with `store.Eq`, `store.Prefix` or `store.Contains` (a `Where` on the list query); anything else is a Go filter on top. Postgres answers the `Where` from a partial index on current versions, on the same expression over `data`. Every index is listed, with the lookup it serves, in `repo.Tables`, and a test loads realistic data and checks Postgres's plan for each lookup uses its index.

**Claim numbers** are `YYJJJ-S-NNNNN` and are never reused. Intake reserves a block per 837 file, one number per claim loop in file order, rejected sets included, so a number with no claim always means a claim loop that didn't load; the job records the block. A claim arriving alone (API, portal, paper) reserves a block of one. Reservations are rows in `claim_number_blocks`, inserted and never changed, with an exclusion constraint against overlapping blocks. A provider's corrected claim or void is a new submission with its own number, linked to its original by what adjudication matched; only adjustments and voids we make are versions of an existing number (`-2`, `-3`).

## Usage

Requires Go and [buf](https://buf.build). The codegen plugins are pinned as Go tools in `go.mod`.

```
make gen     # regenerate gen/ from proto/
make lint    # buf lint + go vet
make test
make check   # lint, regenerate, test, and fail if gen/ is stale
```
