package durante

import "fmt"

const (
	TimeExecutionCancelled        = "execution_cancelled"
	TimeExecutionResourceStop     = "execution_resource_exhausted"
	TimeExecutionFailed           = "execution_failed"
	TimeExecutionIncomplete       = "execution_incomplete"
	TimeExecutionNotComplete      = "execution_not_complete"
	TimeIncompatibleObservation   = "incompatible_observation"
	TimeIncompatibleConfiguration = "incompatible_time_configuration"
	TimeRuntimeProfileMismatch    = "runtime_profile_mismatch"
	TimeHostLibcMismatch          = "host_libc_mismatch"
	TimeReportContractMismatch    = "report_contract_mismatch"
	TimeIncompleteAccounting      = "incomplete_accounting"
	TimeSignalsObserved           = "signals_observed"
	TimeInvalidQuality            = "invalid_accounting_quality"
	TimeIncompleteProcessTree     = "incomplete_process_tree"
	TimeREPDisabled               = "rep_disabled"
	TimeCountersMissing           = "counters_missing"
	TimeInvalidCounters           = "invalid_counters"
	TimeUnsupportedREP            = "unsupported_rep_entries"
	TimeREPTelemetryMismatch      = "rep_telemetry_mismatch"
	TimeREPBreakdownMissing       = "rep_breakdown_missing"
	TimeLatencyBoundMissing       = "latency_bound_missing"
	TimeLatencyBoundInvalid       = "latency_bound_invalid"
	TimeNumericOverflow           = "numeric_overflow"
)

// This order is part of version 1's diagnostic contract. Execution and policy
// stages stop at their first failure; report checks collect every applicable
// reason in the order below. Arithmetic runs only after all earlier checks pass.
var timeReasonOrder = [...]string{
	TimeExecutionCancelled, TimeExecutionResourceStop, TimeExecutionFailed,
	TimeExecutionIncomplete, TimeExecutionNotComplete, TimeIncompatibleObservation,
	TimeIncompatibleConfiguration, TimeRuntimeProfileMismatch, TimeHostLibcMismatch,
	TimeReportContractMismatch, TimeIncompleteAccounting, TimeSignalsObserved,
	TimeInvalidQuality, TimeIncompleteProcessTree, TimeREPDisabled, TimeCountersMissing,
	TimeInvalidCounters, TimeUnsupportedREP, TimeREPTelemetryMismatch,
	TimeREPBreakdownMissing, TimeLatencyBoundMissing, TimeLatencyBoundInvalid, TimeNumericOverflow,
}

// TimeEligibility explains conversion availability, not a verdict or evidence
// that application-writable counters are trusted. Absence means an older runner
// did not publish this extension; observed eligibility always has version 1.
type TimeEligibility struct {
	Version     uint64   `json:"version"`
	Eligible    bool     `json:"eligible"`
	ReasonCodes []string `json:"reason_codes"`
}

func timeEligibility(reasons ...string) TimeEligibility {
	return TimeEligibility{Version: 1, Eligible: len(reasons) == 0, ReasonCodes: append([]string{}, reasons...)}
}

func (e TimeEligibility) Validate() error {
	if e.Version != 1 || e.ReasonCodes == nil || e.Eligible != (len(e.ReasonCodes) == 0) {
		return fmt.Errorf("invalid time estimate eligibility state")
	}
	previous := -1
	for _, reason := range e.ReasonCodes {
		position := -1
		for index, known := range timeReasonOrder {
			if known == reason {
				position = index
				break
			}
		}
		if position <= previous {
			return fmt.Errorf("unknown, duplicate or unordered time eligibility reason")
		}
		previous = position
	}
	return nil
}

func (e *TimeEligibility) UnmarshalJSON(data []byte) error {
	var decoded TimeEligibility
	if err := decodeTimeModelFields(data, "time eligibility", map[string]any{
		"version": &decoded.Version, "eligible": &decoded.Eligible, "reason_codes": &decoded.ReasonCodes,
	}); err != nil {
		return err
	}
	if err := decoded.Validate(); err != nil {
		return err
	}
	*e = decoded
	return nil
}

// TimeConversionEligibility is the common raw-observation gate used by time
// conversion and reference collection. Runtime/configuration, installation
// identity and execution status remain the caller's separate responsibilities.
// It never changes report quality, counters or REP coverage.
func (r Report) TimeConversionEligibility(method string) TimeEligibility {
	if method != "" && method != TimeMethod && method != TimeMethodREPBreakdown && method != TimeMethodLatencyBoundREP {
		return timeEligibility(TimeIncompatibleConfiguration)
	}
	var reasons []string
	if r.Schema != 1 || r.ClientVersion != 1 || r.Metric != Metric || r.Scope != Scope || r.Authority != Authority || !r.ValidDiagnosticContract() {
		reasons = append(reasons, TimeReportContractMismatch)
	}
	if !r.Complete || r.Reason != "" {
		reasons = append(reasons, TimeIncompleteAccounting)
	}
	if r.SignalsObserved != 0 {
		reasons = append(reasons, TimeSignalsObserved)
	}
	expectedQuality := "uninterrupted"
	if r.SignalsObserved != 0 {
		expectedQuality = "signals_observed"
	}
	if r.Quality != expectedQuality {
		reasons = append(reasons, TimeInvalidQuality)
	}
	if r.Processes <= 0 || r.Images < r.Processes {
		reasons = append(reasons, TimeIncompleteProcessTree)
	}
	if !r.REPEnabled {
		reasons = append(reasons, TimeREPDisabled)
	}
	if r.Instructions == nil || r.REPBytes == nil || r.REPEntries == nil || r.UnsupportedREPEntries == nil {
		reasons = append(reasons, TimeCountersMissing)
	}
	if (r.Instructions != nil && *r.Instructions == 0) || (r.REPBytes != nil && r.REPEntries != nil && *r.REPBytes != 0 && *r.REPEntries == 0) {
		reasons = append(reasons, TimeInvalidCounters)
	}
	if r.UnsupportedREPEntries != nil && *r.UnsupportedREPEntries != 0 {
		reasons = append(reasons, TimeUnsupportedREP)
	}
	if r.Complete && r.REPBytes != nil && r.REPEntries != nil && r.UnsupportedREPEntries != nil && !r.ValidREPTelemetry() {
		reasons = append(reasons, TimeREPTelemetryMismatch)
	}
	if method == TimeMethodREPBreakdown && r.REPBreakdown == nil {
		reasons = append(reasons, TimeREPBreakdownMissing)
	}
	if method == TimeMethodLatencyBoundREP && r.LatencyBound == nil {
		reasons = append(reasons, TimeLatencyBoundMissing)
	}
	if r.LatencyBound != nil && !r.ValidLatencyBound() {
		reasons = append(reasons, TimeLatencyBoundInvalid)
	}
	return timeEligibility(reasons...)
}
