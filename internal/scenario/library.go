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
				outcome("clm_A", "APPROVED"), allowed("clm_A", 9500), pays("clm_A", 6500), owes("clm_A", 3000),
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
		Title:   "Cost estimate",
		Summary: "Run a claim through adjudication without saving it or touching accumulators.",
		Steps: []Step{{
			Label:  "Estimate an MRI for Jordan",
			Why:    "An estimate returns the full decision but saves nothing: no claim, no accumulator postings.",
			Action: sim.Action{Type: "process", Preview: true, Claim: &sim.ClaimSpec{ID: "clm_EST", Member: jordan, Procedure: "70551", Charge: 90000, Auth: "PA-1001"}},
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
				Why:    "Prior auth is a judgment call the examiner can override. The claim is re-run (run 2), is adjudicated, and only now posts to the deductible. The first run is kept as history.",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Auth PA-7731 confirmed by phone", Resolve: []sim.Resolve{{Code: "PRIOR_AUTH_REQUIRED", Line: 1}}},
				Checks: []Check{outcome("clm_A", "APPROVED"), run("clm_A", 2), owes("clm_A", 42000), accum("DED_INN_IND", sam, 42000)},
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
				Checks: []Check{outcome("clm_A", "APPROVED"), run("clm_A", 3), pays("clm_A", 6500)},
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
				Checks: []Check{outcome("clm_A", "APPROVED")},
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
				Checks: []Check{outcome("clm_B", "APPROVED"), pays("clm_B", 6500), accum("OOP_INN_IND", jordan, 6000)},
			},
		},
	},
	{
		ID: "reprocess-adjudicated", Group: "Pends",
		Title:   "Reprocessing an adjudicated claim is refused",
		Summary: "Only pended claims are reprocessed; adjudicated claims change through adjustments.",
		Steps: []Step{
			{
				Label:  "Jordan's office visit is adjudicated",
				Action: process(office("clm_A", jordan, "")),
				Checks: []Check{outcome("clm_A", "APPROVED")},
			},
			{
				Label:    "Try to reprocess it",
				Action:   sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Run it again"},
				Rejected: "only PENDED claims can be reprocessed",
				Checks:   []Check{run("clm_A", 1)},
			},
		},
	},

	// --------------------------------------------------------- Before payment
	{
		ID: "reopen-before-payment", Group: "Before payment",
		Title:   "Reopen a claim before it is paid",
		Summary: "Nothing has been reported yet, so the same claim is re-run in place. No new version.",
		Steps: []Step{
			{Label: "Jordan's office visit pays $65 at the $95 rate", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{
				Label:  "The contracted rate for 99213 should have been $85",
				Why:    "The claim is adjudicated, so the corrected rate does not touch it.",
				Action: sim.Action{Type: "setFee", Procedure: "99213", Cents: 8500},
				Checks: []Check{pays("clm_A", 6500)},
			},
			{
				Label:    "Try to adjust it",
				Why:      "An adjustment reverses what an 835 reported. Nothing has been reported for this claim.",
				Action:   sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Fee schedule correction"},
				Rejected: "has not been released for payment",
			},
			{
				Label:  "Reopen it",
				Why:    "The claim goes back to pended. Its copay comes off the out-of-pocket accumulator, and the first result is kept as history.",
				Action: sim.Action{Type: "reopen", ClaimID: "clm_A", Reason: "Fee schedule correction"},
				Checks: []Check{status("clm_A", "PENDED"), run("clm_A", 2), accum("OOP_INN_IND", jordan, 0)},
			},
			{
				Label:  "Reprocess it against the corrected rate",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Fee schedule corrected"},
				Checks: []Check{outcome("clm_A", "APPROVED"), pays("clm_A", 5500), run("clm_A", 3), accum("OOP_INN_IND", jordan, 3000), noClaim("clm_A-2")},
			},
			{Label: "The payment run selects it", Action: selectAll, Checks: []Check{payment("clm_A", "SELECTED")}},
			{Label: "Release the run: one result, reported once", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
		},
	},
	{
		ID: "corrected-before-payment", Group: "Before payment",
		Title:   "The provider corrects a claim before it is paid",
		Summary: "The original is closed with an answer of its own; the corrected claim is decided as a claim in its own right. Nothing is superseded.",
		Steps: []Step{
			{
				Label:  "Jordan's visit is billed as a longer visit (99214)",
				Action: process(longVisit("clm_A", jordan)),
				Checks: []Check{pays("clm_A", 10500)},
			},
			{
				Label:  "The office sends a corrected claim: it was a standard visit (99213)",
				Why:    "clm_A was never reported, so there is nothing to reverse. It is denied as replaced by the provider, its copay comes off the accumulator, and clm_B is decided on its own.",
				Action: sim.Action{Type: "corrected", ClaimID: "clm_A", Claim: &sim.ClaimSpec{ID: "clm_B", Procedure: "99213", Charge: 18000}},
				Checks: []Check{
					outcome("clm_A", "DENIED"), denied("clm_A", "REPLACED_BY_PROVIDER"), pays("clm_A", 0),
					outcome("clm_B", "APPROVED"), pays("clm_B", 6500), accum("OOP_INN_IND", jordan, 3000),
				},
			},
			{Label: "The payment run selects both", Action: selectAll, Checks: []Check{payment("clm_A", "SELECTED"), payment("clm_B", "SELECTED")}},
			{
				Label:  "Release the run",
				Why:    "Each claim the provider sent gets one answer: clm_A denied, clm_B paid.",
				Action: releaseRun,
				Checks: []Check{payment("clm_A", "ISSUED"), payment("clm_B", "ISSUED")},
			},
		},
	},
	{
		ID: "void-before-payment", Group: "Before payment",
		Title:   "The provider voids a claim before it is paid",
		Summary: "The original is closed as denied; the void has nothing to reverse.",
		Steps: []Step{
			{Label: "Jordan's office visit is processed", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{
				Label:  "The office sends a void: the visit never happened",
				Action: sim.Action{Type: "providerVoid", ClaimID: "clm_A", Claim: &sim.ClaimSpec{ID: "clm_B"}},
				Checks: []Check{
					outcome("clm_A", "DENIED"), denied("clm_A", "VOIDED_BY_PROVIDER"), outcome("clm_B", "VOID"),
					accum("OOP_INN_IND", jordan, 0),
				},
			},
		},
	},

	// ----------------------------------------------------------- Adjustments
	{
		ID: "corrected-after-payment", Group: "Adjustments",
		Title:   "The provider corrects a claim after it is paid",
		Summary: "The result was reported, so the corrected claim supersedes it and the next 835 reverses it.",
		Steps: []Step{
			{Label: "Jordan's visit is billed as 99214", Action: process(longVisit("clm_A", jordan)), Checks: []Check{pays("clm_A", 10500)}},
			{Label: "The payment run selects it", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:  "The office sends a corrected claim: it was a standard visit (99213)",
				Why:    "clm_A keeps the outcome it was reported with and is marked superseded. The copay is counted once.",
				Action: sim.Action{Type: "corrected", ClaimID: "clm_A", Claim: &sim.ClaimSpec{ID: "clm_B", Procedure: "99213", Charge: 18000}},
				Checks: []Check{
					superseded("clm_A", "REPLACEMENT"), outcome("clm_A", "APPROVED"), outcome("clm_B", "APPROVED"), pays("clm_B", 6500),
					accum("OOP_INN_IND", jordan, 3000),
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
			{Label: "The run is released: $105 goes to the provider", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED"), outcome("clm_A", "APPROVED")}},
			{
				Label:  "The bank confirms the payment cleared",
				Why:    "Payment events never change the claim's status.",
				Action: sim.Action{Type: "payment", ClaimID: "clm_A", Event: "clear"},
				Checks: []Check{payment("clm_A", "CLEARED"), outcome("clm_A", "APPROVED")},
			},
			{
				Label:  "Correct it to 99213",
				Why:    "The provider holds $105; the replacement pays $65. The $40 difference is a recovery.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Provider billed the wrong visit level", Claim: toOffice},
				Checks: []Check{delta("PAYMENT", 10500, 6500, -4000), superseded("clm_A", "REPLACEMENT"), payment("clm_A", "CLEARED")},
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
		Summary: "Later claims are not changed automatically, and nothing yet tells anyone they may be wrong.",
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
			{Label: "Both are selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_B", "ISSUED")}},
			{
				Label:  "The surgery claim was wrong: it was an office visit",
				Why:    "The replacement posts a $30 copay and no deductible. The $1,200 comes off Jordan's deductible, so clm_B was decided against a balance that is no longer true. clm_B is not changed, and nothing marks it.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Wrong procedure code: this was an office visit", Claim: toOffice},
				Checks: []Check{outcome("clm_A-2", "APPROVED"), accum("DED_INN_IND", jordan, 30000), pays("clm_B", 9600)},
			},
			{
				Label:  "Examiner, knowing to look, previews an adjustment of the MRI",
				Why:    "Re-adjudicated against today's balances, the whole $420 goes to the deductible and the plan pays nothing.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_B", Preview: true, Reason: "Earlier claim adjusted"},
				Checks: []Check{delta("PAYMENT", 9600, 0, -9600), outcome("clm_B", "APPROVED")},
			},
			{
				Label:  "Examiner adjusts the MRI",
				Why:    "The MRI now agrees with the family's balances. Finding it took an examiner who knew to look.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_B", Reason: "Deductible no longer met by the earlier claim"},
				Checks: []Check{
					superseded("clm_B", "REPLACEMENT"), pays("clm_B-2", 0), owes("clm_B-2", 42000),
					accum("DED_INN_IND", jordan, 42000), accum("OOP_INN_IND", jordan, 45000),
				},
			},
		},
	},
	{
		ID: "replacement-pends", Group: "Adjustments",
		Title:   "The replacement claim itself pends",
		Summary: "The original stays in force, as reported, until the replacement is adjudicated.",
		Steps: []Step{
			{Label: "Jordan's office visit is processed", Action: process(office("clm_A", jordan, "")), Checks: []Check{pays("clm_A", 6500)}},
			{Label: "It is selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:  "Corrected claim: it was an MRI, with no authorization",
				Why:    "The replacement pends for prior auth. The original is not superseded yet and its accumulator postings stay.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Corrected claim from provider", Claim: &sim.ClaimSpec{Procedure: "70551", Charge: 90000}},
				Checks: []Check{status("clm_A-2", "PENDED"), outcome("clm_A", "APPROVED"), accum("OOP_INN_IND", jordan, 3000)},
			},
			{
				Label:    "The payment run finds nothing to pay",
				Action:   selectAll,
				Rejected: "no claims are eligible",
			},
			{
				Label:  "Examiner confirms the authorization on the replacement",
				Why:    "Now the replacement is adjudicated: the original is superseded, its copay is reversed and the MRI's deductible is posted.",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A-2", Reason: "Authorization confirmed", Resolve: []sim.Resolve{{Code: "PRIOR_AUTH_REQUIRED", Line: 1}}},
				Checks: []Check{
					outcome("clm_A-2", "APPROVED"), superseded("clm_A", "REPLACEMENT"),
					accum("DED_INN_IND", jordan, 42000), accum("OOP_INN_IND", jordan, 42000),
				},
			},
		},
	},
	{
		ID: "retro-termination", Group: "Adjustments",
		Title:   "Retroactive termination after a claim was paid",
		Summary: "Eligibility changes do not touch adjudicated claims; an adjustment re-adjudicates against current data.",
		Steps: []Step{
			{Label: "Sam's office visit on March 10 is processed", Action: process(office("clm_A", sam, "")), Checks: []Check{pays("clm_A", 6500)}},
			{Label: "It is selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:  "Enrollment update: Sam's coverage ended February 28",
				Why:    "The claim is adjudicated, so it is not touched.",
				Action: sim.Action{Type: "setCoverage", MemberID: sam, Start: "2026-01-01", End: "2026-02-28"},
				Checks: []Check{outcome("clm_A", "APPROVED"), pays("clm_A", 6500)},
			},
			{
				Label:  "Adjust the claim with no changes, to re-adjudicate it",
				Why:    "The replacement denies because coverage ended before the date of service. The $65 paid is a recovery and Sam's copay comes off the out-of-pocket accumulator.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Retroactive termination effective 2026-02-28"},
				Checks: []Check{
					outcome("clm_A-2", "DENIED"), denied("clm_A-2", "COVERAGE_TERMINATED"), superseded("clm_A", "REPLACEMENT"),
					delta("PAYMENT", 6500, 0, -6500), accum("OOP_INN_IND", sam, 0),
				},
			},
		},
	},
	{
		ID: "fee-schedule-fix", Group: "Adjustments",
		Title:   "Fee schedule loaded wrong: measure, then correct",
		Summary: "Previewing adjustments shows the savings from the claims alone, before anything changes.",
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
				Label:  "Preview an adjustment of Jordan's claim",
				Why:    "The projection and the delta come from the claim's own payment record; nothing is saved.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Preview: true, Reason: "Fee schedule correction"},
				Checks: []Check{delta("PAYMENT", 6500, 5500, -1000), outcome("clm_A", "APPROVED")},
			},
			{
				Label:  "Adjust Jordan's claim",
				Why:    "The copay is unchanged, so accumulator postings are identical and no later claim is affected.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Fee schedule correction"},
				Checks: []Check{pays("clm_A-2", 5500), accum("OOP_INN_IND", jordan, 3000)},
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
		Summary: "The claim is cancelled and its accumulators are restored. Later claims are left as they are.",
		Steps: []Step{
			{Label: "Jordan: surgery, $1,200 to the deductible", Action: process(surgery("clm_A", jordan, ""))},
			{Label: "Jordan: MRI, plan pays $96", Action: process(mri("clm_B", jordan, "2026-03-12", "PA-1001")), Checks: []Check{pays("clm_B", 9600)}},
			{Label: "Both are selected for payment", Action: selectAll},
			{Label: "The run is released", Action: releaseRun},
			{
				Label:  "The surgery never happened: void the claim",
				Why:    "A void claim (clm_A-2) is created, adjudicated with outcome VOID and nothing payable so the next remittance reports the reversal. The $1,200 comes off the deductible; the MRI, decided against the old balance, is left as it is.",
				Action: sim.Action{Type: "void", ClaimID: "clm_A", Reason: "Billed in error: service not rendered"},
				Checks: []Check{
					superseded("clm_A", "VOID"), outcome("clm_A-2", "VOID"), pays("clm_A-2", 0),
					accum("DED_INN_IND", jordan, 30000),
				},
			},
		},
	},

	// -------------------------------------------------------------- Payments
	{
		ID: "reopened-from-payment-run", Group: "Payments",
		Title:   "Claim found to be wrong while in a payment run",
		Summary: "Until the run is released nothing has been reported, so the claim is reopened, which takes it out of the run.",
		Steps: []Step{
			{Label: "Jordan's visit is billed as 99214", Action: process(longVisit("clm_A", jordan))},
			{Label: "The payment run selects it", Action: sim.Action{Type: "select", ClaimID: "clm_A"}, Checks: []Check{payment("clm_A", "SELECTED")}},
			{
				Label:    "Try to adjust it",
				Why:      "An adjustment is for a result that has been reported. This one hasn't.",
				Action:   sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Wrong visit level", Claim: toOffice},
				Rejected: "has not been released for payment",
				Checks:   []Check{outcome("clm_A", "APPROVED")},
			},
			{
				Label:  "Reopen it",
				Why:    "The claim leaves the run and goes back to pended, so it cannot be paid as it stood.",
				Action: sim.Action{Type: "reopen", ClaimID: "clm_A", Reason: "Visit level in question"},
				Checks: []Check{noPayment("clm_A"), status("clm_A", "PENDED"), accum("OOP_INN_IND", jordan, 0)},
			},
			{
				Label:    "The next payment run finds nothing to pay",
				Action:   selectAll,
				Rejected: "no claims are eligible",
			},
			{
				Label:  "The records confirm the visit level: reprocess it",
				Action: sim.Action{Type: "reprocess", ClaimID: "clm_A", Reason: "Records confirm the visit level"},
				Checks: []Check{outcome("clm_A", "APPROVED"), pays("clm_A", 10500), noClaim("clm_A-2")},
			},
			{Label: "The payment run selects it", Action: selectAll, Checks: []Check{payment("clm_A", "SELECTED")}},
			{Label: "Release the run", Action: releaseRun, Checks: []Check{payment("clm_A", "ISSUED")}},
			{
				Label:    "Now it has been reported: reopening is refused",
				Action:   sim.Action{Type: "reopen", ClaimID: "clm_A", Reason: "Second thoughts"},
				Rejected: "has been released for payment",
			},
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
				Why:    "The decision was right; only delivery failed. The claim stays adjudicated and approved.",
				Action: sim.Action{Type: "payment", ClaimID: "clm_A", Event: "return"},
				Checks: []Check{payment("clm_A", "RETURNED"), outcome("clm_A", "APPROVED")},
			},
			{
				Label:  "Preview a correction to a longer visit (99214)",
				Why:    "The provider holds nothing, so the delta is the replacement's full $105, not $105 minus $65.",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Preview: true, Reason: "Visit level corrected", Claim: &sim.ClaimSpec{Procedure: "99214", Charge: 25000}},
				Checks: []Check{delta("PAYMENT_NOT_HELD", 0, 10500, 10500)},
			},
			{
				Label:  "Adjust it",
				Action: sim.Action{Type: "adjust", ClaimID: "clm_A", Reason: "Visit level corrected", Claim: &sim.ClaimSpec{Procedure: "99214", Charge: 25000}},
				Checks: []Check{superseded("clm_A", "REPLACEMENT"), pays("clm_A-2", 10500)},
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
