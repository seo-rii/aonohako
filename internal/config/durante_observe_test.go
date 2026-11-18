package config

import "testing"

func TestLoadDuranteAutomaticObservation(t *testing.T) {
	configureDuranteResourceLoadTest(t)
	t.Setenv("AONOHAKO_DURANTE_OBSERVE", "")
	cfg, err := Load()
	if err != nil || cfg.Execution.WorkMeasurement.Observe {
		t.Fatalf("unexpected default: %v", err)
	}
	t.Setenv("AONOHAKO_DURANTE_OBSERVE", "on")
	cfg, err = Load()
	if err != nil || !cfg.Execution.WorkMeasurement.Observe || cfg.Execution.WorkMeasurement.TimeModels != nil || cfg.Execution.WorkMeasurement.Judge.Enabled {
		t.Fatalf("count-only setup: %v", err)
	}
	t.Setenv("AONOHAKO_DURANTE_OBSERVE", "sometimes")
	if _, err := Load(); err == nil {
		t.Fatal("accepted unknown observation setting")
	}
	t.Setenv("AONOHAKO_DURANTE_OBSERVE", "on")
	t.Setenv("AONOHAKO_DURANTE_RESOURCE_POLICY", "")
	if _, err := Load(); err == nil {
		t.Fatal("accepted observation without a bounded envelope")
	}
}
