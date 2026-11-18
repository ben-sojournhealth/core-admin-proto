package scenario

import "coreadmin/internal/sim"

// The sandbox world (see /seed):
//
//   Plan "Acme PPO 1500": in-network deductible $1,500 individual / $3,000
//   family (embedded), out-of-pocket max $5,000 / $10,000; out-of-network
//   deductible $3,000, out-of-pocket max $10,000.
//     Office visit     $30 copay, no deductible
//     Outpatient surgery, advanced imaging   deductible, then 20%
//       (imaging needs prior authorization)
//     Out-of-network   deductible, then 40%; priced at 140% of Medicare
//
//   Family: Jordan (subscriber), Sam (spouse), Riley (child); Avery is a
//   newborn not yet enrolled.
//
//   Dr. Chen is in-network. Contracted rates: 99213 $95, 99214 $135,
//   70551 (MRI) $420, 29881 (knee arthroscopy) $1,200, 27447 (knee
//   replacement) $18,000. Ortiz Orthopedics is out-of-network.

const (
	jordan = "mem_1001"
	sam    = "mem_1002"
	riley  = "mem_1003"
	avery  = "mem_1004"
	ortiz  = "1987654321"
)

func process(c sim.ClaimSpec) sim.Action { return sim.Action{Type: "process", Claim: &c} }

func office(id, member, dos string) sim.ClaimSpec {
	return sim.ClaimSpec{ID: id, Member: member, Procedure: "99213", Charge: 18000, DOS: dos}
}

func longVisit(id, member string) sim.ClaimSpec {
	return sim.ClaimSpec{ID: id, Member: member, Procedure: "99214", Charge: 25000}
}

func surgery(id, member, dos string) sim.ClaimSpec {
	return sim.ClaimSpec{ID: id, Member: member, Procedure: "29881", Charge: 250000, DOS: dos}
}

func mri(id, member, dos, auth string) sim.ClaimSpec {
	return sim.ClaimSpec{ID: id, Member: member, Procedure: "70551", Charge: 90000, DOS: dos, Auth: auth}
}

var (
	selectAll  = sim.Action{Type: "select"}
	releaseRun = sim.Action{Type: "release"}
	toOffice   = &sim.ClaimSpec{Procedure: "99213", Charge: 18000}
)

// Library is every scenario, in display order.
var Library = []Scenario{
	// ---------------------------------------------------------------- Basics
	{
		ID: "office-visit", Group: "Basics",
		Title:   "Office visit with a copay",
		Summary: "The simplest claim: contracted rate, flat copay, no deductible.",
		Steps: []Step{{
			Label:  "Jordan sees Dr. Chen; the office bills $180",
			Why:    "The contract allows $95, so $85 is written off (CO-45). The member pays the $30 copay (PR-3) and the plan pays $65. The copay counts toward the out-of-pocket maximum but not the deductible.",
			Action: process(office("clm_A", jordan, "")),
			Checks: []Check{
				status("clm_A", "APPROVED"), allowed("clm_A", 9500), pays("clm_A", 6500), owes("clm_A", 3000),
				accum("OOP_INN_IND", jordan, 3000), accum("OOP_INN_FAM", jordan, 3000), accum("DED_INN_IND", jordan, 0),
			},
		}},
	},
	{
		ID: "deductible-coinsurance", Group: "Basics",
		Title:   "Deductible, then coinsurance",
		Summary: "Two claims walk a member through their deductible; the second one straddles it.",
		Steps: []Step{
			{
				Label:  "Jordan has knee arthroscopy (allowed $1,200)",
				Why:    "The whole allowed amount goes to the $1,500 deductible, so the plan pays nothing.",
				Action: process(surgery("clm_A", jordan, "")),
				Checks: []Check{pays("clm_A", 0), owes("clm_A", 120000), accum("DED_INN_IND", jordan, 120000)},
			},
			{
				Label:  "Jordan has an authorized MRI (allowed $420)",
				Why:    "Only $300 of deductible is left. The other $120 is split 20% member / 80% plan.",
				Action: process(mri("clm_B", jordan, "2026-03-12", "PA-1001")),
				Checks: []Check{
					owes("clm_B", 32400), pays("clm_B", 9600),
					accum("DED_INN_IND", jordan, 150000), accum("OOP_INN_IND", jordan, 152400),
				},
			},
		},
	},
	{
		ID: "out-of-network", Group: "Basics",
		Title:   "Out-of-network provider",
		Summary: "No contract: priced from Medicare, with separate out-of-network accumulators.",
		Steps: []Step{{
			Label:  "Jordan has knee arthroscopy at Ortiz Orthopedics",
			Why:    "Ortiz has no contract, so the plan's out-of-network tier applies: allowed is 140% of the Medicare rate ($900 → $1,260). It goes to the out-of-network deductible; the in-network deductible is untouched.",
			Action: process(sim.ClaimSpec{ID: "clm_A", Member: jordan, Provider: ortiz, Procedure: "29881", Charge: 250000}),
			Checks: []Check{
				tier("clm_A", "OON"), allowed("clm_A", 126000), pays("clm_A", 0),
				accum("DED_OON_IND", jordan, 126000), accum("DED_INN_IND", jordan, 0),
			},
		}},
	},
	{
		ID: "estimate", Group: "Basics",
		Title:   "Cost estimate (dry run)",
		Summary: "Run a claim through adjudication without saving it or touching accumulators.",
		Steps: []Step{{
			Label:  "Estimate an MRI for Jordan",
			Why:    "A dry run returns the full decision but saves nothing: no claim, no accumulator postings.",
			Action: sim.Action{Type: "process", DryRun: true, Claim: &sim.ClaimSpec{ID: "clm_EST", Member: jordan, Procedure: "70551", Charge: 90000, Auth: "PA-1001"}},
			Checks: []Check{preview("APPROVED", 0, 42000), noClaim("clm_EST"), accum("DED_INN_IND", jordan, 0)},
		}},
	},

	// ---------------------------------------------------------- Accumulators
	{
		ID: "same-instant", Group: "Accumulators",
		Title:   "Two claims for one member at the same instant",
		Summary: "Both are calculated against the same balance; the revision check stops the second from double-counting the deductible.",
		Steps: []Step{{
			Label: "Two $1,200 surgeries for Jordan are processed simultaneously",
			Why:   "Each is calculated seeing $0 of deductible met, so each wants to apply $1,200. The first commit wins. The second is rejected because the family's accumulator record has moved on, and is recalculated: $300 to the deductible, then 20% of the remaining $900.",
			Action: sim.Action{Type: "race", Claims: []*sim.ClaimSpec{
				{ID: "clm_A", Member: jordan, Procedure: "29881", Charge: 250000, DOS: "2026-03-10"},
				{ID: "clm_B", Member: jordan, Procedure: "29881", Charge: 250000, DOS: "2026-03-12"},
			}},
			Checks: []Check{
				logged("Recalculating"),
				owes("clm_A", 120000), pays("clm_A", 0),
				owes("clm_B", 48000), pays("clm_B", 72000),
				accum("DED_INN_IND", jordan, 150000),
			},
		}},
	},
	{
		ID: "family-deductible", Group: "Accumulators",
		Title:   "Family deductible met before an individual's",
		Summary: "Embedded family deductible: a member stops paying deductible when either their own or the family's limit is reached.",
		Steps: []Step{
			{
				Label:  "Jordan: surgery, $1,200 to the deductible",
				Action: process(surgery("clm_A", jordan, "")),
				Checks: []Check{accum("DED_INN_FAM", jordan, 120000)},
			},
			{
				Label:  "Sam: surgery, $1,200 to the deductible",
				Action: process(surgery("clm_B", sam, "")),
				Checks: []Check{accum("DED_INN_FAM", jordan, 240000)},
			},
			{
				Label:  "Riley: surgery",
				Why:    "Riley has met none of their own $1,500, but the family has only $600 left of its $3,000. Riley pays $600 of deductible, then 20% of the other $600.",
				Action: process(surgery("clm_C", riley, "")),
				Checks: []Check{
					owes("clm_C", 72000), pays("clm_C", 48000),
					accum("DED_INN_IND", riley, 60000), accum("DED_INN_FAM", jordan, 300000),
				},
			},
			{
				Label:  "Jordan: authorized MRI",
				Why:    "Jordan still has $300 of individual deductible left, but the family deductible is met, so none applies: straight to 20% coinsurance.",
				Action: process(mri("clm_D", jordan, "2026-03-12", "PA-1001")),
				Checks: []Check{owes("clm_D", 8400), pays("clm_D", 33600), accum("DED_INN_IND", jordan, 120000)},
			},
		},
	},
	{
		ID: "oop-max", Group: "Accumulators",
		Title:   "Out-of-pocket maximum reached mid-claim",
		Summary: "Cost sharing is cut off partway through a claim, then is zero afterwards.",
		Steps: []Step{
			{
				Label:  "Jordan has a knee replacement (allowed $18,000)",
				Why:    "$1,500 deductible plus 20% of $16,500 is $4,800, just under the $5,000 out-of-pocket maximum.",
				Action: process(sim.ClaimSpec{ID: "clm_A", Member: jordan, Procedure: "27447", Charge: 4200000}),
				Checks: []Check{owes("clm_A", 480000), pays("clm_A", 1320000), accum("OOP_INN_IND", jordan, 480000)},
			},
			{
				Label:  "Follow-up arthroscopy (allowed $1,200)",
				Why:    "20% would be $240, but only $200 of out-of-pocket room is left, so coinsurance is capped at $200.",
				Action: process(surgery("clm_B", jordan, "2026-03-20")),
				Checks: []Check{owes("clm_B", 20000), pays("clm_B", 100000), accum("OOP_INN_IND", jordan, 500000)},
			},
			{
				Label:  "Office visit after the maximum is met",
				Why:    "Even the $30 copay is waived: the plan pays the full allowed amount.",
				Action: process(office("clm_C", jordan, "2026-03-25")),
				Checks: []Check{owes("clm_C", 0), pays("clm_C", 9500), accum("OOP_INN_IND", jordan, 500000)},
			},
		},
	},

	// ----------------------------------------------------------------- Pends
	{
		ID: "prior-auth", Group: "Pends",
		Title:   "Prior authorization pend, cleared by an examiner",
		Summary: "A pended claim posts nothing. Clearing the pend reprocesses it.",
		Steps: []Step{
			{
				Label:  "Sam has an MRI with no authorization on the claim",
				Why:    "Imaging requires prior auth, so the claim pends. Nothing is posted to accumulators while a claim is pended.",
				Action: process(mri("clm_A", sam, "", "")),
				Checks: []Check{status("clm_A", "PENDED"), pends("clm_A", "PRIOR_AUTH_REQUIRED"), accum("DED_INN_IND", sam, 0)},
			},
			{
				Label:  "Examiner confirms the authorization and reprocesses",
				Why:    "Prior auth is a judgment call the examiner can override. The claim is re-run (run 2), finalizes, and only now posts to the deductible. The first run is kept as history.",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Auth PA-7731 confirmed by phone", Resolve: []sim.Resolve{{Code: "PRIOR_AUTH_REQUIRED", Line: 1}}},
				Checks: []Check{status("clm_A", "APPROVED"), run("clm_A", 2), owes("clm_A", 42000), accum("DED_INN_IND", sam, 42000)},
			},
		},
	},
	{
		ID: "late-enrollment", Group: "Pends",
		Title:   "Claim arrives before the enrollment file",
		Summary: "A data pend cannot be overridden; it clears when the data arrives.",
		Steps: []Step{
			{
				Label:  "Newborn Avery has an office visit; Avery is not enrolled yet",
				Why:    "A member with no coverage yet pends rather than denies, because enrollment often lags the first claims.",
				Action: process(office("clm_A", avery, "2026-03-20")),
				Checks: []Check{status("clm_A", "PENDED"), pends("clm_A", "NO_ELIGIBILITY")},
			},
			{
				Label:  "Reprocess without anything having changed",
				Why:    "The pend is caused by missing data, so it simply comes back (run 2).",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Retry"},
				Checks: []Check{status("clm_A", "PENDED"), run("clm_A", 2)},
			},
			{
				Label:  "The enrollment file adds Avery from their date of birth",
				Why:    "Loading eligibility does not touch claims by itself.",
				Action: sim.Action{Type: "setCoverage", MemberID: avery, Start: "2026-03-02"},
				Checks: []Check{status("clm_A", "PENDED")},
			},
			{
				Label:  "Reprocess now that coverage exists",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Enrollment file loaded"},
				Checks: []Check{status("clm_A", "APPROVED"), run("clm_A", 3), pays("clm_A", 6500)},
			},
		},
	},
	{
		ID: "no-benefit", Group: "Pends",
		Title:   "Service the plan design does not cover",
		Summary: "No service category matches, so the plan's default (pend) applies. Shows a gap: nothing can deny a pended claim by hand yet.",
		Steps: []Step{
			{
				Label:  "Jordan has physical therapy (97110); the plan has no category for it",
				Action: process(sim.ClaimSpec{ID: "clm_A", Member: jordan, Procedure: "97110", Charge: 12000}),
				Checks: []Check{status("clm_A", "PENDED"), pends("clm_A", "NO_BENEFIT_MATCH")},
			},
			{
				Label:  "Examiner tries to clear the pend",
				Why:    "This pend is about the plan design, not a judgment call, so clearing it has no effect. The plan needs a benefit for the service, or the claim needs to be denied by hand, which the API does not support yet.",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Please pay", Resolve: []sim.Resolve{{Code: "NO_BENEFIT_MATCH", Line: 1}}},
				Checks: []Check{status("clm_A", "PENDED"), logged("caused by data")},
			},
		},
	},
	{
		ID: "duplicate", Group: "Pends",
		Title:   "Possible duplicate claim",
		Summary: "Same member, provider, date and service as an existing claim.",
		Steps: []Step{
			{
				Label:  "Jordan's office visit is processed",
				Action: process(office("clm_A", jordan, "")),
				Checks: []Check{status("clm_A", "APPROVED")},
			},
			{
				Label:  "An identical claim arrives",
				Why:    "It may be a true duplicate or a second visit the same day, so it pends for an examiner instead of paying twice.",
				Action: process(office("clm_B", jordan, "")),
				Checks: []Check{status("clm_B", "PENDED"), pends("clm_B", "POSSIBLE_DUPLICATE"), accum("OOP_INN_IND", jordan, 3000)},
			},
			{
				Label:  "Examiner confirms two separate visits",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_B", Reason: "Two separate visits confirmed with the office", Resolve: []sim.Resolve{{Code: "POSSIBLE_DUPLICATE"}}},
				Checks: []Check{status("clm_B", "APPROVED"), pays("clm_B", 6500), accum("OOP_INN_IND", jordan, 6000)},
			},
		},
	},
	{
		ID: "reprocess-finalized", Group: "Pends",
		Title:   "Reprocessing a finalized claim is refused",
		Summary: "Only pended claims are reprocessed; finalized claims change through adjustments.",
		Steps: []Step{
			{
				Label:  "Jordan's office visit is processed and finalized",
				Action: process(office("clm_A", jordan, "")),
				Checks: []Check{status("clm_A", "APPROVED")},
			},
			{
				Label:    "Try to reprocess it",
				Action:   sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Run it again"},
				Rejected: "only PENDED claims can be reprocessed",
				Checks:   []Check{run("clm_A", 1)},
			},
		},
	},

	// ----------------------------------------------------------- Adjustments
	{
		ID: "adjust-unpaid", Group: "Adjustments",
		Title:   "Adjust a claim that is approved but not yet paid",
		Summary: "The replacement takes the original's place; there is nothing to recover.",
		Steps: []Step{
			{
				Label:  "Jordan's visit is billed as a longer visit (99214)",
				Action: process(longVisit("clm_A", jordan)),
				Checks: []Check{pays("clm_A", 10500)},
			},
			{
				Label:  "The office sends a correction: it was a standard visit (99213)",
				Why:    "A new claim clm_A-2 replaces clm_A. The original's accumulator postings are reversed and the replacement's posted in one write, so the copay is counted once.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Provider billed the wrong visit level", Claim: toOffice},
				Checks: []Check{
					status("clm_A", "ADJUSTED"), status("clm_A-2", "APPROVED"), pays("clm_A-2", 6500),
					delta("APPROVED_UNPAID", 10500, 6500, -4000), accum("OOP_INN_IND", jordan, 3000),
				},
			},
		},
	},
	{
		ID: "adjust-paid", Group: "Adjustments",
		Title:   "Adjust a claim that has been paid",
		Summary: "The delta is measured against what was actually paid, and recovered on the next remittance.",
		Steps: []Step{
			{Label: "Jordan's visit is billed as 99214", Action: process(longVisit("clm_A", jordan)), Checks: []Check{pays("clm_A", 10500)}},
			{Label: "The payment run selects it", Action: selectAll, Checks: []Check{payment("clm_A", "SELECTED")}},
			{Label: "The run is released: $105 goes to the provider", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED"), status("clm_A", "APPROVED")}},
			{
				Label:  "The bank confirms the payment cleared",
				Why:    "Payment events never change the claim's status.",
				Action: sim.Action{Type: "payment", ClaimID: "clm_A", Event: "clear"},
				Checks: []Check{payment("clm_A", "CLEARED"), status("clm_A", "APPROVED")},
			},
			{
				Label:  "Correct it to 99213",
				Why:    "The provider holds $105; the replacement pays $65. The $40 difference is a recovery.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Provider billed the wrong visit level", Claim: toOffice},
				Checks: []Check{delta("PAYMENT", 10500, 6500, -4000), status("clm_A", "ADJUSTED"), payment("clm_A", "CLEARED")},
			},
			{Label: "The next payment run selects the replacement", Action: selectAll, Checks: []Check{payment("clm_A-2", "SELECTED")}},
			{
				Label:  "Release the run",
				Why:    "The remittance reverses the original and pays the replacement, netting to a $40 recovery.",
				Action: releaseRun,
				Checks: []Check{payment("clm_A-2", "ISSUED"), logged("net to the provider is -$40.00")},
			},
		},
	},
	{
		ID: "ripple", Group: "Adjustments",
		Title:   "An adjustment changes what a later claim should have paid",
		Summary: "Later claims are flagged for review, not changed automatically. Also shows that the flagging rule is broad.",
		Steps: []Step{
			{
				Label:  "Jordan: surgery, $1,200 to the deductible",
				Action: process(surgery("clm_A", jordan, "")),
				Checks: []Check{accum("DED_INN_IND", jordan, 120000)},
			},
			{
				Label:  "Jordan: MRI. Only $300 of deductible is left, so the plan pays $96",
				Action: process(mri("clm_B", jordan, "2026-03-12", "PA-1001")),
				Checks: []Check{pays("clm_B", 9600)},
			},
			{
				Label:  "The surgery claim was wrong: it was an office visit",
				Why:    "The replacement posts a $30 copay and no deductible. The $1,200 comes off Jordan's deductible, so clm_B was decided against a balance that is no longer true. clm_B is flagged, not changed.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Wrong procedure code: this was an office visit", Claim: toOffice},
				Checks: []Check{status("clm_A-2", "APPROVED"), accum("DED_INN_IND", jordan, 30000), openFlags("clm_B", 1), pays("clm_B", 9600)},
			},
			{
				Label:  "Examiner dry-runs an adjustment of the flagged MRI",
				Why:    "Re-adjudicated against today's balances, the whole $420 goes to the deductible and the plan pays nothing.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_B", DryRun: true, Reason: "Earlier claim adjusted"},
				Checks: []Check{delta("APPROVED_UNPAID", 9600, 0, -9600), status("clm_B", "APPROVED")},
			},
			{
				Label:  "Examiner adjusts the MRI",
				Why:    "The broad flagging rule now flags clm_A-2, because it was finalized after clm_B and clm_B's postings changed.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_B", Reason: "Deductible no longer met by the earlier claim"},
				Checks: []Check{
					status("clm_B", "ADJUSTED"), pays("clm_B-2", 0), owes("clm_B-2", 42000),
					accum("DED_INN_IND", jordan, 42000), accum("OOP_INN_IND", jordan, 45000), openFlags("clm_A-2", 1),
				},
			},
			{
				Label:  "Examiner clears the flag on the office visit",
				Why:    "A flat copay does not depend on the deductible, so nothing needs to change. This is the noise the broad rule costs.",
				Action: sim.Action{Type: "clearFlag", ClaimID: "clm_A-2", Reason: "Copay only; not affected by the deductible"},
				Checks: []Check{openFlags("clm_A-2", 0)},
			},
		},
	},
	{
		ID: "replacement-pends", Group: "Adjustments",
		Title:   "The replacement claim itself pends",
		Summary: "The original stays in force but cannot be paid until the replacement is resolved.",
		Steps: []Step{
			{Label: "Jordan's office visit is processed", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{
				Label:  "Corrected claim: it was an MRI, with no authorization",
				Why:    "The replacement pends for prior auth. The original is not retired yet and its accumulator postings stay, but it goes on payment hold so it cannot be paid in the meantime.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Corrected claim from provider", Claim: &sim.ClaimSpec{Procedure: "70551", Charge: 90000}},
				Checks: []Check{status("clm_A-2", "PENDED"), status("clm_A", "APPROVED"), hold("clm_A", true), accum("OOP_INN_IND", jordan, 3000)},
			},
			{
				Label:    "The payment run finds nothing to pay",
				Action:   selectAll,
				Rejected: "no claims are eligible",
				Checks:   []Check{noPayment("clm_A")},
			},
			{
				Label:  "Examiner confirms the authorization on the replacement",
				Why:    "Now the replacement finalizes: the original becomes ADJUSTED, its copay is reversed and the MRI's deductible is posted.",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A-2", Reason: "Authorization confirmed", Resolve: []sim.Resolve{{Code: "PRIOR_AUTH_REQUIRED", Line: 1}}},
				Checks: []Check{
					status("clm_A-2", "APPROVED"), status("clm_A", "ADJUSTED"), hold("clm_A", false),
					accum("DED_INN_IND", jordan, 42000), accum("OOP_INN_IND", jordan, 42000),
				},
			},
		},
	},
	{
		ID: "retro-termination", Group: "Adjustments",
		Title:   "Retroactive termination after a claim was paid",
		Summary: "Eligibility changes do not touch finalized claims; an adjustment re-adjudicates against current data.",
		Steps: []Step{
			{Label: "Sam's office visit on March 10 is processed", Action: process(office("clm_A", sam, "")), Checks: []Check{pays("clm_A", 6500)}},
			{Label: "It is selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:  "Enrollment update: Sam's coverage ended February 28",
				Why:    "The claim is finalized, so it is not touched.",
				Action: sim.Action{Type: "setCoverage", MemberID: sam, Start: "2026-01-01", End: "2026-02-28"},
				Checks: []Check{status("clm_A", "APPROVED"), pays("clm_A", 6500)},
			},
			{
				Label:  "Adjust the claim with no changes, to re-adjudicate it",
				Why:    "The replacement denies because coverage ended before the date of service. The $65 paid is a recovery and Sam's copay comes off the out-of-pocket accumulator.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Retroactive termination effective 2026-02-28"},
				Checks: []Check{
					status("clm_A-2", "DENIED"), denied("clm_A-2", "COVERAGE_TERMINATED"), status("clm_A", "ADJUSTED"),
					delta("PAYMENT", 6500, 0, -6500), accum("OOP_INN_IND", sam, 0),
				},
			},
		},
	},
	{
		ID: "fee-schedule-fix", Group: "Adjustments",
		Title:   "Fee schedule loaded wrong: measure, then correct",
		Summary: "Dry-run adjustments show the savings from the claims alone, before anything changes.",
		Steps: []Step{
			{Label: "Jordan's office visit pays $65 at the $95 rate", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{Label: "Sam's office visit pays $65", Action: process(office("clm_B", sam, "")), Checks: []Check{pays("clm_B", 6500)}},
			{Label: "Both are selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED"), payment("clm_B", "ISSUED")}},
			{
				Label:  "The contracted rate for 99213 should have been $85",
				Action: sim.Action{Type: "setFee", Procedure: "99213", Cents: 8500},
				Checks: []Check{pays("clm_A", 6500)},
			},
			{
				Label:  "Dry-run an adjustment of Jordan's claim",
				Why:    "The projection and the delta come from the claim's own payment record; nothing is saved.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", DryRun: true, Reason: "Fee schedule correction"},
				Checks: []Check{delta("PAYMENT", 6500, 5500, -1000), status("clm_A", "APPROVED")},
			},
			{
				Label:  "Adjust Jordan's claim",
				Why:    "The copay is unchanged, so accumulator postings are identical and no later claims are flagged.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Fee schedule correction"},
				Checks: []Check{pays("clm_A-2", 5500), openFlags("clm_B", 0), accum("OOP_INN_IND", jordan, 3000)},
			},
			{
				Label:  "Adjust Sam's claim",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_B", Reason: "Fee schedule correction"},
				Checks: []Check{delta("PAYMENT", 6500, 5500, -1000), pays("clm_B-2", 5500)},
			},
		},
	},
	{
		ID: "void-paid", Group: "Adjustments",
		Title:   "Void a paid claim",
		Summary: "The claim is cancelled, its accumulators are restored, and later claims are flagged.",
		Steps: []Step{
			{Label: "Jordan: surgery, $1,200 to the deductible", Action: process(surgery("clm_A", jordan, ""))},
			{Label: "Jordan: MRI, plan pays $96", Action: process(mri("clm_B", jordan, "2026-03-12", "PA-1001")), Checks: []Check{pays("clm_B", 9600)}},
			{Label: "Both are selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun},
			{
				Label:  "The surgery never happened: void the claim",
				Why:    "A void claim (clm_A-2) is created, finalized with nothing payable so the next remittance reports the reversal. The $1,200 comes off the deductible and the MRI is flagged.",
				Action: sim.Action{Type: "void", ClaimID: "clm_A", Reason: "Billed in error: service not rendered"},
				Checks: []Check{
					status("clm_A", "VOIDED"), status("clm_A-2", "APPROVED"), pays("clm_A-2", 0),
					accum("DED_INN_IND", jordan, 30000), openFlags("clm_B", 1),
				},
			},
		},
	},

	// -------------------------------------------------------------- Payments
	{
		ID: "blocked-while-selected", Group: "Payments",
		Title:   "Claim found to be wrong while in a payment run",
		Summary: "Adjustments are refused while payment is in flight. Dry-run, pull the claim, then adjust.",
		Steps: []Step{
			{Label: "Jordan's visit is billed as 99214", Action: process(longVisit("clm_A", jordan))},
			{Label: "The payment run selects it", Action: sim.Action{Type: "select", ClaimID: "clm_A"}, Checks: []Check{payment("clm_A", "SELECTED")}},
			{
				Label:    "Try to adjust it",
				Why:      "If this were allowed, the original payment could go out while the replacement also pays.",
				Action:   sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Wrong visit level", Claim: toOffice},
				Rejected: "selected for a payment run",
				Checks:   []Check{status("clm_A", "APPROVED")},
			},
			{
				Label:  "A dry run is still allowed",
				Why:    "It changes nothing, and answers “is it worth pulling this from the run?”",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", DryRun: true, Reason: "Wrong visit level", Claim: toOffice},
				Checks: []Check{delta("APPROVED_UNPAID", 10500, 6500, -4000), payment("clm_A", "SELECTED")},
			},
			{
				Label:  "Pull it from the run",
				Why:    "The claim goes back to unpaid with a payment hold, so the next run does not pick it up again before it is fixed.",
				Action: sim.Action{Type: "pull", ClaimID: "clm_A", Reason: "Wrong visit level; do not pay"},
				Checks: []Check{noPayment("clm_A"), hold("clm_A", true)},
			},
			{
				Label:    "The next payment run skips it",
				Action:   selectAll,
				Rejected: "no claims are eligible",
			},
			{
				Label:  "Now adjust it",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Wrong visit level", Claim: toOffice},
				Checks: []Check{status("clm_A", "ADJUSTED"), hold("clm_A", false), status("clm_A-2", "APPROVED")},
			},
			{Label: "The payment run selects the replacement", Action: selectAll, Checks: []Check{payment("clm_A-2", "SELECTED")}},
			{Label: "Release the run", Action: releaseRun, Checks: []Check{payment("clm_A-2", "ISSUED"), noPayment("clm_A")}},
		},
	},
	{
		ID: "returned-payment", Group: "Payments",
		Title:   "Payment bounces, then the claim is adjusted",
		Summary: "A failed payment does not change the claim. The adjustment delta is measured against what the provider actually holds: nothing.",
		Steps: []Step{
			{Label: "Jordan's office visit is processed", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{Label: "It is selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:  "The bank returns the payment (closed account)",
				Why:    "The decision was right; only delivery failed. The claim stays APPROVED.",
				Action: sim.Action{Type: "payment", ClaimID: "clm_A", Event: "return"},
				Checks: []Check{payment("clm_A", "RETURNED"), status("clm_A", "APPROVED")},
			},
			{
				Label:  "Dry-run a correction to a longer visit (99214)",
				Why:    "The provider holds nothing, so the delta is the replacement's full $105, not $105 minus $65.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", DryRun: true, Reason: "Visit level corrected", Claim: &sim.ClaimSpec{Procedure: "99214", Charge: 25000}},
				Checks: []Check{delta("PAYMENT_NOT_HELD", 0, 10500, 10500)},
			},
			{
				Label:  "Adjust it",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Visit level corrected", Claim: &sim.ClaimSpec{Procedure: "99214", Charge: 25000}},
				Checks: []Check{status("clm_A", "ADJUSTED"), pays("clm_A-2", 10500)},
			},
			{
				Label:    "Try to reissue the original's bounced payment",
				Why:      "The original has been replaced, so its old amount must not be sent again.",
				Action:   sim.Action{Type: "payment", ClaimID: "clm_A", Event: "reissue"},
				Rejected: "its payment is not reissued",
				Checks:   []Check{payment("clm_A", "RETURNED")},
			},
		},
	},
}
