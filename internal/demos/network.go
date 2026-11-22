package demos

// Network components. Contracting, credentialing and roster intake are out
// of scope for '28.
const (
	NetworkVendors = "network-vendors" // network and provider files from vendors
	Rosters        = "rosters"         // providers' rosters and contracts
	VendorIntake   = "vendor-intake"   // network vendor files
	RosterIntake   = "roster-intake"   // rosters
	ProviderData   = "provider-data"   // provider data management, directory
	Contracting    = "contracting"     // provider contracts; pricing them is adjudication's
	Credentialing  = "credentialing"   // verifying providers before they join a network
	// Claim status, disputes and (out of scope for '28) prior auth requests
	// from providers.
	ProviderPortal = "provider-portal"
)

var networkDemos = []Demo{
	{
		ID: "provider-data", Title: "Provider Data", Status: Planned,
		Summary: "Roster loads, network participation, contracting and the provider directory.",
		Links:   []Link{{Component: NetworkVendors}, {Component: Rosters}, {Component: VendorIntake}, {Component: RosterIntake}, {Component: ProviderData}, {Component: Contracting}, {Component: Credentialing}},
	},
}
