package durante

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"slices"
)

// WorkClockV1 is measured in units of basic-block instructions plus requested
// MOVS and STOS bytes. It has no machine-time conversion.
const WorkClockV1 = "instructions_plus_movs_stos_bytes_v1"

type WorkClockRequest struct {
	Version    int         `json:"version"`
	Budget     uint64      `json:"budget"`
	TimePolicy *TimePolicy `json:"-"`
}

type WorkClockProgress struct {
	Instructions uint64 `json:"instructions"`
	MOVSBytes    uint64 `json:"movs_bytes"`
	STOSBytes    uint64 `json:"stos_bytes"`
	OverflowMask uint64 `json:"overflow_mask,omitempty"`
}

func (p WorkClockProgress) Valid() bool {
	return p.OverflowMask <= 7 &&
		(p.OverflowMask&1 == 0 || p.Instructions == math.MaxUint64) &&
		(p.OverflowMask&2 == 0 || p.MOVSBytes == math.MaxUint64) &&
		(p.OverflowMask&4 == 0 || p.STOSBytes == math.MaxUint64)
}

func (p WorkClockProgress) Exceeds(budget uint64) bool {
	iAndMovs, carry1 := bits.Add64(p.Instructions, p.MOVSBytes, 0)
	total, carry2 := bits.Add64(iAndMovs, p.STOSBytes, 0)
	return p.OverflowMask != 0 || carry1 != 0 || carry2 != 0 || total > budget
}

// Matches reports whether a natural exit's live progress equals the complete
// terminal report. The raw metric counts through signals and CMPS/SCAS.
// Weighted clocks check time eligibility separately.
func (p WorkClockProgress) Matches(r *Report) bool {
	return r != nil && r.Complete && r.Instructions != nil && r.REPBreakdown != nil &&
		r.UnsupportedREPEntries != nil &&
		p == (WorkClockProgress{Instructions: *r.Instructions, MOVSBytes: r.REPBreakdown.MOVSBytes, STOSBytes: r.REPBreakdown.STOSBytes})
}

type WorkClockReceipt struct {
	Version      int                `json:"version"`
	ModelBinding string             `json:"model_binding,omitempty"`
	Metric       string             `json:"metric"`
	Budget       uint64             `json:"budget"`
	State        string             `json:"state"`
	Progress     *WorkClockProgress `json:"progress,omitempty"`
}

// WorkClock holds the owner's descriptor for one fresh role-local live_work_v1
// file. ReadAt avoids mapping a file writable by the sandbox into the worker.
type WorkClock struct {
	file           *os.File
	progress       WorkClockProgress
	ready, invalid bool
}

func OpenWorkClock(root string, gid int) (*WorkClock, error) {
	f, err := os.CreateTemp(root, ".work-clock-*")
	if err != nil {
		return nil, err
	}
	if err = f.Truncate(48); err == nil {
		err = f.Chown(-1, gid)
	}
	if err == nil {
		err = f.Chmod(0660)
	}
	if err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return &WorkClock{file: f}, nil
}

func (c *WorkClock) Path() string { return c.file.Name() }
func (c *WorkClock) Close() error { return c.file.Close() }

// A single read can copy invalid=0 before invalidation and copy counters
// published afterwards. Confirm the sticky flag after collecting the counters.
func readValidWorkClock(file io.ReaderAt, record *[48]byte) bool {
	invalid := record[8:16]
	if _, err := file.ReadAt(record[:], 0); err != nil || binary.LittleEndian.Uint64(invalid) != 0 {
		return false
	}
	_, err := file.ReadAt(invalid, 8)
	return err == nil && binary.LittleEndian.Uint64(invalid) == 0
}

// Snapshot validates the cumulative words from the C client. A mixed snapshot
// of monotone words is a lower bound. An invalid or regressing observation
// permanently disables this clock; the sandbox keeps its independent limits.
func (c *WorkClock) Snapshot() *WorkClockProgress {
	if c.invalid {
		return nil
	}
	var raw [48]byte
	info, err := c.file.Stat()
	current, pathErr := os.Lstat(c.file.Name())
	if err != nil || pathErr != nil || !os.SameFile(info, current) || info.Size() != int64(len(raw)) {
		c.invalid = true
		return nil
	}
	if !readValidWorkClock(c.file, &raw) {
		c.invalid = true
		return nil
	}
	word := binary.LittleEndian.Uint64
	ready := word(raw[0:8])
	p := WorkClockProgress{Instructions: word(raw[16:24]), MOVSBytes: word(raw[24:32]), STOSBytes: word(raw[32:40]), OverflowMask: word(raw[40:48])}
	prev := c.progress
	if ready > 1 || !p.Valid() ||
		ready == 0 && (c.ready || p != (WorkClockProgress{})) ||
		prev.Instructions > p.Instructions || prev.MOVSBytes > p.MOVSBytes || prev.STOSBytes > p.STOSBytes ||
		prev.OverflowMask&p.OverflowMask != prev.OverflowMask {
		c.invalid = true
		return nil
	}
	if ready == 0 {
		return nil
	}
	c.ready, c.progress = true, p
	return &p
}

func (r WorkClockRequest) Validate() error {
	if r.Version != 1 {
		return fmt.Errorf("work_measurement_clock requires version 1")
	}
	return nil
}

func (r WorkClockRequest) Receipt(progress *WorkClockProgress) *WorkClockReceipt {
	clock := &WorkClockReceipt{Version: 1, Metric: r.MetricName(), ModelBinding: r.ModelBinding(), Budget: r.Budget, State: "unavailable"}
	if progress != nil {
		clock.State, clock.Progress = "observed", progress
	}
	return clock
}

// A missing counter or budget is not an observed zero. Clock objects use a
// closed, exact-key wire format on both sides of a remote execution.
func decodeWorkClockObject(data []byte, dest any, required, optional []string) error {
	fields, err := manifestObject(data)
	if err != nil {
		return err
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return fmt.Errorf("missing work clock field %q", key)
		}
	}
	for key, value := range fields {
		if !slices.Contains(required, key) && !slices.Contains(optional, key) || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("unexpected or null work clock field %q", key)
		}
	}
	return json.Unmarshal(data, dest)
}

func (r *WorkClockRequest) UnmarshalJSON(data []byte) error {
	if len(data) > 256 {
		return fmt.Errorf("work clock request exceeds 256 bytes")
	}
	type plain WorkClockRequest
	var out plain
	if err := decodeWorkClockObject(data, &out, []string{"version", "budget"}, nil); err != nil {
		return err
	}
	*r = WorkClockRequest(out)
	return r.Validate()
}

func (p *WorkClockProgress) UnmarshalJSON(data []byte) error {
	type plain WorkClockProgress
	var out plain
	if err := decodeWorkClockObject(data, &out, []string{"instructions", "movs_bytes", "stos_bytes"}, []string{"overflow_mask"}); err != nil {
		return err
	}
	*p = WorkClockProgress(out)
	if !p.Valid() {
		return fmt.Errorf("invalid work clock overflow mask")
	}
	return nil
}

func (r *WorkClockReceipt) UnmarshalJSON(data []byte) error {
	type plain WorkClockReceipt
	var out plain
	if err := decodeWorkClockObject(data, &out, []string{"version", "metric", "budget", "state"}, []string{"model_binding", "progress"}); err != nil {
		return err
	}
	*r = WorkClockReceipt(out)
	return nil
}
