package durante

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLatencyBoundReportDecoding(t *testing.T) {
	for _, raw := range []string{
		`{"complete":true,"instructions":10,"latency_bound":null}`,
		`{"complete":true,"instructions":10,"LATENCY_BOUND":{"version":1,"cycles_x16":40}}`,
		`{"complete":false,"instructions":10,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"complete":false,"COMPLETE":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"Complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"complete":true,"instructions":null,"INSTRUCTIONS":10,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"complete":true,"Instructions":10,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"complete":true,"latency_bound":{"version":1,"cycles_x16":40}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":39}}`,
		`{"complete":true,"instructions":4611686018427387904,"latency_bound":{"version":1,"cycles_x16":18446744073709551615}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":2,"cycles_x16":40}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":null}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_X16":40}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":40,"extra":0}}`,
		`{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":0,"cycles_x16":40}}`,
	} {
		var r Report
		if err := json.Unmarshal([]byte(raw), &r); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	const raw = `{"complete":true,"instructions":10,"latency_bound":{"version":1,"cycles_x16":40}}`
	var r Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil || !r.ValidLatencyBound() || r.LatencyBound == nil || r.LatencyBound.CyclesX16 != 40 {
		t.Fatalf("valid bound: %+v, %v", r, err)
	}
	if !r.WithoutTotals("stopped").ValidUnavailableResult() {
		t.Fatal("stopped observation retained totals")
	}
}

func TestSessionLatencyBoundReceipt(t *testing.T) {
	root := t.TempDir()
	launcher := runtimeInstallationFixture(t, root)
	client := runtimeClientFixture(t, root)
	cfg, err := ParseConfig("cpp", launcher, client, DispatchDefault)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		cfg.LatencyBound = enabled
		for _, advertised := range []bool{false, true} {
			s, err := Prepare(context.Background(), root, cfg, "CPP20", os.Geteuid(), os.Getegid())
			if err != nil {
				t.Fatal(err)
			}
			args := s.Wrap([]string{"target"})
			flag := slices.Index(args, "-latency_bound")
			if (flag >= 0) != enabled || flag > slices.Index(args, "--") {
				t.Fatalf("enabled=%t client args %v", enabled, args)
			}
			startRow := start(10, "10-1", "initial", 0, "")
			endRow := end(startRow, 2, 10)
			if advertised {
				version, work := uint64(1), uint64(40)
				startRow.LatencyBoundVersion, endRow.LatencyBoundX16 = &version, &work
			}
			f, err := os.Create(filepath.Join(s.dir, "process-10.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			encoder := json.NewEncoder(f)
			for _, row := range []event{startRow, endRow} {
				if err := encoder.Encode(row); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			report := s.Collect(10)
			if report.Complete != (enabled == advertised) || (report.LatencyBound != nil) != (enabled && advertised) ||
				(!report.Complete && !report.ValidUnavailableResult()) {
				t.Errorf("enabled=%t advertised=%t got %+v", enabled, advertised, report)
			}
		}
	}
}
