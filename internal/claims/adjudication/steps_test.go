package adjudication_test

import (
	"os"
	"strings"
	"testing"

	"coreadmin/internal/claims/adjudication"
)

// Every pend and denial the engine can return is on some step of the map.
func TestEveryOutcomeIsOnAStep(t *testing.T) {
	onStep := map[string]bool{}
	for _, s := range adjudication.Steps {
		for _, c := range append(append([]string{}, s.Pends...), s.Denies...) {
			onStep[c] = true
		}
	}
	src, err := os.ReadFile("engine.go")
	if err != nil {
		t.Fatal(err)
	}
	more, _ := os.ReadFile("remarks.go")
	src = append(src, more...)
	for _, code := range []string{
		adjudication.PendMemberNotFound, adjudication.PendNoPlanVersion, adjudication.PendNoTier, adjudication.PendPossibleDuplicate,
		adjudication.PendOriginalNotFound, adjudication.PendOriginalNotFinalized, adjudication.PendOriginalInPaymentRun, adjudication.PendOpenReplacement,
		adjudication.PendRemarkCodeRequired, adjudication.PendNoEligibility, adjudication.PendCoverageInGrace, adjudication.PendNoBenefitMatch,
		adjudication.PendPriorAuthRequired, adjudication.PendNoPrice,
		adjudication.DenyCoverageTerminated, adjudication.DenyProviderSanctioned, adjudication.DenyNotCovered,
	} {
		if !onStep[code] {
			t.Errorf("%s isn't on any step", code)
		}
	}
	// Every Pend*/Deny* constant the engine uses is in the list above.
	for _, word := range strings.FieldsFunc(string(src), func(r rune) bool {
		return !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if (strings.HasPrefix(word, "Pend") || strings.HasPrefix(word, "Deny")) && len(word) > 4 && word[4] >= 'A' && word[4] <= 'Z' && !strings.HasSuffix(word, "Error") {
			if _, ok := codes[word]; !ok {
				t.Errorf("engine uses %s, which the map doesn't list", word)
			}
		}
	}
}

var codes = map[string]bool{
	"PendMemberNotFound": true, "PendNoPlanVersion": true, "PendNoTier": true, "PendPossibleDuplicate": true,
	"PendOriginalNotFound": true, "PendOriginalNotFinalized": true, "PendOriginalInPaymentRun": true, "PendOpenReplacement": true,
	"PendRemarkCodeRequired": true, "PendNoEligibility": true, "PendCoverageInGrace": true, "PendNoBenefitMatch": true,
	"PendPriorAuthRequired": true, "PendNoPrice": true,
	"DenyCoverageTerminated": true, "DenyProviderSanctioned": true, "DenyNotCovered": true,
}
