# core-admin-proto

A prototype payer core administration system for a self-funded TPA: claims, eligibility and enrollment, provider data, plan design, stop-loss. Go, protobuf models stored as protojson in Postgres. README.md has the full picture; each domain folder has its own README.

## Working together

- **Design first.** Discuss design and wait for the go-ahead before changing protos or building. Batch agreed changes.
- **Be precise about payer operations.** State domain claims exactly. When corrected, concede and adjust.
- **Commit only when asked.** If asked, push too only when the request says so.
- **Demo UIs are visual, with little text.** Show state as boxes, bars and badges. Put detail behind a click. Keep summaries short.

## Domains

Each domain is a folder under `internal/` and a Postgres schema of its own:

- **`employer`**: enrollment intake (834), eligibility, plan design, employer portal, billing & funding.
- **`claims`**: claims intake (837), adjudication, service definitions, remittance & EOBs (835), disputes, settlement. Claims also owns the plan *as claims understands it*, accumulators and pricing (fee schedules).
- **`network`**: provider data, vendor intake, provider portal. Out of scope for '28: contracting, credentialing, roster intake.
- **`finance`**: disbursement, stop-loss.
- **Medical management:** authorization and medical policy. Out of scope for '28; matching auths to claims is claims'.

**Boundary rules:**

- **Stores:** a domain's code uses only its own domain's stores.
- **Asking other domains:** it goes through their services, defined in proto (`proto/coreadmin/<domain>/v1/*_service.proto`) and called in-process (Connect, simple mode). `internal/common/repo/boundary_test.go` enforces this.
- **What's exempt:** demos (packages named `*demo`), Journey, the simulator and seed loading set up every domain.
- **Date semantics:** service answers use the latest data we have, about the date asked. Coverage on a claim's date of service reflects a retro-termination recorded today.
- **Shared code** lives in `internal/common/`. Keep domain logic out of it.

## Data rules

- **Inserts only.**
  - The only UPDATE allowed closes the current version's transaction time (`tx_to`). "Update" means closing the current version and inserting the next.
  - Every table is its own audit log, and state on any date can be reconstructed.
  - Counters are insert-only rows too, e.g. claim-number blocks.
- **Every table is versioned:** `id, revision, tx_from, tx_to, recorded_by, data jsonb`. An exclusion constraint keeps exactly one version valid at a time.
- **Business keys:**
  - Defined per table, within that domain's work. Each is either "permanent" or "one at a time".
  - They're still open: don't assume them.
  - Foreign keys are not the framing, and no foreign keys cross schemas.
- **Indexes follow access patterns.** Every lookup names its fields (`store.Eq`, `Prefix`, `Contains`), and every index states the lookup it serves. A test checks each plan uses its index.
- **Configuration is hardcoded in Go** unless there's a concrete reason for data: externally maintained code sets with dates, per-customer variation, or changes needed without a deploy.

## Protobuf rules

- **Every field has a comment.**
- **Enums mapped from outside codes** (X12 and similar) have `UNSPECIFIED = 0` (not sent) and `UNKNOWN` (sent but unrecognized). The raw code is kept, and an unrecognized code is not rejected at intake. Only small fixed code sets become enums; open-ended lists stay strings.
- **Intake captures what was sent, verbatim:** no lookups, no matching. Matching (member, customer, original claim) belongs downstream, e.g. in adjudication, which reruns when data changes.
- **`gen/` is generated, not committed:** run `make gen` after cloning or merging proto changes.

## Running

```
make gen      # after cloning, and after proto changes
make test     # go test ./... (runs gen first)
make check    # gen, lint, test
make demo     # demo server on localhost:8080
go run ./cmd/demo -addr localhost:8081   # a second demo, e.g. from another worktree
```

- **Postgres:** everything runs on one local embedded Postgres on port 5433, started on demand.
  - Its data lives in a shared directory outside every checkout (`internal/common/pgdb`).
  - Each checkout gets its own demo database; tests use a fresh sandbox per test.

## Open, decided later with the user

- **Claim workflow:** pend queues, routing. There is no claim-level payment hold: a claim not yet reported is reopened (`ReopenClaim`); withholding money for reasons about the payee or funding belongs with settlement.
- **Later claims after an adjustment.** When an adjustment or void changes a family's accumulators, claims adjudicated after it may be wrong. Nothing detects or tracks that today, on purpose: an earlier attempt was removed. The user will design it; don't add a mechanism, a field on the claim, or a name for it.
- **Per-table business keys.**
- **Duplicate detection.**
- **Adjudication step order and dependencies:** the adjudication map lists steps without an order.
- **Raw inbound file storage:** comes with the SFTP integrations.
