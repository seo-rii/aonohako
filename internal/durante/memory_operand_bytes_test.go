package durante

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestMemoryOperandBytesReceipt(t *testing.T) {
	const fixed = `{"complete":true,"quality":"uninterrupted","signals_observed":0,"engine_profile":"` + engineProfileV11 + `","instructions":10,"latency_bound":{"version":1,"cycles_x16":40}`
	const metric = `,"memory_operand_bytes":{"version":1,"bytes":0}`
	var report Report
	if err := json.Unmarshal([]byte(fixed+"}"), &report); err != nil || report.MemoryOperandBytes != nil {
		t.Fatalf("legacy report: %+v %v", report, err)
	}
	raw := fixed + metric + "}"
	if err := json.Unmarshal([]byte(raw), &report); err != nil || !report.ValidMemoryOperandBytes() || report.MemoryOperandBytes == nil || *report.MemoryOperandBytes != (MemoryOperandBytes{Version: 1}) {
		t.Fatalf("measured zero: %+v %v", report, err)
	}
	for _, profile := range []string{engineProfileV12, engineProfileV13} {
		wire := strings.Replace(raw, engineProfileV11, profile, 1)
		if err := json.Unmarshal([]byte(wire), &report); err != nil || !report.ValidMemoryOperandBytes() {
			t.Fatalf("%s memory receipt: %+v %v", profile, report, err)
		}
	}
	if stopped := report.WithoutTotals("stopped"); stopped.MemoryOperandBytes != nil || !stopped.ValidUnavailableResult() {
		t.Fatal("unavailable report retained memory operand bytes")
	}
	for _, tc := range []struct{ from, to string }{
		{engineProfileV11, engineProfileV10},
		{`"engine_profile"`, `"ENGINE_PROFILE"`},
		{`"complete":true`, `"complete":false`},
		{`"signals_observed":0,`, ""},
		{`"signals_observed":0`, `"signals_observed":null`},
		{`"signals_observed":0`, `"signals_observed":1,"SIGNALS_OBSERVED":0`},
		{`"quality":"uninterrupted"`, `"quality":"signals_observed"`},
		{`"signals_observed":0`, `"signals_observed":1`},
		{`,"latency_bound":{"version":1,"cycles_x16":40}`, ""},
		{metric, `,"memory_operand_bytes":null`},
		{`"memory_operand_bytes"`, `"MEMORY_OPERAND_BYTES"`},
		{`"version":1,"bytes":0`, `"version":2,"bytes":0`},
		{`"bytes":0`, `"bytes":null`},
		{`"bytes":0`, `"bYtes":0`},
		{`"bytes":0`, `"bytes":0,"extra":1`},
		{`"bytes":0`, `"bytes":0,"bytes":1`},
	} {
		wire := strings.Replace(raw, tc.from, tc.to, 1)
		if err := json.Unmarshal([]byte(wire), &report); err == nil {
			t.Errorf("accepted invalid receipt %s", wire)
		}
	}
}

func TestMemoryOperandBytesEvents(t *testing.T) {
	// Add the new raw fields to full, canonical start/end records so field
	// type and event placement are checked by the normal JSON entry point.
	s := start(10, "10-1", "initial", 0, "")
	e := end(s, 2, 10)
	for _, tc := range []struct {
		base   event
		fields string
		valid  bool
	}{
		{s, `"latency_bound_version":1,"memory_operand_bytes_version":1`, true},
		{s, `"latency_bound_version":1,"memory_operand_bytes_version":2`, false},
		{s, `"memory_operand_bytes_version":1`, false},
		{s, `"memory_operand_bytes":0`, false},
		{e, `"memory_operand_bytes":0`, true},
		{e, `"memory_operand_bytes":null`, false},
		{e, `"memory_operand_bytes":-1`, false},
		{e, `"memory_operand_bytes":18446744073709551616`, false},
		{e, `"memory_operand_bytes_version":1`, false},
	} {
		raw, err := json.Marshal(tc.base)
		if err != nil {
			t.Fatal(err)
		}
		_, err = decodeEvent([]byte(strings.TrimSuffix(string(raw), "}") + "," + tc.fields + "}"))
		if (err == nil) != tc.valid {
			t.Errorf("%s + %s: %v", tc.base.Event, tc.fields, err)
		}
	}
}

func TestMemoryOperandBytesUsesCanonicalSignalFields(t *testing.T) {
	terminal := end(start(10, "10-1", "initial", 0, ""), 2, 10)
	terminal.Signals, terminal.Quality = 1, "signals_observed"
	raw, err := json.Marshal(terminal)
	if err != nil {
		t.Fatal(err)
	}
	fields := `,"SIGNALS_OBSERVED":0,"QUALITY":"uninterrupted","memory_operand_bytes":5}`
	row, err := decodeEvent([]byte(strings.TrimSuffix(string(raw), "}") + fields))
	if err != nil || row.Signals != 1 || row.Quality != "signals_observed" {
		t.Fatalf("signal observation changed: %+v %v", row, err)
	}
}

func TestMemoryOperandBytesTree(t *testing.T) {
	// Exercise a real zero and the unsigned maximum without dynamic
	// instruction assumptions. Both values are allowed by the upstream metric.
	one, zero, max := uint64(1), uint64(0), uint64(math.MaxUint64)
	for _, tc := range []struct {
		name  string
		edit  func(map[int][]event)
		valid bool
		want  uint64
	}{
		{"zero", nil, true, 0},
		{"maximum", func(m map[int][]event) { m[10][2].MemoryOperandBytes = &max }, true, max},
		{"overflow", func(m map[int][]event) { m[10][2].MemoryOperandBytes = &max; m[20][1].MemoryOperandBytes = &one }, false, 0},
		{"mixed start", func(m map[int][]event) { m[20][0].MemoryOperandBytesVersion = nil }, false, 0},
		{"missing terminal", func(m map[int][]event) { m[10][2].MemoryOperandBytes = nil }, false, 0},
		{"signal", func(m map[int][]event) { m[20][1].Signals = 1; m[20][1].Quality = "signals_observed" }, false, 0},
		{"unadvertised", func(m map[int][]event) {
			for _, rows := range m {
				rows[0].MemoryOperandBytesVersion = nil
			}
		}, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams := basicTree()
			for _, rows := range streams {
				rows[0].LatencyBoundVersion = &one
				rows[0].MemoryOperandBytesVersion = &one
				terminal := &rows[len(rows)-1]
				cycles := terminal.Instructions * 4
				terminal.LatencyBoundX16 = &cycles
				terminal.MemoryOperandBytes = &zero
			}
			if tc.edit != nil {
				tc.edit(streams)
			}
			got, err := aggregate(streams, 10)
			if (err == nil) != tc.valid || (tc.valid && (got.MemoryOperandBytes == nil || got.MemoryOperandBytes.Bytes != tc.want)) {
				t.Fatalf("aggregate=%+v, err=%v", got, err)
			}
		})
	}
}
