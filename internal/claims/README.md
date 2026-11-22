# Claims

Claims intake (837), adjudication, remittance (835), and the claims operations: process, reprocess, adjust, void, holds and flags. Claims also owns the plan as claims understands it, accumulators, and pricing (fee schedules). It asks Employer (`EmployerService`) about members, coverage and plans, and Network (`NetworkService`) about providers and contracts.

| Package | |
|---|---|
| `internal/claims` | ClaimsService operations; claim numbers; payment state |
| `claimsdb` | The Claims schema's tables and stores |
| `claimsintake` | 837P intake: maps files to claims, writes the 999 |
| `adjudication` | The engine, pends and denials, remark codes; `Steps` for the adjudication map |
| `remittance` | 835s from adjudicated claims: build, balance, reverse, render; previews |
| `remittancedemo` | The Remittance demo |

## Adjudication map

`/adjudication/`: clicking Claim Adjudication on the map opens a map of adjudication itself: everything the engine does, listed without asserting an order (order and dependencies are still to be designed), the pends and denials it can end in, the Claims data it uses, and what it asks the Employer and Network services, with their proto. The steps come from `adjudication.Steps`; tests check every pend and denial the engine uses is on a step and every call is a real method.

## Claims Intake

`/claims-intake/`: 837P files from the clearinghouse. Pick a sample (or edit one, or paste your own), send it, and see the 999 verdict per transaction set, each claim as intake mapped it (as YAML), the raw file with each claim's segments, and the 999 we return. The page stops where intake does: it doesn't show adjudication results. Claims load into Journey's sandbox, and each loaded claim links to Journey to see how it adjudicated.

Intake runs once per file and only reads. It copies the submitted fields onto the claim exactly as sent and looks nothing up. Everything that's matched to our data happens in adjudication, which reruns on every reprocess:

- **999.** Rejects only what can't be read reliably: envelope errors (SE count, control numbers), missing required segments or elements, bad codes. A rejected transaction set loads nothing; other sets in the file still load. A file whose ISA13 we've already loaded from that sender is a duplicate and loads nothing.
- **Every readable claim is saved and visible to examiners**, even when nothing matches. The clearinghouse received date is BHT04/05 (one clearinghouse for now; no per-clearinghouse configuration).
- **Adjudication matches on every run, from the submitted fields only.** Member: claims sends the subscriber and patient as submitted (member id, SSN, name, birth date, gender) to Employer's `MatchClaimMember`, which owns the matching rule and answers matched, not found or ambiguous; anything but matched pends. Original (frequency 7/8): by `original_link` if set, otherwise by the quoted REF*F8 resolved to the claim's current version. What a run matched is recorded on that run's `Adjudication` (and copied to the claim's output-only `member_id`, `customer_id`, `original_claim_id`), never written into the submitted fields. So a rerun after enrollment catches up, or after an examiner links an original, matches again.
- **Claim numbers are assigned on receipt and never change.** Intake reserves a number for every claim loop in a file before loading it (see Claim numbers, below). A provider's corrected claim or void gets its own number and links to its original by what adjudication matched, on its first run or later; Journey shows the relationship as a badge: "Corrects 26063-A-00001-2", or "Correction · original not linked" until it's matched. Journey's "Other versions" follows the links, so one claim's history can span numbers.
- **Unmatched pends.** No member: `MEMBER_NOT_FOUND`. No original: `ORIGINAL_NOT_FOUND`. An original that's still pended, in an unreleased payment run, or already being replaced: the claim pends until that settles, and reprocessing finishes it (including finalizing a pended void).

## Claims Lifecycle demo

A single page for making things happen to the system and watching its state change: process claims, clear pends, adjust and void, run and release payment runs, simulate bank events, change eligibility and fee schedule data. It shows the family's accumulator record (with its revision), every claim's line-by-line decision and stored protojson, and a plain-language event log. State is in Postgres (sandbox `lifecycle`); Reset starts over.

The left panel is the scenario library (`internal/scenario/library.go`). Each scenario is a script of actions with the outcome expected after every step, so it works as a guided walkthrough in the UI and as a test (`go test ./internal/scenario`). To add an edge case, add a `Scenario` there.

What is real and what is simplified:

- Real: the adjudication engine (`adjudication.DefaultEngine`), the accumulator ledger with its revision check, and the claims operations. Professional claims; no coordination of benefits, benefit limits or bundling.
- Simplified: the payment system (`internal/claims/payment.go`) only writes payment state back onto claims; remittances and netting are log lines. Eligibility and fee schedule changes are direct edits standing in for file loads.

## Remittance

`/remittance/`: from adjudication result to 835. Each sample adjudicates its claims on a fresh sandbox and builds the remittance: a copay, a preventive visit, everything to the deductible, two lines, a denial and a partial denial, a batch of claims on one payment, a corrected claim and a void reversing what an earlier 835 reported, and a void with nothing to net against. The adjudication results sit beside the 835; hovering a service line lights up its segments. Payments go by ACH or check. Nothing is kept between runs.

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

- **Claim identity.** `id` is an internal UUIDv7 (`internal/common/ids`), assigned at intake and never shown. People see `claim_number`, `YYJJJ-S-NNNNN` (year, day received, source, daily sequence). A replacement or void keeps the original's number with the next version: `26075-E-00012-2`. The Claims Lifecycle sandbox gives originals short ids (`clm_A`) for its scenario scripts and names later versions `clm_A-2`; the sandbox resolves those names to the real ids.
- **Transactions.** `Repos.InTx` runs work across every store as one transaction: reads see its own writes, and everything commits together or not at all. Each claim operation is one transaction, so receiving, reprocessing, adjusting or voiding a claim saves the claim, its accumulator postings, the original's retirement and any review flags together. Payment run selection and release are one transaction per run. A commit fails with `ErrConflict` if a record it wrote was changed by someone else first; adjudication then recalculates from the start. Activity log lines from a transaction appear only once it commits. `Store.Lock` claims a record for the rest of a transaction without changing it (Postgres's `SELECT … FOR UPDATE`). Adjudication locks a replacement's or void's original before checking it, so two corrections of the same claim in flight at once can't both become its open replacement: the second to commit conflicts, recalculates and sees the first.
- **Remittance.** An 835 (`remittance.v1.Remittance`) covers one payment to one payee from one customer's plan, since a self-funded plan pays from the employer's own account. It is built from each line's adjudication, whose CAS adjustments already balance, and checked against the 835 balancing rules before it exists. It is stored as sent: when a claim is replaced or voided, the reversal (CLP02 22) repeats what the provider was told with the signs flipped, rather than recomputing it, and the payment is the net. A preview is the same build with a placeholder check, for looking at before a claim is finalized; it is never stored. CARCs and remark codes are decided in adjudication, from one table of denial reporting; a CARC that needs a remark code (96 needs N130) without one pends the line (`REMARK_CODE_REQUIRED`) until a rerun gets it right, and the 835 check refuses it too. Not built yet: recoveries that exceed the payment (PLB), and wiring into payment runs.

## Claim numbers

**Claim numbers** are `YYJJJ-S-NNNNN` and are never reused. Intake reserves a block per 837 file, one number per claim loop in file order, rejected sets included, so a number with no claim always means a claim loop that didn't load; the job records the block. A claim arriving alone (API, portal, paper) reserves a block of one. Reservations are rows in `claim_number_blocks`, inserted and never changed, with an exclusion constraint against overlapping blocks. A provider's corrected claim or void is a new submission with its own number, linked to its original by what adjudication matched; only adjustments and voids we make are versions of an existing number (`-2`, `-3`).
