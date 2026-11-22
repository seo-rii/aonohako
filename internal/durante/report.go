package durante

import (
	"aonohako/internal/cpupolicy"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
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

// LatencyBound is a static block cost summed over the executed process tree.
// Version 1 charges at least four sixteenths of a cycle per instruction.
type LatencyBound struct {
	Version   int    `json:"version"`
	CyclesX16 uint64 `json:"cycles_x16"`
}

func (b *LatencyBound) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	if len(fields) != 2 {
		return fmt.Errorf("invalid latency bound fields")
	}
	var version int
	if err := json.Unmarshal(fields["version"], &version); err != nil || version != 1 {
		return fmt.Errorf("unsupported latency bound version")
	}
	var cycles *uint64
	if err := json.Unmarshal(fields["cycles_x16"], &cycles); err != nil || cycles == nil {
		return fmt.Errorf("missing or invalid latency bound cycles_x16")
	}
	*b = LatencyBound{Version: version, CyclesX16: *cycles}
	return nil
}

func (b LatencyBound) validFor(instructions uint64) bool {
	return b.Version == 1 && instructions > 0 && instructions <= b.CyclesX16/4
}

// MemoryOperandBytes sums nominal read and write operand widths across selected images.
type MemoryOperandBytes struct {
	Version uint64 `json:"version"`
	Bytes   uint64 `json:"bytes"`
}

func (b *MemoryOperandBytes) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	if len(fields) != 2 {
		return fmt.Errorf("invalid memory operand bytes fields")
	}
	var version uint64
	if err := json.Unmarshal(fields["version"], &version); err != nil || version != 1 {
		return fmt.Errorf("unsupported memory operand bytes version")
	}
	var count *uint64
	if err := json.Unmarshal(fields["bytes"], &count); err != nil || count == nil {
		return fmt.Errorf("missing or invalid memory operand byte count")
	}
	*b = MemoryOperandBytes{Version: version, Bytes: *count}
	return nil
}

// REPBreakdown partitions supported REP work by instruction kind. A missing
// breakdown is legacy, unspecified detail, not four observed zero counters.
type REPBreakdown struct {
	Version     uint64 `json:"version"`
	MOVSBytes   uint64 `json:"movs_bytes"`
	STOSBytes   uint64 `json:"stos_bytes"`
	MOVSEntries uint64 `json:"movs_entries"`
	STOSEntries uint64 `json:"stos_entries"`
}

func (b *REPBreakdown) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	var decoded REPBreakdown
	targets := map[string]*uint64{
		"version": &decoded.Version, "movs_bytes": &decoded.MOVSBytes,
		"stos_bytes": &decoded.STOSBytes, "movs_entries": &decoded.MOVSEntries,
		"stos_entries": &decoded.STOSEntries,
	}
	if len(fields) != len(targets) {
		return fmt.Errorf("invalid REP breakdown fields")
	}
	for key, target := range targets {
		value, exists := fields[key]
		if !exists || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("missing or null REP breakdown field %q", key)
		}
		if err := json.Unmarshal(value, target); err != nil {
			return fmt.Errorf("invalid REP breakdown field %q", key)
		}
	}
	if decoded.Version != 1 {
		return fmt.Errorf("unsupported REP breakdown version")
	}
	*b = decoded
	return nil
}

func (b REPBreakdown) validFor(bytes, entries uint64, enabled bool) bool {
	return b.Version == 1 &&
		(b.MOVSBytes == 0 || b.MOVSEntries > 0) && (b.STOSBytes == 0 || b.STOSEntries > 0) &&
		b.MOVSBytes <= math.MaxUint64-b.STOSBytes && b.MOVSBytes+b.STOSBytes == bytes &&
		b.MOVSEntries <= math.MaxUint64-b.STOSEntries && b.MOVSEntries+b.STOSEntries == entries &&
		(enabled || (b == REPBreakdown{Version: 1}))
}

func (b REPBreakdown) checkedAdd(other REPBreakdown) (REPBreakdown, error) {
	if b.Version != 1 || other.Version != 1 {
		return REPBreakdown{}, fmt.Errorf("unsupported REP breakdown version")
	}
	totals := [4]uint64{b.MOVSBytes, b.STOSBytes, b.MOVSEntries, b.STOSEntries}
	values := [4]uint64{other.MOVSBytes, other.STOSBytes, other.MOVSEntries, other.STOSEntries}
	for i, value := range values {
		if totals[i] > math.MaxUint64-value {
			return REPBreakdown{}, fmt.Errorf("tree REP breakdown overflow")
		}
		totals[i] += value
	}
	return REPBreakdown{Version: 1, MOVSBytes: totals[0], STOSBytes: totals[1], MOVSEntries: totals[2], STOSEntries: totals[3]}, nil
}

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
	REPBreakdown          *REPBreakdown         `json:"rep_breakdown,omitempty"`
	LatencyBound          *LatencyBound         `json:"latency_bound,omitempty"`
	MemoryOperandBytes    *MemoryOperandBytes   `json:"memory_operand_bytes,omitempty"`
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
	DisallowedCPUFeatures []cpupolicy.Feature   `json:"disallowed_cpu_features,omitempty"`
	// CPUIDProfile is the client's consistent fixed-processor receipt across
	// every image. Empty means the host's CPUID was visible to the target.
	CPUIDProfile string `json:"cpuid_profile,omitempty"`
	// RuntimeDeterminism is the option set the runner applied to the measured
	// launcher (see RuntimeDeterminismV1). Empty means none was applied. It is
	// runner configuration, not a client receipt.
	RuntimeDeterminism string `json:"runtime_determinism,omitempty"`
	nativeCPUFeatures  []cpupolicy.Feature
}

// HasCPUFeaturePolicy checks a locally collected, globally consistent policy
// receipt from a valid root start. It can remain available after an ordinary
// guest fault, but is never restored from public JSON or treated as attestation.
func (r *Report) HasCPUFeaturePolicy(features []cpupolicy.Feature) bool {
	if r == nil || len(features) == 0 || cpupolicy.Validate(features) != nil {
		return false
	}
	canonical := slices.Clone(features)
	slices.Sort(canonical)
	return slices.Equal(r.nativeCPUFeatures, canonical)
}

func (r *Report) UnmarshalJSON(data []byte) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	// encoding/json accepts case-insensitive aliases. Do not let a second
	// spelling override an explicit trust or coverage statement.
	protectedFields := []string{"integrity_verified", "measurement_contract", "rep_telemetry_status", "target_libc_attested", "installation", "rep_breakdown", "latency_bound", "memory_operand_bytes", "disallowed_cpu_features", "cpuid_profile", "runtime_determinism"}
	if _, present := fields["latency_bound"]; present {
		protectedFields = append(protectedFields, "complete", "instructions")
	}
	if _, present := fields["memory_operand_bytes"]; present {
		protectedFields = append(protectedFields, "signals_observed", "quality", "engine_profile")
		signals, exists := fields["signals_observed"]
		if !exists || bytes.Equal(bytes.TrimSpace(signals), []byte("null")) {
			return fmt.Errorf("missing signal observation for memory operand bytes")
		}
	}
	for key := range fields {
		for _, protected := range protectedFields {
			if key != protected && strings.EqualFold(key, protected) {
				return fmt.Errorf("noncanonical diagnostic contract field")
			}
		}
	}
	if value, exists := fields["rep_breakdown"]; exists && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("REP breakdown cannot be null")
	}
	type plain Report
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if _, exists := fields["latency_bound"]; exists && (decoded.LatencyBound == nil || !(*Report)(&decoded).ValidLatencyBound()) {
		return fmt.Errorf("invalid latency bound on diagnostic report")
	}
	if _, exists := fields["memory_operand_bytes"]; exists && (decoded.MemoryOperandBytes == nil || !(*Report)(&decoded).ValidMemoryOperandBytes()) {
		return fmt.Errorf("invalid memory operand bytes on diagnostic report")
	}
	if _, exists := fields["disallowed_cpu_features"]; exists {
		if len(decoded.DisallowedCPUFeatures) == 0 || cpupolicy.Validate(decoded.DisallowedCPUFeatures) != nil ||
			(len(decoded.DisallowedCPUFeatures) == 2 && decoded.DisallowedCPUFeatures[0] != cpupolicy.AVX) {
			return fmt.Errorf("invalid or noncanonical disallowed CPU features")
		}
	}
	if _, exists := fields["cpuid_profile"]; exists && !ValidCPUIDProfile(decoded.CPUIDProfile) {
		return fmt.Errorf("invalid or empty CPUID profile")
	}
	if _, exists := fields["runtime_determinism"]; exists && decoded.RuntimeDeterminism != RuntimeDeterminismV1 {
		return fmt.Errorf("invalid or empty runtime determinism")
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
		r.Instructions == nil && r.REPBytes == nil && r.REPEntries == nil && r.UnsupportedREPEntries == nil && r.REPBreakdown == nil && r.LatencyBound == nil && r.MemoryOperandBytes == nil &&
		r.SignalsObserved == 0 && r.Processes == 0 && r.Images == 0 && len(r.DisallowedCPUFeatures) == 0 && r.CPUIDProfile == "" &&
		r.RuntimeDeterminism == ""
}

// ValidLatencyBound checks the optional counter against its instruction total.
func (r *Report) ValidLatencyBound() bool {
	return r != nil && (r.LatencyBound == nil || (r.Complete && r.Instructions != nil && r.LatencyBound.validFor(*r.Instructions)))
}

// ValidMemoryOperandBytes checks the counter's requirements for a published worker report.
func (r *Report) ValidMemoryOperandBytes() bool {
	return r != nil && (r.MemoryOperandBytes == nil || (r.MemoryOperandBytes.Version == 1 &&
		r.EngineProfile == engineProfileV11 && r.LatencyBound != nil && r.ValidLatencyBound() &&
		r.Quality == "uninterrupted" && r.SignalsObserved == 0))
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
	if r.REPBreakdown != nil && !r.REPBreakdown.validFor(*r.REPBytes, *r.REPEntries, r.REPEnabled) {
		return false
	}
	return r.REPTelemetryStatus == repTelemetryStatus(r.REPEnabled, *r.UnsupportedREPEntries)
}
