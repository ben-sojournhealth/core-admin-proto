package protodoc_test

import (
	"strings"
	"testing"

	"coreadmin/internal/common/protodoc"
)

func TestEnrollmentDocs(t *testing.T) {
	d := protodoc.For("coreadmin.eligibility.v1.EnrollmentTransaction")
	txn := d.Messages["coreadmin.eligibility.v1.EnrollmentTransaction"]
	if !strings.Contains(txn["relationship_code"].Doc, "INS02") {
		t.Errorf("relationship_code: %q", txn["relationship_code"].Doc)
	}
	if txn["header"].Message != "coreadmin.eligibility.v1.EnrollmentHeader" {
		t.Errorf("header type: %+v", txn["header"])
	}
	if !strings.Contains(d.Messages["coreadmin.eligibility.v1.EnrollmentSource"]["interchange_control_number"].Doc, "ISA13") {
		t.Errorf("source docs: %v", d.Messages["coreadmin.eligibility.v1.EnrollmentSource"])
	}
	if got := d.Enums["coreadmin.eligibility.v1.MaintenanceType"]["MAINTENANCE_TYPE_ADDITION"]; !strings.HasPrefix(got, "021:") {
		t.Errorf("enum value doc: %q", got)
	}
}

// Every field of the enrollment record, and of everything it uses, has an
// explanation for the tooltips.
func TestEveryEnrollmentFieldExplained(t *testing.T) {
	d := protodoc.For("coreadmin.eligibility.v1.EnrollmentTransaction")
	for m, fields := range d.Messages {
		if strings.HasPrefix(m, "google.protobuf.") {
			continue
		}
		for name, f := range fields {
			if f.Doc == "" {
				t.Errorf("%s.%s has no comment", m, name)
			}
		}
	}
}
