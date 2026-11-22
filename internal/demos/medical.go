package demos

// Medical management components: usually contracted out by a TPA; out of
// scope for '28.
const (
	MedicalPolicy = "medical-policy" // criteria, auth-required lists
	Authorization = "authorization"  // prior auth, UM review; matching auths to claims is adjudication's
)

var medicalDemos []Demo
