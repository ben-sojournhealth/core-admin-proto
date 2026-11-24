package demos

// Finance components.
const (
	// Moving the money: employer funding, EFT, checks, reconciliation.
	Disbursement = "disbursement"
	// Working an overpayment until it is resolved: notice, offset (taken by
	// Settlement) or refund, disputes, aging, write-off.
	Recoveries = "recoveries"
	StopLoss   = "stop-loss"
)

var financeDemos = []Demo{
	{
		ID: "disbursement", Title: "Disbursement", Status: Planned,
		Summary: "Employer funding, EFT and checks, and bank reconciliation.",
		Links:   []Link{{Component: Disbursement}},
	},
	{
		ID: "recoveries", Title: "Recoveries", Status: Planned,
		Summary: "Overpayments worked to resolution: notice, offset or refund, disputes and write-off.",
		Links:   []Link{{Component: Recoveries}},
	},
	{
		ID: "stop-loss", Title: "Stop-Loss", Status: Planned,
		Summary: "Specific and aggregate exposure, lasers, contract windows and filings.",
		Links:   []Link{{Component: StopLoss}},
	},
}
