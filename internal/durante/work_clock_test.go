package durante

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSessionLiveWorkClockOptions(t *testing.T) {
	const path = "/work/clock"
	raw := &WorkClockRequest{Version: 1, Budget: 42}
	weighted := &WorkClockRequest{Version: 1, Budget: 42, TimePolicy: &TimePolicy{}}
	for _, tc := range []struct {
		name, path string
		plan       *WorkClockRequest
		wantStrict bool
	}{
		{"no live clock", "", nil, false},
		{"raw work units", path, raw, false},
		{"model weighted", path, weighted, EngineProfile == engineProfileV12},
		{"no live file", "", weighted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := &Session{config: Config{Drrun: "drrun", Client: "client.so"}}
			argv := session.WrapWithWorkClock([]string{"target"}, tc.path, tc.plan)
			want := []string{"-rep_bytes"}
			if tc.path != "" {
				want = append(want, "-live_work_v1", path)
			}
			if tc.wantStrict {
				want = append(want, "-live_work_require_eligible")
			}
			want = append(want, "--", "target")
			i := slices.Index(argv, "-rep_bytes")
			if i < 0 || !slices.Equal(argv[i:], want) {
				t.Fatalf("got client arguments %q, want suffix %q", argv, want)
			}
		})
	}
}

// Simulate invalidation after a bulk read copied the header. The counter
// subsequently published by the writer can already appear in that same read.
type invalidatingClockRecord [48]byte

func (r *invalidatingClockRecord) ReadAt(p []byte, off int64) (int, error) {
	if off == 0 && len(p) == len(r) {
		copy(p[:16], r[:16])
		binary.LittleEndian.PutUint64(r[8:16], 1)
		binary.LittleEndian.PutUint64(r[16:24], 101)
		copy(p[16:], r[16:])
		return len(p), nil
	}
	return bytes.NewReader(r[:]).ReadAt(p, off)
}

func TestWorkClockRejectsCrossingDuringInvalidation(t *testing.T) {
	var live invalidatingClockRecord
	binary.LittleEndian.PutUint64(live[:8], 1)
	binary.LittleEndian.PutUint64(live[16:24], 100)
	var record [48]byte
	if readValidWorkClock(&live, &record) {
		t.Fatal("credited a strict crossing published after invalidation")
	}
	if binary.LittleEndian.Uint64(record[16:24]) != 101 {
		t.Fatal("fixture did not copy the crossing counter")
	}
}

func TestWorkClockSnapshot(t *testing.T) {
	open := func(t *testing.T) *WorkClock {
		t.Helper()
		clock, err := OpenWorkClock(t.TempDir(), os.Getgid())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { clock.Close() })
		return clock
	}
	write := func(t *testing.T, c *WorkClock, words [6]uint64) {
		t.Helper()
		var buf [48]byte
		for i, w := range words {
			binary.LittleEndian.PutUint64(buf[i*8:], w)
		}
		if _, err := c.file.WriteAt(buf[:], 0); err != nil {
			t.Fatal(err)
		}
	}
	clock := open(t)
	if clock.Snapshot() != nil {
		t.Fatal("fresh file is not ready")
	}
	write(t, clock, [6]uint64{1, 0, 8, 4, 2, 0})
	if p := clock.Snapshot(); p == nil || p.Exceeds(14) || !p.Exceeds(13) {
		t.Fatalf("incorrect strict threshold for %+v", p)
	}
	for name, words := range map[string][6]uint64{
		"invalid": {1, 1, 8, 4, 2, 0}, "regression": {1, 0, 8, 3, 2, 0}, "ready lost": {},
		"invalid overflow": {1, 0, 8, 4, 2, 1}, "ready flag": {2},
	} {
		t.Run(name, func(t *testing.T) {
			c := open(t)
			write(t, c, [6]uint64{1, 0, 8, 4, 2, 0})
			if c.Snapshot() == nil {
				t.Fatal("missing initial progress")
			}
			write(t, c, words)
			if c.Snapshot() != nil {
				t.Fatal("retained invalid progress")
			}
			write(t, c, [6]uint64{1, 0, 10, 8, 6, 0})
			if c.Snapshot() != nil {
				t.Fatal("revived an invalid clock")
			}
		})
	}
	for _, name := range []string{"size", "replaced"} {
		t.Run(name, func(t *testing.T) {
			c := open(t)
			write(t, c, [6]uint64{1, 0, 12})
			var err error
			if name == "size" {
				err = c.file.Truncate(47)
			} else if err = os.Rename(c.Path(), filepath.Join(filepath.Dir(c.Path()), "old")); err == nil {
				err = os.WriteFile(c.Path(), make([]byte, 48), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if c.Snapshot() != nil {
				t.Fatal("accepted changed clock file")
			}
		})
	}
}

func TestWorkClockStrictWireAndWideUnits(t *testing.T) {
	for _, invalid := range []string{
		`{"version":1}`, `{"version":1,"budget":null}`, `{"version":1,"budget":1,"Budget":2}`,
		`{"version":1,"budget":1,"budget":2}`, `{"version":2,"budget":0}`,
	} {
		var req WorkClockRequest
		if err := json.Unmarshal([]byte(invalid), &req); err == nil {
			t.Fatalf("accepted %s", invalid)
		}
	}
	var req WorkClockRequest
	if err := json.Unmarshal([]byte(`{"version":1,"budget":0}`), &req); err != nil {
		t.Fatal(err)
	}
	var progress WorkClockProgress
	if json.Unmarshal([]byte(`{"instructions":0,"movs_bytes":0}`), &progress) == nil {
		t.Fatal("missing STOS treated as zero")
	}
	for _, p := range []WorkClockProgress{
		{Instructions: math.MaxUint64, MOVSBytes: 1}, {STOSBytes: math.MaxUint64, OverflowMask: 4},
	} {
		if !p.Valid() || !p.Exceeds(math.MaxUint64) {
			t.Fatalf("wrapped a boundary: %+v", p)
		}
	}
}

func TestWorkClockMatchesTerminalCounts(t *testing.T) {
	u := func(v uint64) *uint64 { return &v }
	report := func() *Report {
		return &Report{Complete: true, Instructions: u(55), UnsupportedREPEntries: u(0),
			REPBreakdown: &REPBreakdown{Version: 1, MOVSBytes: 20, STOSBytes: 3}}
	}
	progress := WorkClockProgress{Instructions: 55, MOVSBytes: 20, STOSBytes: 3}
	if !progress.Matches(report()) {
		t.Fatal("equal counts did not match")
	}
	// The client keeps publishing through signals and CMPS/SCAS entries.
	observed := report()
	observed.SignalsObserved, observed.UnsupportedREPEntries = 2, u(1)
	if !progress.Matches(observed) {
		t.Fatal("signals or CMPS/SCAS entries disqualified equal counts")
	}
	for name, change := range map[string]func(*Report){
		"incomplete":   func(r *Report) { r.Complete = false },
		"instructions": func(r *Report) { r.Instructions = u(54) },
		"no count":     func(r *Report) { r.Instructions = nil },
		"movs":         func(r *Report) { r.REPBreakdown.MOVSBytes = 19 },
		"stos":         func(r *Report) { r.REPBreakdown.STOSBytes = 4 },
		"no breakdown": func(r *Report) { r.REPBreakdown = nil },
		"no entries":   func(r *Report) { r.UnsupportedREPEntries = nil },
	} {
		r := report()
		change(r)
		if progress.Matches(r) {
			t.Fatalf("%s: matched", name)
		}
	}
	if progress.Matches(nil) {
		t.Fatal("nil report matched")
	}
}
