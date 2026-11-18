package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Preserve absence of the optional eligibility extension while rejecting null
// and alternate spellings that encoding/json would treat as the same field.
func (e *WorkMeasurementExecution) UnmarshalJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("invalid work measurement execution object")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return fmt.Errorf("invalid or duplicate work measurement execution field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return err
		}
		if strings.EqualFold(key, "time_estimate_eligibility") {
			if key != "time_estimate_eligibility" || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				return fmt.Errorf("time estimate eligibility must be a canonical nonnull object")
			}
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid work measurement execution object end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing work measurement execution data")
	}
	type plain WorkMeasurementExecution
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*e = WorkMeasurementExecution(decoded)
	return nil
}
