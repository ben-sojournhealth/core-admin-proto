package adjudication_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	claimsv1 "coreadmin/gen/coreadmin/claims/v1"
	commonv1 "coreadmin/gen/coreadmin/common/v1"
	"coreadmin/internal/adjudication"
)

func cents(c int64) *commonv1.Money { return &commonv1.Money{Cents: c} }

// The seed claim's expected outcome: $180 charge, $95 allowed, $30 copay.
func officeVisit() *claimsv1.LineAdjudication {
	return &claimsv1.LineAdjudication{
		Status:  claimsv1.LineStatus_LINE_STATUS_PAYABLE,
		Charge:  cents(18000),
		Pricing: &claimsv1.Pricing{Source: claimsv1.PricingSource_PRICING_SOURCE_CONTRACT, Allowed: cents(9500)},
		CostShares: []*claimsv1.CostShareApplied{
			{Type: claimsv1.CostShareType_COST_SHARE_TYPE_COPAY, Amount: cents(3000)},
		},
		MemberResponsibility: cents(3000),
		Paid:                 cents(6500),
		CasAdjustments: []*claimsv1.CasAdjustment{
			{Group: claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL, ReasonCode: "45", Amount: cents(8500)},
			{Group: claimsv1.CasGroup_CAS_GROUP_PATIENT_RESPONSIBILITY, ReasonCode: "3", Amount: cents(3000)},
		},
	}
}

func TestCheckLineBalanced(t *testing.T) {
	denied := &claimsv1.LineAdjudication{
		Status: claimsv1.LineStatus_LINE_STATUS_DENIED,
		Denial: &claimsv1.Denial{Code: "NOT_COVERED"},
		Charge: cents(18000),
		Paid:   cents(0),
		CasAdjustments: []*claimsv1.CasAdjustment{
			{Group: claimsv1.CasGroup_CAS_GROUP_CONTRACTUAL, ReasonCode: "96", Amount: cents(18000)},
		},
	}
	mutate := func(f func(*claimsv1.LineAdjudication)) *claimsv1.LineAdjudication {
		a := proto.Clone(officeVisit()).(*claimsv1.LineAdjudication)
		f(a)
		return a
	}

	tests := []struct {
		name    string
		line    *claimsv1.LineAdjudication
		wantErr string
	}{
		{"payable balanced", officeVisit(), ""},
		{"denied balanced", denied, ""},
		{"pended unchecked", &claimsv1.LineAdjudication{Status: claimsv1.LineStatus_LINE_STATUS_PENDED}, ""},
		{"overpaid", mutate(func(a *claimsv1.LineAdjudication) { a.Paid = cents(7000) }), "allowed"},
		{"cost shares do not sum", mutate(func(a *claimsv1.LineAdjudication) { a.MemberResponsibility = cents(2000) }), "sum of cost shares"},
		{"CAS short", mutate(func(a *claimsv1.LineAdjudication) { a.CasAdjustments = a.CasAdjustments[:1] }), "CAS"},
		{"denied but paid", mutate(func(a *claimsv1.LineAdjudication) {
			a.Status = claimsv1.LineStatus_LINE_STATUS_DENIED
			a.Denial = &claimsv1.Denial{Code: "NOT_COVERED"}
		}), "denied line has paid"},
		{"no status", mutate(func(a *claimsv1.LineAdjudication) { a.Status = 0 }), "no status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := adjudication.CheckLineBalanced(tt.line)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}
