# Employer

Enrollment intake (834), eligibility, plan design, the employer portal, and billing & funding. Other domains ask it through `EmployerService` (`proto/coreadmin/employer/v1/employer_service.proto`): customers, members, a member's medical coverage on a date, and the plan version in force. Answers use the latest data we have, about the date asked.

| Package | |
|---|---|
| `internal/employer` | EmployerService |
| `employerdb` | The Employer schema's tables and stores |
| `enrollmentintake` | 834 intake: maps member loops to EnrollmentTransactions, writes the 999 |
| `eligibility` | Eligibility derived from an append-only input log; every member version kept; regeneration; Verify |
| `eligibilitydemo` | Eligibility sandboxes and the walkthrough's live examples |

## Enrollment Intake

`/enrollment-intake/`: 834 files from employers, the enrollment counterpart of Claims Intake. Pick a sample (a new hire with family, a newborn, a termination and a retroactive one, an address change, COBRA continuation, a full file, an unrecognized maintenance type, a bad set, a bad envelope), edit it or paste your own, and see the mapped records live, the 999 per transaction set, and the raw file with each member's segments.

Hovering a field of the mapped record explains it: the tooltips are the comments in `enrollment.proto`, served from a descriptor set `make gen` builds, and a test fails if any field lacks one.

Intake captures each member loop (INS) as an `EnrollmentTransaction`, exactly as sent: every DTP and REF with its qualifier, and any segment it doesn't map kept raw in `unmapped_segments`. The small, fixed code sets eligibility acts on (purpose, action, maintenance type, benefit status) are enums with two empty states: UNSPECIFIED when the code wasn't sent, UNKNOWN when it was sent but isn't one intake recognizes. An unknown code is kept as sent in `unrecognized_codes` and doesn't fail the 999. Longer code lists (relationship, reason, coverage level) stay as sent. It looks nothing up. Which customer, member and plan a loop means, and what it changes, is for eligibility to decide when it applies the loop; for now loops wait in an inbox. The demo has its own sandbox.

## Eligibility

`/eligibility/`: members as derived from every 834 loaded in Enrollment Intake (the two demos share a sandbox with two clients, Acme and Brightline, each starting from an open-enrollment full file, and one COBRA vendor serving both).

Eligibility is derived, never edited:
- **Inputs.** Every 834 member loop, and every approval of full-file terminations, is an `EligibilityInput`, recorded in sequence and never changed.
- **Assertions.** Each input asserts something about a member for a range of dates ("covered on the PPO, family tier, from Apr 1"; "not covered after Jan 31"). For any date, the latest-recorded assertion wins, so a retro term, a reinstatement and a corrected date are a series of assertions, not edits.
- **Versions.** Whenever a derived member changes, a `MemberVersion` is saved with when it was recorded and the input that caused it. The latest version recorded at or before a time is what we believed then. A full file that only confirms what's on file creates no versions.
- **Senders.** Each customer has a list of who may send its enrollment and as what (`internal/employer/eligibility/senders.go`): its employer (HR system) and, if it uses one, a COBRA administrator; a customer without one runs COBRA on its own 834s. One COBRA vendor can serve many customers, and customers can use different vendors. A file from a sender that isn't set up is an exception; a COBRA administrator sends only COBRA; an employer can't enroll COBRA when a vendor runs it; and an employer termination doesn't end coverage the COBRA administrator established.
- **Full files.** Members a full file leaves out are proposed for termination and change only when someone approves. A full file speaks only for the members its sender established, so the employer's file doesn't reconcile the vendor's continuees, and the vendor's file doesn't touch employees.
- **Regeneration.** Replaying every input under the current rules reproduces the members; a difference becomes new versions recorded now, never a rewrite of old ones. Member ids and card numbers depend only on the inputs, so replays agree, even into an empty database. `Service.Compare` replays under other rules (e.g. `BeforeSenderAuthority`) to show who a rules change would affect before adopting it.

**Walkthrough** at `/eligibility/how.html`: a slide deck explaining the approach (two kinds of time, assertions, full files, senders, regeneration). Every example in it runs live on real 834 samples in a fresh sandbox (`internal/employer/eligibilitydemo`), and its claims are pinned by a test.

**Checks** (`Service.Verify`): the input log has no gaps; every version names its cause; the latest version is what's on file; replaying every input reproduces the members; and, for every input, replaying only the inputs up to it reproduces exactly the versions recorded at that moment. The test suite runs them after sending every sample in 12 random orders, and checks they catch a member edited behind eligibility's back.

Claims ask `EmployerService` for a member's coverage on the date of service, from the latest data. Nothing adjusts claims when eligibility changes retroactively; that's decided by people for now. `internal/employer/eligibility/schema.sql` sketches the Postgres tables.
