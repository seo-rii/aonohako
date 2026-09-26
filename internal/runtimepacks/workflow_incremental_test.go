package runtimepacks

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIncrementalPlannerRetainsExactPersistentCacheTargets(t *testing.T) {
	planner := filepath.Join("..", "..", "scripts", "runtime_incremental.py")
	program := `
import importlib.util, json, sys
spec = importlib.util.spec_from_file_location("runtime_incremental", sys.argv[1])
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)
fp = "sha256:" + "a" * 64
impact = {
    "production": [{"name": "type-" + chr(97+i), "languages": "python", "fingerprint": fp} for i in range(25)],
    "ci": [{"name": name, "fingerprint": fp} for name in ("ci-python", "ci-idris2", "ci-cuda-ocelot")],
}
outputs = m.plan(impact, {"workflow": fp, "sandbox": fp}, "a" * 40, {"cache_write": False})["outputs"]
print(json.dumps({key: [entry["name"] for entry in outputs[key]] for key in (
    "ci_cached_matrix", "ci_regular_matrix", "production_cached_matrix", "production_regular_matrix"
)}))
`
	output, err := exec.Command("python3", "-c", program, planner).CombinedOutput()
	if err != nil {
		t.Fatalf("run incremental cache planner: %v\n%s", err, output)
	}
	var matrices map[string][]string
	if err := json.Unmarshal(output, &matrices); err != nil {
		t.Fatalf("parse incremental cache plan: %v\n%s", err, output)
	}
	expected := map[string][]string{
		"ci_cached_matrix":          {"ci-cuda-ocelot", "ci-idris2"},
		"ci_regular_matrix":         {"ci-python"},
		"production_cached_matrix":  {"type-a", "type-c", "type-o"},
		"production_regular_matrix": {"type-b", "type-d", "type-e", "type-f", "type-g", "type-h", "type-i", "type-j", "type-k", "type-l", "type-m", "type-n", "type-p", "type-q", "type-r", "type-s", "type-t", "type-u", "type-v", "type-w", "type-x", "type-y"},
	}
	if !reflect.DeepEqual(matrices, expected) {
		t.Fatalf("persistent cache targets changed: got %#v, want %#v", matrices, expected)
	}
}
