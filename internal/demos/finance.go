package demos

// Finance components.
const (
	// Moving the money: employer funding, EFT, checks, reconciliation.
	Disbursement = "disbursement"
	StopLoss     = "stop-loss"
)

var financeDemos = []Demo{
	{
		ID: "disbursement", Title: "Disbursement", Status: Planned,
		Summary: "Employer funding, EFT and checks, and bank reconciliation.",
		Links:   []Link{{Component: Disbursement}},
	},
	{
		ID: "stop-loss", Title: "Stop-Loss", Status: Planned,
		Summary: "Specific and aggregate exposure, lasers, contract windows and filings.",
		Links:   []Link{{Component: StopLoss}},
	},
}
