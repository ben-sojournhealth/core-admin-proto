package eligibility

// Role is what a sender is to a customer's enrollment.
type Role int

const (
	// The plan sponsor (the employer, or its HR or benefits platform):
	// active employees and their dependents.
	Employer Role = iota + 1
	// A COBRA administrator: continuees, and only their COBRA coverage.
	COBRAAdministrator
)

func (r Role) String() string {
	return map[Role]string{Employer: "employer", COBRAAdministrator: "COBRA administrator"}[r]
}

// SenderSetup is one sender allowed to send a customer's enrollment.
type SenderSetup struct {
	// ISA06 of its files.
	Sender string
	Role   Role
}

// enrollmentSenders lists, per customer, who may send enrollment and as
// what. A customer with no COBRA administrator runs COBRA itself, on its
// own 834s. A COBRA administrator usually serves many customers, and
// customers may use different ones. Set up when a client is onboarded.
var enrollmentSenders = map[string][]SenderSetup{
	"cust_acme":       {{Sender: "ACMEHR", Role: Employer}, {Sender: "COBRAVND", Role: COBRAAdministrator}},
	"cust_brightline": {{Sender: "BRIGHTHR", Role: Employer}, {Sender: "COBRAVND", Role: COBRAAdministrator}},
}

// roleOf is what sender is to a customer, if anything.
func roleOf(customerID, sender string) (Role, bool) {
	for _, s := range enrollmentSenders[customerID] {
		if s.Sender == sender {
			return s.Role, true
		}
	}
	return 0, false
}

// cobraAdministrator is the sender that runs a customer's COBRA; empty
// when the customer runs it itself.
func cobraAdministrator(customerID string) string {
	for _, s := range enrollmentSenders[customerID] {
		if s.Role == COBRAAdministrator {
			return s.Sender
		}
	}
	return ""
}
