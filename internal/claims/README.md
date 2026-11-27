# Claims

Claims intake (837), adjudication, remittance (835), and the claims operations: process, reprocess, reopen, adjust and void, and the read-only estimate and previews. Claims also owns the plan as claims understands it, accumulators, and pricing (fee schedules). It asks Employer (`EmployerService`) about members, coverage and plans, and Network (`NetworkService`) about providers and contracts.

| Package | |
|---|---|
| `internal/claims` | ClaimsService operations; claim numbers; payment state |
| `claimsdb` | The Claims schema's tables and stores |
| `claimsintake` | 837P intake: maps files to claims, writes the 999 |
| `adjudication` | The engine, pends and denials, remark codes; `Steps` for the adjudication map |
| `remittance` | 835s from adjudicated claims: build, balance, reverse, render; previews |
| `remittancedemo` | The Remittance demo |
| `claimsdemo` | "How claims works": the walkthrough's examples, played through the real operations |

## How claims works

`/claims/`: clicking the Claims domain's title on the map opens the domain on its own, with every component in it (service definitions, disputes, overpayments, and the two halves of externally processed claims, which the overview leaves out). "How it works" beside the title there opens a walkthrough of the domain, a slide at a time: the ClaimsService endpoints (read from the proto, each marked built or declared only), the Claim record's fields grouped by who writes them, the tables, how a provider's corrected claim differs from an adjustment we make, what an adjustment does to later claims, the adjustment delta, and how 835s net reversals. It shows the release of the payment run as the dividing line: reopening before, replacing after. It also shows a claim's four separate facts (status, outcome, superseded, payment) and how the outcome follows from the lines.

Every example is played on a fresh sandbox when the page loads, by calling the real operations (`claimsdemo.BuildTour`); the slides draw what came back, each call with its request, response and every claim's state after it. `TestTour` holds the slides to the code: every service method and every `Claim` field must be placed, every built operation must be called by an example, and each story must end as the slides say. What the payment system would do is the stand-in in `payment.go`, drawn dashed.

The last slide is a proposal, not built: settlement lines and the payment cycle in Claims, funding and payments in Finance, with its open questions.

## Adjudication map

`/adjudication/`: clicking Claim Adjudication on the map opens a map of adjudication itself: everything the engine does, listed without asserting an order (order and dependencies are still to be designed), the pends and denials it can end in, the Claims data it uses, and what it asks the Employer and Network services, with their proto. The steps come from `adjudication.Steps`; tests check every pend and denial the engine uses is on a step and every call is a real method.

## Claims Intake

`/claims-intake/`: 837P files from the clearinghouse. Pick a sample (or edit one, or paste your own), send it, and see the 999 verdict per transaction set, each claim as intake mapped it (as YAML), the raw file with each claim's segments, and the 999 we return. The page stops where intake does: it doesn't show adjudication results. Claims load into Journey's sandbox, and each loaded claim links to Journey to see how it adjudicated.

Intake runs once per file and only reads. It copies the submitted fields onto the claim exactly as sent and looks nothing up. It maps every loop of the 837P (header, billing provider, subscriber, payer, patient, claim, other insurance, service lines and their drug, provider, other-payer and form loops); a segment it doesn't know, or an element it has no field for, is kept verbatim in the claim's `unmapped_segments`, and a test holds the samples to none. Most of what it captures is situational and not read by adjudication yet. Everything that's matched to our data happens in adjudication, which reruns on every reprocess:

- **999.** Rejects only what can't be read reliably: envelope errors (SE count, control numbers), missing required segments or elements, bad codes. A rejected transaction set loads nothing; other sets in the file still load. A file whose ISA13 we've already loaded from that sender is a duplicate and loads nothing.
- **Every readable claim is saved and visible to examiners**, even when nothing matches. The clearinghouse received date is BHT04/05 (one clearinghouse for now; no per-clearinghouse configuration).
- **Adjudication matches on every run, from the submitted fields only.** Member: claims sends the subscriber and patient as submitted (member id, SSN, name, birth date, gender) to Employer's `MatchClaimMember`, which owns the matching rule and answers matched, not found or ambiguous; anything but matched pends. Original (frequency 7/8): by `original_link` if set, otherwise by the quoted REF*F8 resolved to the claim's current version. What a run matched is recorded on that run's `Adjudication` (and copied to the claim's output-only `member_id`, `customer_id`, `original_claim_id`), never written into the submitted fields. So a rerun after enrollment catches up, or after an examiner links an original, matches again.
- **Claim numbers are assigned on receipt and never change.** Intake reserves a number for every claim loop in a file before loading it (see Claim numbers, below). A provider's corrected claim or void gets its own number and links to its original by what adjudication matched, on its first run or later; Journey shows the relationship as a badge: "Corrects 26063-E-00001-2", or "Correction · original not linked" until it's matched. Journey's "Other versions" follows the links, so one claim's history can span numbers.
- **Unmatched pends.** No member: `MEMBER_NOT_FOUND`. No original: `ORIGINAL_NOT_FOUND`. An original that's still pended, already superseded, or already being replaced: the claim pends until that settles, and reprocessing finishes it (including adjudicating a pended void).

## Claims Lifecycle demo

A single page for making things happen to the system and watching its state change: process claims, clear pends, adjust and void, run and release payment runs, simulate bank events, change eligibility and fee schedule data. It shows the family's accumulator record (with its revision), every claim's line-by-line decision and stored protojson, and a plain-language event log. State is in Postgres (sandbox `lifecycle`); Reset starts over.

The left panel is the scenario library (`internal/scenario/library.go`). Each scenario is a script of actions with the outcome expected after every step, so it works as a guided walkthrough in the UI and as a test (`go test ./internal/scenario`). To add an edge case, add a `Scenario` there.

What is real and what is simplified:

- Real: the adjudication engine (`adjudication.DefaultEngine`), the accumulator ledger with its revision check, and the claims operations. Professional claims; no coordination of benefits, benefit limits or bundling.
- Simplified: the payment system (`internal/claims/payment.go`) only writes payment state back onto claims; remittances and netting are log lines. Eligibility and fee schedule changes are direct edits standing in for file loads.

## Remittance

`/remittance/`: from adjudication result to 835. Each sample adjudicates its claims on a fresh sandbox and builds the remittance: a copay, a preventive visit, everything to the deductible, two lines, a denial and a partial denial, a batch of claims on one payment, a corrected claim and a void reversing what an earlier 835 reported, and a void with nothing to net against. The adjudication results sit beside the 835; hovering a service line lights up its segments. Payments go by ACH or check. Nothing is kept between runs.

## Service definitions

`/service-definitions/`: named groups of services ("Advanced imaging", "Emergency room"), each defined by the codes a claim line carries. `ServiceDefinitionService` (`proto/coreadmin/claims/v1/service_definitions.proto`, `internal/claims/servicedefs`) lists, gets, creates and updates definitions, returns every earlier version of one, and answers `MatchServiceDefinitions`: which groups a line falls in on its date of service, and the entries that put it there.

- **Entries** are a code or an inclusive range in one of three code systems (procedure, revenue, place of service). Nothing about the provider or the diagnosis is tested: a definition is about the service performed and where, each with the dates of service it counts for.
- **Matching:** a line is in a group when, for every code system the group has entries in effect for on that date, the line's code is in one of them. A group with nothing in effect matches nothing. A line can be in several groups.
- **Updates** send the whole definition back at the revision it was read at; every version is kept.

The page calls the service itself, mounted as Connect serves it (JSON over POST), over a sandbox seeded with the categories the seeded plans carry. Pick a group to see the codes that count for a service today, densely, by code system. Most count on every date; one that doesn't carries its dates in parentheses, as years where it can: `10 (2022–)`. Codes that no longer count, or don't yet, sit behind "not current". Which codes count today is what `GetServiceDefinition` with `effective_on` answers. Click a code to change its range or dates or remove it; type a code or range to add one. When the group was edited is the revision beside its name: clicking it opens the edit history (`ListServiceDefinitionRevisions`), newest first, each revision with when, by whom and what it changed; clicking a revision shows the group as it stood then, read-only. `MatchServiceDefinitions` has no place on the page; its tests exercise it.

The "How it works" tab explains it in five cards: a group is a name and its codes; a line is in a group when it has a code of every kind the group lists; the two kinds of time (date of service, and when we edited it); the rows the table holds, one per save; and the endpoints. Its examples are the real service's answers on a fresh sandbox (`servicedefsdemo.BuildTour`), pinned by a test.

Nothing refers to a definition yet. Plans still carry their own service categories, and adjudication matches against those. Plan benefits, pricing contracts and claim edits are expected to refer to definitions by id; what a change to a definition does to each of them is not designed.

## Claim lifecycle

Four separate facts say where a claim stands. None overwrites another.

```
status:                RECEIVED ─► PENDED ─► (ReprocessClaim) ─► ADJUDICATED
adjudication.outcome:  APPROVED | PARTIALLY_DENIED | DENIED | VOID
superseded_by:         unset ─► a replacement or a void
payment.state:         SELECTED ─► ISSUED ─► CLEARED | RETURNED | VOIDED
```

- **Status** is where the claim is in processing. One open pend, even on one line, holds the whole claim at `PENDED`. It is `ADJUDICATED` when no pend is open and every line is decided. Accumulators post then, once, in a single conditional write to the family's accumulator record (no locks; a concurrent claim for the same family recalculates).
- **Outcome** is what that run decided, read off the lines: `APPROVED` when every line is payable (the plan may still pay nothing), `DENIED` only when every line is denied, `PARTIALLY_DENIED` between. A void's outcome is `VOID`: it decides no lines. On the 835 only a full denial is CLP02 4; a partial denial is 1, with the denial in the line's CAS.
- **Reported or not.** A claim's result is reported when its payment run is released (`payment.state` `ISSUED` or later): the 835 goes out and the payment is issued. That is the dividing line for changing an adjudicated claim, and it keeps one rule true: every adjudicated result that reaches an 835 is reported once, and only a reported result is ever superseded.
  - **Not yet released.** Nothing has been reported, so there is nothing to reverse. `ReopenClaim` puts the same claim back to `PENDED` (a `REOPENED` pend carries the reason), takes it out of an unreleased run, reverses its accumulator postings and keeps the result in the history; `ReprocessClaim` re-runs it in place. There is no new version. `AdjustClaim` and `VoidClaim` are refused.
  - **A provider's corrected claim or void (frequency 7 or 8) of a claim not yet released** closes that claim with an answer of its own: every line denied as `REPLACED_BY_PROVIDER` or `VOIDED_BY_PROVIDER`, its postings reversed, out of any unreleased run. It is not superseded. The corrected claim is then decided as a claim in its own right, so each claim the provider sent gets one answer on an 835 and nothing is reversed. No CARC has been chosen for those two denials; until one is, the 835 check refuses a remittance carrying them.
  - **Released.** The result stands as reported and changes only through a replacement or void claim that references it (`AdjustClaim`, `VoidClaim`, or a provider's frequency 7 or 8). When that claim is adjudicated the original's `superseded_by` is set, naming it and whether it is a replacement or a void, and the next 835 reverses what was reported. The original's status and outcome stay as they were: a denied claim that is corrected still reads as denied, and superseded. `ReopenClaim` is refused.
  - There is no claim-level payment hold. Only `PENDED` claims are reprocessed.
- **Asking without saving.** No operation that writes has a dry-run flag. Three read-only operations answer what the writing ones would do, and save nothing (no claim, no number, no postings): `EstimateClaim` for a claim not in the system; `PreviewReadjudication` for an existing claim not yet reported, pended or adjudicated, run as submitted and as if it had posted nothing itself; `PreviewAdjustment` for a reported claim, returning the replacement and the delta against what the payee holds. Each preview refuses the other's case. An answer reflects the data at the moment it is asked; it is not a promise.
- When an adjustment or void changes accumulator postings, later claims for the same family were decided against balances that no longer hold. They are not changed, and nothing detects or records it today. What should is still to be designed.
- An adjudication result is output only. Nothing reads a previous result; the accumulator ledger, stop-loss and remittance reverse from their own records by claim id.
- Every line result must pass `adjudication.CheckLineBalanced`.

## Conventions

- **Claim identity.** `id` is an internal UUIDv7 (`internal/common/ids`), assigned at intake and never shown. People see `claim_number`, `YYJJJ-S-NNNNN` (year, day received, source: E for EDI, P for paper, W for the web portal; daily sequence). A replacement or void keeps the original's number with the next version: `26075-E-00012-2`. Every claim in the sandboxes arrives as an 837 (the simulator writes a one-claim file for each claim it submits and loads it through intake), so their numbers all carry E. The Claims Lifecycle sandbox gives originals short ids (`clm_A`) for its scenario scripts and names later versions `clm_A-2`; the sandbox resolves those names to the real ids.
- **Transactions.** `Repos.InTx` runs work across every store as one transaction: reads see its own writes, and everything commits together or not at all. Each claim operation is one transaction, so receiving, reprocessing, adjusting or voiding a claim saves the claim, its accumulator postings and the original being marked superseded together. Payment run selection and release are one transaction per run. A commit fails with `ErrConflict` if a record it wrote was changed by someone else first; adjudication then recalculates from the start. Activity log lines from a transaction appear only once it commits. `Store.Lock` claims a record for the rest of a transaction without changing it (Postgres's `SELECT … FOR UPDATE`). Adjudication locks a replacement's or void's original before checking it, so two corrections of the same claim in flight at once can't both become its open replacement: the second to commit conflicts, recalculates and sees the first.
- **Remittance.** An 835 (`remittance.v1.Remittance`) covers one payment to one payee from one customer's plan, since a self-funded plan pays from the employer's own account. It is built from each line's adjudication, whose CAS adjustments already balance, and checked against the 835 balancing rules before it exists. It is stored as sent: when a claim is replaced or voided, the reversal (CLP02 22) repeats what the provider was told with the signs flipped, rather than recomputing it, and the payment is the net. A preview is the same build with a placeholder check, for looking at before a claim is adjudicated; it is never stored. CARCs and remark codes are decided in adjudication, from one table of denial reporting; a CARC that needs a remark code (96 needs N130) without one pends the line (`REMARK_CODE_REQUIRED`) until a rerun gets it right, and the 835 check refuses it too. Not built yet: recoveries that exceed the payment (PLB), and wiring into payment runs.

## Claim numbers

**Claim numbers** are `YYJJJ-S-NNNNN` and are never reused. Intake reserves a block per 837 file, one number per claim loop in file order, rejected sets included, so a number with no claim always means a claim loop that didn't load; the job records the block. A claim arriving alone (portal, paper) reserves a block of one. Reservations are rows in `claim_number_blocks`, inserted and never changed, with an exclusion constraint against overlapping blocks. A provider's corrected claim or void is a new submission with its own number, linked to its original by what adjudication matched; only adjustments and voids we make are versions of an existing number (`-2`, `-3`).
