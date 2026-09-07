package runvalidation

import (
	"encoding/json"
	"testing"

	"aonohako/internal/model"
)

// FuzzValidateNoPanic feeds arbitrary JSON through the decode+validate path the
// API runs on every /execute request. Validate must reject or accept, but never
// panic, and it must be idempotent on a value it already accepted (which catches
// accidental in-place mutation of the request during validation).
func FuzzValidateNoPanic(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"lang":"binary","binaries":[{"name":"a","data_b64":""}],"limits":{"time_ms":1000,"memory_mb":256}}`,
		`{"lang":"python","stdin":"x","limits":{"time_ms":-1,"memory_mb":0}}`,
		`{"programs":[{"id":"p","lang":"binary","binaries":[{"name":"a","data_b64":""}]}],"steps":[{"id":"s","program_id":"p","limits":{"time_ms":1,"memory_mb":1}}]}`,
		`{"communication":{"version":1,"participant_count":2}}`,
		`{"limits":{"time_ms":600001,"memory_mb":99999}}`,
		`{"lang":"binary","binaries":[{"name":"../escape","data_b64":"AA=="}],"limits":{"time_ms":1,"memory_mb":1}}`,
		`{"spj":{"binary":{"name":"j","data_b64":"AA=="}},"lang":"binary","binaries":[{"name":"a","data_b64":""}],"limits":{"time_ms":1,"memory_mb":1}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		var req model.RunRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		if err := Validate(&req); err != nil {
			return
		}
		if err := Validate(&req); err != nil {
			t.Fatalf("Validate not idempotent: first pass ok, second failed: %v", err)
		}
	})
}
