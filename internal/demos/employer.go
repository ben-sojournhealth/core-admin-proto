package demos

// Employer components.
const (
	Enrollment       = "enrollment"        // 834 files from employers
	EnrollmentIntake = "enrollment-intake" // 834
	EmployerPortal   = "employer-portal"
	PlanDesign       = "plan-design"
	Eligibility      = "eligibility"
	// Who is covered, sent to each outside processor (PBM, behavioral, dental,
	// vision) so it can adjudicate its claims.
	EligibilityFeed = "eligibility-feed"
	// Funding arrangements, funding requests and claims registers, and
	// admin-fee billing.
	BillingFunding = "billing-funding"
)

var employerDemos = []Demo{
	{
		ID: "enrollment-intake", Title: "Enrollment Intake", Status: Live, Path: "/enrollment-intake/",
		Summary: "834 files from employers: the 999 for each transaction set, and each member loop exactly as mapped from the file.",
		Links:   []Link{{Component: Enrollment}, {Component: EnrollmentIntake}},
	},
	{
		ID: "eligibility", Title: "Eligibility", Status: Live, Path: "/eligibility/",
		Summary: "Members derived from every 834 input: each version as it was believed, flip-flops, full files with an approval gate, regeneration.",
		Links:   []Link{{Component: Eligibility}},
	},
	{
		ID: "plan-design", Title: "Plan Design", Status: Planned,
		Summary: "Configure a customer's benefits and test the design against claims.",
		Links:   []Link{{Component: PlanDesign}},
	},
}
