package durante

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	MaxResourceTimeMs      = 600_000
	MaxResourceMemoryMB    = 4096
	maxResourcePolicyBytes = 4096
)

// ResourceLimits bound actual instrumented execution, including engine costs.
// They do not define credited time/memory or authorize a student verdict.
type ResourceLimits struct {
	CPUTimeMs  int `json:"cpu_time_ms"`
	WallTimeMs int `json:"wall_time_ms"`
	MemoryMB   int `json:"memory_mb"`
}

func (l ResourceLimits) Validate() error {
	if l.CPUTimeMs <= 0 || l.CPUTimeMs > MaxResourceTimeMs {
		return fmt.Errorf("cpu_time_ms must be between 1 and %d", MaxResourceTimeMs)
	}
	if l.WallTimeMs <= 0 || l.WallTimeMs > MaxResourceTimeMs {
		return fmt.Errorf("wall_time_ms must be between 1 and %d", MaxResourceTimeMs)
	}
	if l.WallTimeMs < l.CPUTimeMs {
		return fmt.Errorf("wall_time_ms must be at least cpu_time_ms")
	}
	if l.MemoryMB <= 0 || l.MemoryMB > MaxResourceMemoryMB {
		return fmt.Errorf("memory_mb must be between 1 and %d", MaxResourceMemoryMB)
	}
	return nil
}

// ResourcePolicy is an operator opt-in for a dedicated diagnostic runner.
// MemoryBudgetMB must match its actual container allocation; ReservedMemoryMB
// accounts for server/runtime headroom separately from the bounded work root.
type ResourcePolicy struct {
	MaxLimits        ResourceLimits `json:"max_limits"`
	MemoryBudgetMB   int            `json:"memory_budget_mb"`
	ReservedMemoryMB int            `json:"reserved_memory_mb"`
}

func ParseResourcePolicy(raw string) (*ResourcePolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxResourcePolicyBytes || !utf8.ValidString(raw) {
		return nil, fmt.Errorf("invalid resource policy encoding or size")
	}
	var policy ResourcePolicy
	var limits json.RawMessage
	if err := decodeResourcePolicyObject([]byte(raw), map[string]any{
		"max_limits":         &limits,
		"memory_budget_mb":   &policy.MemoryBudgetMB,
		"reserved_memory_mb": &policy.ReservedMemoryMB,
	}); err != nil {
		return nil, err
	}
	if err := decodeResourcePolicyObject(limits, map[string]any{
		"cpu_time_ms":  &policy.MaxLimits.CPUTimeMs,
		"wall_time_ms": &policy.MaxLimits.WallTimeMs,
		"memory_mb":    &policy.MaxLimits.MemoryMB,
	}); err != nil {
		return nil, fmt.Errorf("max_limits: %w", err)
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &policy, nil
}

// Each object has a closed, explicitly required schema. Decode fields by their
// exact keys to reject duplicate keys and encoding/json's case-folded aliases.
func decodeResourcePolicyObject(raw []byte, fields map[string]any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return fmt.Errorf("resource policy must be an object")
	}
	seen := make(map[string]bool, len(fields))
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return fmt.Errorf("invalid resource policy key")
		}
		field, exists := fields[key]
		if !exists || seen[key] {
			return fmt.Errorf("unknown or duplicate resource policy field %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("invalid resource policy field %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("resource policy field %q cannot be null", key)
		}
		if err := json.Unmarshal(value, field); err != nil {
			return fmt.Errorf("invalid resource policy field %q", key)
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid resource policy object end")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("trailing resource policy data")
	}
	if len(seen) != len(fields) {
		return fmt.Errorf("resource policy requires all fields explicitly")
	}
	return nil
}

func (p *ResourcePolicy) validate() error {
	if p == nil {
		return fmt.Errorf("durante resource policy is not enabled")
	}
	if err := p.MaxLimits.Validate(); err != nil {
		return fmt.Errorf("max_limits: %w", err)
	}
	if p.MemoryBudgetMB <= 0 || p.ReservedMemoryMB <= 0 {
		return fmt.Errorf("memory_budget_mb and reserved_memory_mb must be positive")
	}
	if p.ReservedMemoryMB >= p.MemoryBudgetMB {
		return fmt.Errorf("reserved_memory_mb must be smaller than memory_budget_mb")
	}
	return nil
}

func (p *ResourcePolicy) ValidateRequest(limits ResourceLimits) error {
	if err := p.validate(); err != nil {
		return err
	}
	if err := limits.Validate(); err != nil {
		return err
	}
	if limits.CPUTimeMs > p.MaxLimits.CPUTimeMs || limits.WallTimeMs > p.MaxLimits.WallTimeMs || limits.MemoryMB > p.MaxLimits.MemoryMB {
		return fmt.Errorf("work_measurement_limits exceeds operator max_limits")
	}
	return nil
}

// ValidateAdmission reserves the worst case across all active runs. Division
// avoids overflowing maxActiveRuns*MemoryMB, including directly built configs.
func (p *ResourcePolicy) ValidateAdmission(maxActiveRuns, workRootMaxBytes int) error {
	if err := p.validate(); err != nil {
		return err
	}
	if maxActiveRuns <= 0 || workRootMaxBytes < 0 {
		return fmt.Errorf("resource policy requires positive max_active_runs and nonnegative work_root_max_bytes")
	}
	const mib = 1 << 20
	workRootMB := workRootMaxBytes / mib
	if workRootMaxBytes%mib != 0 {
		workRootMB++
	}
	remainingMB := p.MemoryBudgetMB - p.ReservedMemoryMB
	if workRootMB >= remainingMB {
		return fmt.Errorf("durante memory budget exhausted by work root and reserved memory")
	}
	remainingMB -= workRootMB
	if maxActiveRuns > remainingMB/p.MaxLimits.MemoryMB {
		return fmt.Errorf("durante max_active_runs and max_limits.memory_mb exceed memory budget after work root and reserved memory")
	}
	return nil
}
