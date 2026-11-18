package config

import "testing"

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
