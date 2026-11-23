package durante

import (
	"fmt"
	"strings"
	"testing"
)

const validResourcePolicyJSON = `{"max_limits":{"cpu_time_ms":4000,"wall_time_ms":8000,"memory_mb":512},"memory_budget_mb":1280,"reserved_memory_mb":128}`

func TestResourcePolicyStrictJSON(t *testing.T) {
	policy, err := ParseResourcePolicy(validResourcePolicyJSON)
	if err != nil || policy.MaxLimits != (ResourceLimits{CPUTimeMs: 4000, WallTimeMs: 8000, MemoryMB: 512}) || policy.MemoryBudgetMB != 1280 || policy.ReservedMemoryMB != 128 {
		t.Fatalf("parse: %+v, %v", policy, err)
	}
	for _, raw := range []string{"", " \n\t"} {
		if policy, err := ParseResourcePolicy(raw); policy != nil || err != nil {
			t.Fatalf("absent policy should remain disabled: %+v %v", policy, err)
		}
	}
	cases := map[string]string{
		"null":                    "null",
		"array":                   "[]",
		"missing":                 `{}`,
		"unknown":                 strings.Replace(validResourcePolicyJSON, `"reserved_memory_mb":128`, `"reserved_memory_mb":128,"other":1`, 1),
		"duplicate":               strings.Replace(validResourcePolicyJSON, `"memory_budget_mb":1280`, `"memory_budget_mb":1280,"memory_budget_mb":2048`, 1),
		"escaped duplicate":       strings.Replace(validResourcePolicyJSON, `"memory_budget_mb":1280`, `"memory_budget_mb":1280,"memory_budget_\u006db":2048`, 1),
		"case alias":              strings.Replace(validResourcePolicyJSON, `"max_limits"`, `"MAX_LIMITS"`, 1),
		"trailing object":         validResourcePolicyJSON + `{}`,
		"trailing scalar":         validResourcePolicyJSON + `true`,
		"null limits":             strings.Replace(validResourcePolicyJSON, `{"cpu_time_ms":4000,"wall_time_ms":8000,"memory_mb":512}`, `null`, 1),
		"nested duplicate":        strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000`, `"cpu_time_ms":4000,"cpu_time_ms":5000`, 1),
		"nested unknown":          strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000`, `"cpu_time_ms":4000,"other":1`, 1),
		"nested missing":          strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000,`, ``, 1),
		"nested null":             strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000`, `"cpu_time_ms":null`, 1),
		"missing reserve":         strings.Replace(validResourcePolicyJSON, `,"reserved_memory_mb":128`, ``, 1),
		"null reserve":            strings.Replace(validResourcePolicyJSON, `"reserved_memory_mb":128`, `"reserved_memory_mb":null`, 1),
		"fraction":                strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000`, `"cpu_time_ms":4.1`, 1),
		"numeric string":          strings.Replace(validResourcePolicyJSON, `"cpu_time_ms":4000`, `"cpu_time_ms":"4000"`, 1),
		"overflow":                strings.Replace(validResourcePolicyJSON, `"memory_budget_mb":1280`, `"memory_budget_mb":18446744073709551616`, 1),
		"zero reserve":            strings.Replace(validResourcePolicyJSON, `"reserved_memory_mb":128`, `"reserved_memory_mb":0`, 1),
		"zero budget":             strings.Replace(validResourcePolicyJSON, `"memory_budget_mb":1280`, `"memory_budget_mb":0`, 1),
		"negative reserve":        strings.Replace(validResourcePolicyJSON, `"reserved_memory_mb":128`, `"reserved_memory_mb":-1`, 1),
		"reserve consumes budget": strings.Replace(validResourcePolicyJSON, `"reserved_memory_mb":128`, `"reserved_memory_mb":1280`, 1),
		"oversize":                validResourcePolicyJSON + strings.Repeat(" ", maxResourcePolicyBytes),
		"invalid UTF-8":           strings.Replace(validResourcePolicyJSON, "max_limits", "max_\xfflimits", 1),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if policy, err := ParseResourcePolicy(raw); err == nil || policy != nil {
				t.Fatalf("accepted invalid policy: %+v", policy)
			}
		})
	}
}

func TestResourceLimitsBoundsAndOperatorCaps(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	for name, limits := range map[string]ResourceLimits{
		"cpu zero":       {WallTimeMs: 100, MemoryMB: 64},
		"cpu negative":   {CPUTimeMs: -1, WallTimeMs: 100, MemoryMB: 64},
		"cpu huge":       {CPUTimeMs: maxInt, WallTimeMs: maxInt, MemoryMB: 64},
		"wall zero":      {CPUTimeMs: 100, MemoryMB: 64},
		"wall huge":      {CPUTimeMs: 100, WallTimeMs: maxInt, MemoryMB: 64},
		"wall under cpu": {CPUTimeMs: 101, WallTimeMs: 100, MemoryMB: 64},
		"memory zero":    {CPUTimeMs: 100, WallTimeMs: 100},
		"memory huge":    {CPUTimeMs: 100, WallTimeMs: 100, MemoryMB: maxInt},
	} {
		t.Run(name, func(t *testing.T) {
			if err := limits.Validate(); err == nil {
				t.Fatal("accepted invalid actual limits")
			}
		})
	}
	maximum := ResourceLimits{CPUTimeMs: MaxResourceTimeMs, WallTimeMs: MaxResourceTimeMs, MemoryMB: MaxResourceMemoryMB}
	if err := maximum.Validate(); err != nil {
		t.Fatal(err)
	}
	policy, _ := ParseResourcePolicy(validResourcePolicyJSON)
	if err := policy.ValidateRequest(policy.MaxLimits); err != nil {
		t.Fatal(err)
	}
	for _, limits := range []ResourceLimits{{CPUTimeMs: 4001, WallTimeMs: 8000, MemoryMB: 512}, {CPUTimeMs: 4000, WallTimeMs: 8001, MemoryMB: 512}, {CPUTimeMs: 4000, WallTimeMs: 8000, MemoryMB: 513}} {
		if err := policy.ValidateRequest(limits); err == nil {
			t.Fatalf("accepted above operator cap: %+v", limits)
		}
	}
	var disabled *ResourcePolicy
	if err := disabled.ValidateRequest(policy.MaxLimits); err == nil {
		t.Fatal("disabled policy accepted envelope")
	}
	policy.MaxLimits.MemoryMB = -1
	if err := policy.ValidateRequest(ResourceLimits{CPUTimeMs: 1, WallTimeMs: 1, MemoryMB: 1}); err == nil {
		t.Fatal("accepted directly constructed invalid policy")
	}
}

func TestResourcePolicyAdmissionAccountsForWorkRootAndOverflow(t *testing.T) {
	policy, _ := ParseResourcePolicy(validResourcePolicyJSON)
	for _, tc := range []struct {
		active, workRoot int
		allowed          bool
	}{
		{2, 128 << 20, true}, // 2*512 + 128 + 128 = 1280 MiB.
		{2, (128 << 20) + 1, false},
		{3, 0, false},
		{0, 0, false},
		{-1, 0, false},
		{1, -1, false},
		{1, 1152 << 20, false},
		{int(^uint(0) >> 1), 0, false},
		{1, int(^uint(0) >> 1), false},
	} {
		t.Run(fmt.Sprintf("%d/%d", tc.active, tc.workRoot), func(t *testing.T) {
			if err := policy.ValidateAdmission(tc.active, tc.workRoot); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v, error=%v", tc.allowed, err)
			}
		})
	}
	maxInt := int(^uint(0) >> 1)
	large := &ResourcePolicy{MaxLimits: ResourceLimits{CPUTimeMs: 1, WallTimeMs: 1, MemoryMB: 4096}, MemoryBudgetMB: maxInt, ReservedMemoryMB: 1}
	if err := large.ValidateAdmission(maxInt, 0); err == nil {
		t.Fatal("multiplication overflow admitted impossible concurrency")
	}
	if err := large.ValidateAdmission((maxInt-1)/4096, 0); err != nil {
		t.Fatalf("safe exact quotient rejected: %v", err)
	}
}
