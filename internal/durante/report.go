package durante

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const Metric = "bb_entry_app_instructions"
const Authority = "diagnostic_only"
const Scope = "process_tree_user_code"

const (
	REPDisabled     = "disabled"
	REPMovsStosOnly = "movs_stos_only"
	REPPartial      = "partial"
	REPUnavailable  = "unavailable"
)

// MeasurementContract describes semantics, never attestation. Pointer booleans
// and RawMessage preserve missing/null fields when remote JSON is decoded.
type MeasurementContract struct {
	Version                int             `json:"version"`
	Backend                string          `json:"backend"`
	CounterIntegrity       string          `json:"counter_integrity"`
	TrustedVerdictEligible *bool           `json:"trusted_verdict_eligible"`
	InstructionSemantics   string          `json:"instruction_semantics"`
	SIMDWeighting          string          `json:"simd_weighting"`
	CPUTimeConversion      json.RawMessage `json:"cpu_time_conversion"`
}

func (c *MeasurementContract) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	keys := []string{"version", "backend", "counter_integrity", "trusted_verdict_eligible", "instruction_semantics", "simd_weighting", "cpu_time_conversion"}
	if len(fields) != len(keys) {
		return fmt.Errorf("invalid measurement contract fields")
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing measurement contract field")
		}
	}
	type plain MeasurementContract
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = MeasurementContract(decoded)
	return nil
}

func diagnosticFalse() *bool { value := false; return &value }

// DiagnosticContract must be constructed locally, never copied from target
// lifecycle records. It matches Durante's additive version-1 result contract.
func DiagnosticContract() *MeasurementContract {
	return &MeasurementContract{Version: 1, Backend: "dynamorio",
		CounterIntegrity: "application_mutable", TrustedVerdictEligible: diagnosticFalse(),
		InstructionSemantics: "basic_block_entry", SIMDWeighting: "one_per_instruction",
		CPUTimeConversion: json.RawMessage("null")}
}

// Report deliberately keeps work units separate from CPU time and RSS. A
// complete report verifies lifecycle consistency, not adversarial integrity.
type Report struct {
	Schema                int                   `json:"schema"`
	ClientVersion         int                   `json:"client_version"`
	Scope                 string                `json:"scope"`
	REPEnabled            bool                  `json:"rep_bytes_enabled"`
	EngineProfile         string                `json:"engine_profile"`
	Metric                string                `json:"metric"`
	Authority             string                `json:"authority"`
	Complete              bool                  `json:"complete"`
	Quality               string                `json:"quality"`
	Reason                string                `json:"reason,omitempty"`
	Instructions          *uint64               `json:"instructions,omitempty"`
	REPBytes              *uint64               `json:"rep_bytes,omitempty"`
	REPEntries            *uint64               `json:"rep_entries,omitempty"`
	UnsupportedREPEntries *uint64               `json:"unsupported_rep_entries,omitempty"`
	Processes             int                   `json:"processes"`
	Images                int                   `json:"images"`
	SignalsObserved       uint64                `json:"signals_observed"`
	Family                string                `json:"family"`
	DispatchProfile       string                `json:"dispatch_profile"`
	HostLibc              string                `json:"host_glibc_version,omitempty"`
	TargetLibcAttested    bool                  `json:"target_libc_attested"`
	IntegrityVerified     *bool                 `json:"integrity_verified"`
	MeasurementContract   *MeasurementContract  `json:"measurement_contract"`
	REPTelemetryStatus    string                `json:"rep_telemetry_status"`
	Installation          *InstallationIdentity `json:"installation,omitempty"`
}

func (r *Report) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	// encoding/json accepts case-insensitive aliases. Do not let a second
	// spelling override an explicit trust or coverage statement.
	for key := range fields {
		for _, protected := range []string{"integrity_verified", "measurement_contract", "rep_telemetry_status", "target_libc_attested", "installation"} {
			if key != protected && strings.EqualFold(key, protected) {
				return fmt.Errorf("noncanonical diagnostic contract field")
			}
		}
	}
	type plain Report
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = Report(decoded)
	return nil
}

func incomplete(reason string) Report {
	return Report{Schema: 1, ClientVersion: 1, Metric: Metric, Authority: Authority, Scope: Scope,
		Quality: "incomplete", Reason: reason, IntegrityVerified: diagnosticFalse(),
		MeasurementContract: DiagnosticContract(), REPTelemetryStatus: REPUnavailable}
}

// WithoutTotals preserves observation identity after a trusted execution stop.
// A target-written complete stream cannot overrule resource/cancellation state.
func (r *Report) WithoutTotals(reason string) *Report {
	if r == nil {
		return nil
	}
	result := incomplete(reason)
	result.Family, result.EngineProfile, result.DispatchProfile = r.Family, r.EngineProfile, r.DispatchProfile
	result.HostLibc, result.Installation = r.HostLibc, r.Installation
	return &result
}

func (r *Report) ValidUnavailableResult() bool {
	return r != nil && !r.Complete && r.Reason != "" && r.Quality == "incomplete" && r.REPTelemetryStatus == REPUnavailable &&
		r.Instructions == nil && r.REPBytes == nil && r.REPEntries == nil && r.UnsupportedREPEntries == nil &&
		r.SignalsObserved == 0 && r.Processes == 0 && r.Images == 0
}

// ValidDiagnosticContract rejects old, absent or promoted remote contracts.
// A successful check still does not make application-writable counters trusted.
func (r *Report) ValidDiagnosticContract() bool {
	if r == nil || r.TargetLibcAttested || r.IntegrityVerified == nil || *r.IntegrityVerified || r.MeasurementContract == nil {
		return false
	}
	c := r.MeasurementContract
	return c.Version == 1 && c.Backend == "dynamorio" && c.CounterIntegrity == "application_mutable" &&
		c.TrustedVerdictEligible != nil && !*c.TrustedVerdictEligible &&
		c.InstructionSemantics == "basic_block_entry" && c.SIMDWeighting == "one_per_instruction" &&
		bytes.Equal(bytes.TrimSpace(c.CPUTimeConversion), []byte("null"))
}

func repTelemetryStatus(enabled bool, unsupported uint64) string {
	if !enabled {
		return REPDisabled
	}
	if unsupported != 0 {
		return REPPartial
	}
	return REPMovsStosOnly
}

// ValidREPTelemetry checks a complete report's declared optional byte coverage.
func (r *Report) ValidREPTelemetry() bool {
	if r == nil || !r.Complete || r.REPBytes == nil || r.REPEntries == nil || r.UnsupportedREPEntries == nil {
		return false
	}
	if (*r.REPEntries == 0 && *r.REPBytes != 0) || (!r.REPEnabled && (*r.REPBytes != 0 || *r.REPEntries != 0 || *r.UnsupportedREPEntries != 0)) {
		return false
	}
	return r.REPTelemetryStatus == repTelemetryStatus(r.REPEnabled, *r.UnsupportedREPEntries)
}
