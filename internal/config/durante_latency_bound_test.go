package config

import (
	"testing"

	"aonohako/internal/durante"
)

func TestLoadDuranteLatencyBound(t *testing.T) {
	configureDuranteResourceLoadTest(t)
	for _, raw := range []string{"", "off", "v1"} {
		t.Setenv("AONOHAKO_DURANTE_LATENCY_BOUND", raw)
		cfg, err := Load()
		if err != nil || cfg.Execution.WorkMeasurement.LatencyBound != (raw == "v1") {
			t.Fatalf("latency bound %q: %v", raw, err)
		}
	}
	t.Setenv("AONOHAKO_DURANTE_LATENCY_BOUND", "v2")
	if _, err := Load(); err == nil {
		t.Fatal("accepted unsupported latency version")
	}
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	t.Setenv("AONOHAKO_DURANTE_FAMILIES", "")
	t.Setenv("AONOHAKO_DURANTE_LATENCY_BOUND", "v1")
	if _, err := Load(); err == nil {
		t.Fatal("accepted a latency profile without an enabled family")
	}
}

func TestLoadDuranteMemoryOperandBytes(t *testing.T) {
	configureDuranteResourceLoadTest(t)
	v11 := durante.EngineProfile == "dynamorio-11.3-durante-v11-vm32m"
	for _, tc := range []struct {
		memory, latency        string
		wantEnabled, wantError bool
	}{
		{"", "", false, false}, {"off", "v1", false, false},
		{"v1", "", false, true}, {"v2", "v1", false, true},
		{"v1", "v1", v11, !v11},
	} {
		t.Setenv("AONOHAKO_DURANTE_MEMORY_OPERAND_BYTES", tc.memory)
		t.Setenv("AONOHAKO_DURANTE_LATENCY_BOUND", tc.latency)
		cfg, err := Load()
		if (err != nil) != tc.wantError || cfg.Execution.WorkMeasurement.MemoryOperandBytes != tc.wantEnabled {
			t.Errorf("memory %q latency %q: enabled %t, error %v", tc.memory, tc.latency, cfg.Execution.WorkMeasurement.MemoryOperandBytes, err)
		}
	}
}
