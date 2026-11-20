// Package reference collects fixed runtime workloads and evaluates explicit
// reference-time candidates. Its results retain diagnostic authority.
package reference

import (
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

const Schema = 1
const CorpusID = "cpp-reference-v1"
const ExternalCorpusID = "external-cpp20-validation-v1"
const RuntimeCorpusID = "runtime-reference-v1"
const RustLongCorpusID = "rust-long-reference-v1"
const SplitTrain = "train"
const SplitValidation = "validation"

type Case struct {
	Scale          int    `json:"scale"`
	Input          string `json:"input"`
	ExpectedStdout string `json:"expected_stdout"`
}

type Workload struct {
	ID     string `json:"id"`
	Split  string `json:"split"`
	Source string `json:"source"`
	Cases  []Case `json:"cases"`
}

// Sample preserves all three independent executions. The middle execution's
// counters are paired with native reference observations, not credited native
// memory or a claim that the executions consumed identical resources.
// Optional REP operation detail is retained as observed; an absent split is
// unspecified and must not become an observed zero partition.
type Sample struct {
	WorkloadID     string            `json:"workload_id"`
	Split          string            `json:"split"`
	Scale          int               `json:"scale"`
	Repetition     int               `json:"repetition"`
	SourceSHA256   string            `json:"source_sha256"`
	BinarySHA256   string            `json:"binary_sha256"`
	InputSHA256    string            `json:"input_sha256"`
	ExpectedSHA256 string            `json:"expected_sha256"`
	NativeBefore   model.RunResponse `json:"native_before"`
	Metered        model.RunResponse `json:"metered"`
	NativeAfter    model.RunResponse `json:"native_after"`
}

type Dataset struct {
	Schema          int                    `json:"schema"`
	CorpusID        string                 `json:"corpus_id"`
	Smoke           bool                   `json:"smoke"`
	Complete        bool                   `json:"complete"`
	StartedAt       string                 `json:"started_at"`
	RuntimeImage    string                 `json:"runtime_image"`
	SourceLanguage  string                 `json:"source_language"`
	DispatchProfile string                 `json:"dispatch_profile"`
	CPUIDProfile    string                 `json:"cpuid_profile,omitempty"`
	RunnerSHA256    string                 `json:"runner_sha256"`
	Environment     map[string]string      `json:"environment"`
	Repetitions     int                    `json:"repetitions"`
	NativeLimits    model.Limits           `json:"native_limits"`
	MeteredLimits   durante.ResourceLimits `json:"metered_limits"`
	Samples         []Sample               `json:"samples"`
	Failures        []string               `json:"failures,omitempty"`
	// RuntimeDeterminism is the option set the collection applied to this
	// source's measured launcher (empty: none).
	RuntimeDeterminism string `json:"runtime_determinism,omitempty"`
	// LatencyBoundVersion binds every metered sample to the requested counter.
	LatencyBoundVersion int `json:"latency_bound_version,omitempty"`
	// External collections bind the held-out workloads and preselected policy.
	CorpusSHA256       string `json:"corpus_sha256,omitempty"`
	FrozenPolicySHA256 string `json:"frozen_policy_sha256,omitempty"`
}

type FitOptions struct {
	Method             string `json:"method,omitempty"`
	ModelID            string `json:"model_id"`
	ModelVersion       uint64 `json:"model_version"`
	ReferenceID        string `json:"reference_id"`
	MinRepetitions     int    `json:"min_repetitions"`
	MinNativeNs        uint64 `json:"min_native_ns"`
	MaxNativeSpreadPPM uint64 `json:"max_native_spread_ppm"`
	MaxWorkSpreadPPM   uint64 `json:"max_work_spread_ppm"`
	MaxErrorPPM        uint64 `json:"max_error_ppm"`
}

// CaseEvaluation uses all 2*N native observations; its median rounds an even
// pair upward to whole nanoseconds. Prediction/error fields describe the worst
// absolute-error metered repetition, and every repetition is gated.
type CaseEvaluation struct {
	WorkloadID           string `json:"workload_id"`
	Split                string `json:"split"`
	Scale                int    `json:"scale"`
	NativeMedianNs       uint64 `json:"native_median_ns"`
	NativeSpreadPPM      uint64 `json:"native_spread_ppm"`
	InstructionSpreadPPM uint64 `json:"instruction_spread_ppm"`
	REPSpreadPPM         uint64 `json:"rep_spread_ppm"`
	EstimatedNs          int64  `json:"estimated_ns"`
	AbsoluteErrorNs      uint64 `json:"absolute_error_ns"`
	AbsoluteErrorPPM     uint64 `json:"absolute_error_ppm"`
	Underestimated       bool   `json:"underestimated"`
	// A zero spread is observed when all latency bounds in the case agree.
	LatencyBoundSpreadPPM *uint64 `json:"latency_bound_spread_ppm,omitempty"`
}

type Evaluation struct {
	Schema          int                 `json:"schema"`
	Authority       string              `json:"authority"`
	Passed          bool                `json:"passed"`
	Options         FitOptions          `json:"options"`
	RuntimeID       string              `json:"runtime_id"`
	CandidatePolicy *durante.TimePolicy `json:"candidate_policy,omitempty"`
	Cases           []CaseEvaluation    `json:"cases,omitempty"`
	Failures        []string            `json:"failures,omitempty"`
}
