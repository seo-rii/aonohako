package workmeter

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func start(pid int, segment string, origin string, parent int, parentSegment string) event {
	return event{Schema: 1, ClientVersion: 1, Metric: Metric, Authority: Authority, PID: pid, Segment: segment, Seq: 1, Event: "start", SDKVersion: "11.3.0", Origin: origin, ParentPID: parent, ParentSegment: parentSegment, REPEnabled: true}
}
func next(base event, seq uint64, kind string) event { base.Seq = seq; base.Event = kind; return base }
func end(base event, seq, count uint64) event {
	r := next(base, seq, "end")
	r.Instructions = count
	r.Valid = true
	r.ThreadsComplete = true
	r.ThreadsStarted = 1
	r.ThreadsFinished = 1
	r.ExitSeen = true
	r.Quality = "uninterrupted"
	return r
}
func basicTree() map[int][]event {
	p := start(10, "10-1", "initial", 0, "")
	c := start(20, "20-1", "fork", 10, p.Segment)
	spawn := next(p, 2, "spawn")
	spawn.ChildPID = 20
	spawn.Syscall = 57
	spawn.Supported = true
	return map[int][]event{10: {p, spawn, end(p, 3, 100)}, 20: {c, end(c, 2, 200)}}
}
func TestAggregateForkAndExecWithoutDoubleCounting(t *testing.T) {
	rows := basicTree()
	c := rows[20][0]
	checkpoint := end(c, 2, 200)
	checkpoint.Event = "exec_attempt"
	checkpoint.ExitSeen = false
	childImage := start(20, "20-2", "initial", 0, "")
	rows[20] = []event{c, checkpoint, childImage, end(childImage, 2, 300)}
	r, err := aggregate(rows, 10)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || *r.Instructions != 600 || r.Processes != 2 || r.Images != 3 {
		t.Fatalf("unexpected aggregate: %+v", r)
	}
}
func TestFailedExecUsesFinalCumulativeCountOnly(t *testing.T) {
	p := start(10, "10-1", "initial", 0, "")
	checkpoint := end(p, 2, 100)
	checkpoint.Event = "exec_attempt"
	fail := next(p, 3, "exec_failed")
	fail.Syscall = 59
	fail.Result = -2
	r, err := aggregate(map[int][]event{10: {p, checkpoint, fail, end(p, 4, 150)}}, 10)
	if err != nil || *r.Instructions != 150 || r.Images != 1 {
		t.Fatalf("count=%+v err=%v", r, err)
	}
}
func TestNonzeroExitAndSignalsAreExplicitlyQualified(t *testing.T) {
	p := start(10, "10-1", "initial", 0, "")
	e := end(p, 2, 100)
	e.ExitCode = 7
	e.Signals = 1
	e.Quality = "signals_observed"
	r, err := aggregate(map[int][]event{10: {p, e}}, 10)
	if err != nil || !r.Complete || r.Quality != "signals_observed" || r.SignalsObserved != 1 {
		t.Fatalf("report=%+v err=%v", r, err)
	}
}
func TestAggregateRejectsIncompleteAndInconsistentTree(t *testing.T) {
	tests := map[string]func(map[int][]event){
		"missing child":           func(m map[int][]event) { delete(m, 20) },
		"unannounced child":       func(m map[int][]event) { m[10] = []event{m[10][0], end(m[10][0], 2, 100)} },
		"lineage mismatch":        func(m map[int][]event) { m[20][0].ParentSegment = "10-9" },
		"killed child":            func(m map[int][]event) { m[20] = m[20][:1] },
		"duplicate sequence":      func(m map[int][]event) { m[20][1].Seq = 1 },
		"missing thread":          func(m map[int][]event) { m[20][1].ThreadsFinished = 0 },
		"counter overflow":        func(m map[int][]event) { m[20][1].Instructions = math.MaxUint64 },
		"unsupported spawn":       func(m map[int][]event) { m[10][1].Supported = false },
		"signal quality mismatch": func(m map[int][]event) { m[20][1].Signals = 1 },
		"mixed rep":               func(m map[int][]event) { m[20][0].REPEnabled = false },
		"fake authority":          func(m map[int][]event) { m[20][0].Authority = "authoritative" },
		"reused PID":              func(m map[int][]event) { m[20] = append(m[20], m[20][0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			m := basicTree()
			mutate(m)
			if _, err := aggregate(m, 10); err == nil {
				t.Fatal("invalid tree accepted")
			}
		})
	}
}
func writeTree(t *testing.T, dir string) {
	t.Helper()
	for pid, rows := range basicTree() {
		var b strings.Builder
		for _, r := range rows {
			raw, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			b.Write(raw)
			b.WriteByte('\n')
		}
		name := "process-10.jsonl"
		if pid == 20 {
			name = "process-20.jsonl"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(b.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCollectorReadsCompleteTree(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir)
	r := CollectDirectory(dir, 10)
	if !r.Complete || r.Instructions == nil || *r.Instructions != 300 {
		t.Fatalf("%+v", r)
	}
}
func TestCollectorRejectsUnsafeOrMalformedFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "truncated", "duplicate key", "null field", "extra entry", "oversized", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			writeTree(t, dir)
			p := filepath.Join(dir, "process-20.jsonl")
			switch kind {
			case "symlink":
				os.Remove(p)
				os.Symlink("/etc/passwd", p)
			case "hardlink":
				os.Remove(p)
				os.Link(filepath.Join(dir, "process-10.jsonl"), p)
			case "directory":
				os.Remove(p)
				os.Mkdir(p, 0700)
			case "truncated":
				data, _ := os.ReadFile(p)
				os.WriteFile(p, data[:len(data)-1], 0600)
			case "duplicate key":
				data, _ := os.ReadFile(p)
				data = []byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"schema":1`, 1))
				os.WriteFile(p, data, 0600)
			case "null field":
				data, _ := os.ReadFile(p)
				data = []byte(strings.Replace(string(data), `"parent_pid":10`, `"parent_pid":null`, 1))
				os.WriteFile(p, data, 0600)
			case "extra entry":
				os.WriteFile(filepath.Join(dir, "secret"), []byte("x"), 0600)
			case "oversized":
				os.WriteFile(p, []byte(strings.Repeat("x", maxFileBytes+1)), 0600)
			}
			r := CollectDirectory(dir, 10)
			if r.Complete || r.Instructions != nil || r.Reason == "" {
				t.Fatalf("unsafe report accepted: %+v", r)
			}
		})
	}
}
