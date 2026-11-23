package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkMeasurementEligibilityKeepsAbsenceAndRejectsAmbiguousJSON(t *testing.T) {
	const member = `"time_estimate_eligibility":{"version":1,"eligible":true,"reason_codes":[]}`
	for _, raw := range []string{`{"state":"complete"}`, `{"state":"complete",` + member + `}`} {
		var execution WorkMeasurementExecution
		if err := json.Unmarshal([]byte(raw), &execution); err != nil {
			t.Fatal(err)
		}
		present := strings.Contains(raw, "eligibility")
		if (execution.TimeEstimateEligibility != nil) != present {
			t.Fatal("legacy absence changed")
		}
	}
	for _, raw := range []string{
		`{"time_estimate_eligibility":null}`,
		`{` + member + `,` + member + `}`,
		`{` + strings.Replace(member, "time_estimate_eligibility", "TIME_ESTIMATE_ELIGIBILITY", 1) + `}`,
		`{` + member + `,"time_estimate_eligibilit\u0079":null}`,
		`{` + member + `} {}`,
		`{"time_estimate_eligibility":{"version":1,"eligible":true,"reason_codes":["signals_observed"]}}`,
	} {
		var execution WorkMeasurementExecution
		if err := json.Unmarshal([]byte(raw), &execution); err == nil {
			t.Fatal("ambiguous remote eligibility accepted", raw)
		}
	}
}
