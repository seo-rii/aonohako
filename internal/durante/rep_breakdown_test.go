package durante

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func repCount(value uint64) *uint64 { return &value }

func advertiseREP(row event) event {
	row.REPBreakdownVersion = repCount(1)
	return row
}

func withREPBreakdown(row event, part REPBreakdown) event {
	row.REPMOVSBytes, row.REPSTOSBytes = repCount(part.MOVSBytes), repCount(part.STOSBytes)
	row.REPMOVSEntries, row.REPSTOSEntries = repCount(part.MOVSEntries), repCount(part.STOSEntries)
	row.REPBytes, row.REPEntries = part.MOVSBytes+part.STOSBytes, part.MOVSEntries+part.STOSEntries
	return row
}

func collectREPRows(t *testing.T, streams map[int][]event) Report {
	t.Helper()
	dir := t.TempDir()
	for pid, rows := range streams {
		var stream strings.Builder
		for _, row := range rows {
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			stream.Write(raw)
			stream.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "process-"+strconv.Itoa(pid)+".jsonl"), []byte(stream.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return CollectDirectory(dir, 10)
}

func TestREPBreakdownPreservesLegacyAbsenceAndObservedZeros(t *testing.T) {
	legacy := collectREPRows(t, basicTree())
	if !legacy.Complete || legacy.REPBreakdown != nil || !legacy.ValidREPTelemetry() {
		t.Fatalf("legacy observation changed: %+v", legacy)
	}
	raw, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(raw), "rep_breakdown") {
		t.Fatalf("legacy observation acquired zero detail: %s, %v", raw, err)
	}
	for _, tc := range []struct {
		name        string
		enabled     bool
		part        REPBreakdown
		unsupported uint64
	}{
		{"disabled", false, REPBreakdown{Version: 1}, 0},
		{"zero RCX", true, REPBreakdown{Version: 1, MOVSEntries: 2, STOSEntries: 3}, 0},
		{"both kinds", true, REPBreakdown{Version: 1, MOVSBytes: 17, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 3}, 0},
		{"unsupported excluded", true, REPBreakdown{Version: 1, MOVSBytes: 17, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 3}, 7},
		{"uint64 boundary", true, REPBreakdown{Version: 1, MOVSBytes: math.MaxUint64, MOVSEntries: 1}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := advertiseREP(start(10, "10-1", "initial", 0, ""))
			s.REPEnabled = tc.enabled
			e := withREPBreakdown(end(s, 2, 123), tc.part)
			e.UnsupportedREPEntries = tc.unsupported
			r := collectREPRows(t, map[int][]event{10: {s, e}})
			if !r.Complete || !r.ValidREPTelemetry() || r.REPBreakdown == nil || *r.REPBreakdown != tc.part || *r.UnsupportedREPEntries != tc.unsupported {
				t.Fatalf("incorrect detailed observation: %+v; detail=%+v", r, r.REPBreakdown)
			}
		})
	}
}

func detailedREPTree() map[int][]event {
	rows := basicTree()
	rows[10][0] = advertiseREP(rows[10][0])
	rows[10][2] = withREPBreakdown(rows[10][2], REPBreakdown{Version: 1, MOVSBytes: 17, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 3})
	c := advertiseREP(rows[20][0])
	checkpoint := withREPBreakdown(end(c, 2, 200), REPBreakdown{Version: 1, MOVSBytes: 19, STOSBytes: 7, MOVSEntries: 4, STOSEntries: 2})
	checkpoint.Event, checkpoint.ExitSeen = "exec_attempt", false
	image := advertiseREP(start(20, "20-2", "initial", 0, ""))
	last := withREPBreakdown(end(image, 2, 300), REPBreakdown{Version: 1, MOVSBytes: 3, STOSBytes: 5, MOVSEntries: 1, STOSEntries: 2})
	rows[20] = []event{c, checkpoint, image, last}
	return rows
}

func TestREPBreakdownSumsForkExecImagesAndRejectsMixedCapabilities(t *testing.T) {
	for _, syscall := range []int{57, 58} {
		rows := detailedREPTree()
		rows[10][1].Syscall = syscall
		r := collectREPRows(t, rows)
		want := REPBreakdown{Version: 1, MOVSBytes: 39, STOSBytes: 17, MOVSEntries: 7, STOSEntries: 7}
		if !r.Complete || *r.Instructions != 600 || r.Processes != 2 || r.Images != 3 || r.REPBreakdown == nil || *r.REPBreakdown != want || *r.REPBytes != 56 || *r.REPEntries != 14 {
			t.Fatalf("fork/exec detail counted incorrectly: %+v, %+v", r, r.REPBreakdown)
		}
	}
	for _, location := range [][2]int{{10, 0}, {20, 0}, {20, 2}} {
		rows := detailedREPTree()
		rows[location[0]][location[1]].REPBreakdownVersion = nil
		r := collectREPRows(t, rows)
		if !r.ValidUnavailableResult() || r.REPBreakdown != nil {
			t.Fatalf("mixed capability retained totals: %+v", r)
		}
	}
}

func TestREPBreakdownFailedExecDiscardsOnlyCheckpointSemantics(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		for _, missing := range []int{0, 1, 4} {
			s := start(10, "10-1", "initial", 0, "")
			if advertised {
				s = advertiseREP(s)
			}
			checkpoint := withREPBreakdown(end(s, 2, 100), REPBreakdown{Version: 1, MOVSBytes: 10, STOSBytes: 20, MOVSEntries: 1, STOSEntries: 2})
			checkpoint.Event, checkpoint.ExitSeen = "exec_attempt", false
			checkpoint.REPMOVSBytes = repCount(999) // Typed, but deliberately inconsistent.
			fields := []**uint64{&checkpoint.REPMOVSBytes, &checkpoint.REPSTOSBytes, &checkpoint.REPMOVSEntries, &checkpoint.REPSTOSEntries}
			for _, field := range fields[:missing] {
				*field = nil
			}
			fail := next(s, 3, "exec_failed")
			fail.Syscall, fail.Result = 59, -2
			last := end(s, 4, 150)
			var want *REPBreakdown
			if advertised {
				want = &REPBreakdown{Version: 1, MOVSBytes: 15, STOSBytes: 25, MOVSEntries: 2, STOSEntries: 3}
				last = withREPBreakdown(last, *want)
			}
			r := collectREPRows(t, map[int][]event{10: {s, checkpoint, fail, last}})
			if !r.Complete || *r.Instructions != 150 || r.Images != 1 || !reflect.DeepEqual(r.REPBreakdown, want) {
				t.Fatalf("failed checkpoint affected totals: advertised=%t missing=%d report=%+v", advertised, missing, r)
			}
			// Selecting that checkpoint after successful exec must reject its
			// missing/incorrect partition, or its unadvertised raw fields.
			if advertised || missing != 4 {
				image := start(10, "10-2", "initial", 0, "")
				if advertised {
					image = advertiseREP(image)
				}
				terminal := end(image, 2, 50)
				if advertised {
					terminal = withREPBreakdown(terminal, REPBreakdown{Version: 1})
				}
				r = collectREPRows(t, map[int][]event{10: {s, checkpoint, image, terminal}})
				if !r.ValidUnavailableResult() || r.REPBreakdown != nil {
					t.Fatalf("selected invalid checkpoint accepted: %+v", r)
				}
			}
		}
	}
}

func TestREPBreakdownRawFieldsRequireExactUnsignedTypesEverywhere(t *testing.T) {
	for _, kind := range []string{"start", "end", "exec_attempt", "exec_failed", "spawn"} {
		row := end(start(10, "10-1", "initial", 0, ""), 2, 100)
		row.Event = kind
		raw, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"rep_breakdown_version", "rep_movs_bytes", "rep_stos_bytes", "rep_movs_entries", "rep_stos_entries"} {
			for _, bad := range []string{"null", "true", "1.0", "1e0", "-1", `"1"`, "18446744073709551616", "[]", "{}"} {
				changed := strings.TrimSuffix(string(raw), "}") + `,"` + field + `":` + bad + "}"
				if _, err := decodeEvent([]byte(changed)); err == nil {
					t.Fatalf("accepted %s %s=%s", kind, field, bad)
				}
			}
			duplicate := strings.TrimSuffix(string(raw), "}") + `,"` + field + `":1,"` + field + `":1}`
			if _, err := decodeEvent([]byte(duplicate)); err == nil {
				t.Fatalf("accepted duplicate raw %s", field)
			}
		}
		for _, version := range []string{"0", "2", "18446744073709551615"} {
			changed := strings.TrimSuffix(string(raw), "}") + `,"rep_breakdown_version":` + version + "}"
			if _, err := decodeEvent([]byte(changed)); err == nil {
				t.Fatalf("accepted %s capability %s", kind, version)
			}
		}
	}
}

func TestREPBreakdownRawCaseAliasesCannotAdvertiseOrOverwriteDetail(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		s := start(10, "10-1", "initial", 0, "")
		if advertised {
			s = advertiseREP(s)
		}
		e := end(s, 2, 100)
		if advertised {
			e = withREPBreakdown(e, REPBreakdown{Version: 1})
		}
		var rows []event
		for _, row := range []event{s, e} {
			raw, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			changed := strings.TrimSuffix(string(raw), "}") + `,"REP_BREAKDOWN_VERSION":false,"REP_MOVS_BYTES":"unknown","REP_STOS_BYTES":null,"REP_MOVS_ENTRIES":-1,"REP_STOS_ENTRIES":1.5}`
			decoded, err := decodeEvent([]byte(changed))
			if err != nil {
				t.Fatalf("unknown scalar aliases changed upstream-compatible decoding: %v", err)
			}
			rows = append(rows, decoded)
		}
		r, err := aggregate(map[int][]event{10: rows}, 10)
		if err != nil || !r.Complete || (r.REPBreakdown != nil) != advertised {
			t.Fatalf("case aliases changed capability/detail: %+v, %v", r, err)
		}
	}
}

func TestREPBreakdownRejectsMissingPartitionsAndOverflowWithoutTotals(t *testing.T) {
	for name, edit := range map[string]func(*event, *event){
		"missing counter":            func(_ *event, e *event) { e.REPMOVSBytes = nil },
		"unadvertised":               func(s *event, _ *event) { s.REPBreakdownVersion = nil },
		"byte partition":             func(_ *event, e *event) { e.REPBytes++ },
		"entry partition":            func(_ *event, e *event) { e.REPEntries++ },
		"MOVS bytes without entries": func(_ *event, e *event) { e.REPMOVSEntries = repCount(0); e.REPSTOSEntries = repCount(5) },
		"STOS bytes without entries": func(_ *event, e *event) { e.REPMOVSEntries = repCount(5); e.REPSTOSEntries = repCount(0) },
		"disabled":                   func(s *event, e *event) { s.REPEnabled = false; e.REPBytes, e.REPEntries = 0, 0 },
		"byte overflow": func(_ *event, e *event) {
			e.REPMOVSBytes, e.REPSTOSBytes, e.REPBytes = repCount(math.MaxUint64), repCount(1), 0
		},
		"entry overflow": func(_ *event, e *event) {
			e.REPMOVSBytes, e.REPSTOSBytes, e.REPBytes = repCount(0), repCount(0), 0
			e.REPMOVSEntries, e.REPSTOSEntries, e.REPEntries = repCount(math.MaxUint64), repCount(1), 0
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := advertiseREP(start(10, "10-1", "initial", 0, ""))
			e := withREPBreakdown(end(s, 2, 100), REPBreakdown{Version: 1, MOVSBytes: 17, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 3})
			edit(&s, &e)
			r := collectREPRows(t, map[int][]event{10: {s, e}})
			if !r.ValidUnavailableResult() || r.REPBreakdown != nil {
				t.Fatalf("invalid detail exposed totals: %+v", r)
			}
		})
	}
	for _, entries := range []bool{false, true} {
		rows := basicTree()
		for pid, stream := range rows {
			stream[0] = advertiseREP(stream[0])
			part := REPBreakdown{Version: 1, MOVSBytes: 1, MOVSEntries: 1}
			if pid == 10 {
				part.MOVSBytes = math.MaxUint64
			}
			if entries {
				part.MOVSEntries, part.MOVSBytes = part.MOVSBytes, 0
			}
			stream[len(stream)-1] = withREPBreakdown(stream[len(stream)-1], part)
		}
		r := collectREPRows(t, rows)
		if !r.ValidUnavailableResult() || r.REPBreakdown != nil {
			t.Fatalf("tree overflow exposed totals: %+v", r)
		}
	}
}
