package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"slices"
	"strings"
	"syscall"

	"aonohako/internal/durante"
)

const maxDeadlineJSON = 64 << 10

// These two trusted receipts must name the same capture and selected policy.
type inputDeadlineBinding struct {
	method, action, elf, stdin, policy, reference, selection, sample, result string
}

func (b *inputDeadlineBinding) fields(methodKey string) map[string]any {
	return map[string]any{
		methodKey: &b.method, "action": &b.action, "elf_sha256": &b.elf, "stdin_sha256": &b.stdin,
		"policy_sha256": &b.policy, "reference_sha256": &b.reference, "selection_sha256": &b.selection,
		"current_sample_sha256": &b.sample, "current_result_sha256": &b.result,
	}
}

func digestString(value string) bool {
	raw, err := hex.DecodeString(value)
	return len(raw) == sha256.Size && err == nil && strings.ToLower(value) == value
}

// The closed object format makes an explicitly observed zero distinct from a
// missing counter, and rejects duplicate keys and Go's case-folded key aliases.
func deadlineObject(data []byte, fields map[string]any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return fmt.Errorf("expected receipt object")
	}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		field, exists := fields[key]
		if err != nil || !ok || !exists {
			return fmt.Errorf("unknown or duplicate receipt field %q", key)
		}
		delete(fields, key)
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			return err
		}
		if bytes.Equal(raw, []byte("null")) && key != "reference_index" {
			return fmt.Errorf("null receipt field %q", key)
		}
		if err := json.Unmarshal(raw, field); err != nil {
			return fmt.Errorf("invalid receipt field %q: %w", key, err)
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return fmt.Errorf("invalid end of receipt")
	}
	if _, err := d.Token(); err != io.EOF || len(fields) != 0 {
		return fmt.Errorf("incomplete or trailing receipt data")
	}
	return nil
}

// Opening nonblocking allows a named pipe to be rejected before waiting on a writer.
func openDeadlineFile(path string) (*os.File, int64, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("expected a regular file: %s", path)
	}
	return f, info.Size(), nil
}

func readDeadlineReceipt(path string, fields map[string]any) error {
	f, _, err := openDeadlineFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxDeadlineJSON+1))
	if err != nil {
		return err
	}
	if len(raw) > maxDeadlineJSON {
		return fmt.Errorf("receipt exceeds %d bytes", maxDeadlineJSON)
	}
	return deadlineObject(raw, fields)
}

func fileMatchesDigest(path, expected string) error {
	f, size, err := openDeadlineFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyN(h, f, size); err != nil {
		return err
	}
	var extra [1]byte
	if n, err := f.Read(extra[:]); n != 0 || err != io.EOF || hex.EncodeToString(h.Sum(nil)) != expected {
		return fmt.Errorf("file differs from projection: %s", path)
	}
	return nil
}

func readDeadlineProjection(path string, ms int64) (inputDeadlineBinding, durante.WorkClockRequest, error) {
	var b inputDeadlineBinding
	var req durante.WorkClockRequest
	var schema int
	var kind, authority, calibration string
	var limits []int64
	var weights json.RawMessage
	fields := b.fields("selection_method")
	fields["schema"], fields["kind"], fields["authority"] = &schema, &kind, &authority
	fields["calibration_sha256"], fields["limits_ms"], fields["weights"] = &calibration, &limits, &weights
	if err := readDeadlineReceipt(path, fields); err != nil {
		return b, req, err
	}
	for _, identity := range []string{b.elf, b.stdin, b.policy, b.reference, b.selection, b.sample, b.result, calibration} {
		if !digestString(identity) {
			return b, req, fmt.Errorf("invalid projection SHA-256")
		}
	}
	if schema != 1 || kind != "aonohako_input_deadline_projection" || authority != durante.Authority ||
		!slices.Contains([]string{"shape", "temporal", "footprint"}, b.method) || (b.action != "calibrate" && b.action != "reuse") {
		return b, req, fmt.Errorf("unsupported projection identity")
	}
	if len(limits) == 0 || len(limits) > 16 || !slices.Contains(limits, ms) {
		return b, req, fmt.Errorf("deadline absent from certified limits")
	}
	for i, t := range limits {
		if t <= 0 || t > math.MaxInt64/1_000_000 || (i > 0 && limits[i-1] >= t) {
			return b, req, fmt.Errorf("invalid certified limits")
		}
	}
	var p durante.TimePolicy
	if err := deadlineObject(weights, map[string]any{
		"method": &p.Method, "instruction_ns_numerator": &p.InstructionNSNumerator,
		"rep_byte_ns_numerator": &p.REPByteNSNumerator, "ns_denominator": &p.NSDenominator,
	}); err != nil {
		return b, req, err
	}
	if p.Method != durante.TimeMethod || p.InstructionNSNumerator == 0 || p.REPByteNSNumerator == 0 || p.NSDenominator == 0 {
		return b, req, fmt.Errorf("invalid projected weights")
	}
	req = durante.WorkClockRequest{Version: 1, Budget: uint64(ms) * 1_000_000, TimePolicy: &p}
	return b, req, req.Validate()
}

type deadlineResult struct {
	Schema    int    `json:"schema"`
	Authority string `json:"authority"`
	Selection string `json:"selection_sha256"`
	Policy    string `json:"policy_sha256"`
	LimitMS   int64  `json:"limit_ms"`
	PID       int64  `json:"pid"`
	Status    string `json:"status"`
	Exceeded  bool   `json:"exceeded"`
}

func replayDeadline(projection, live, elf, stdin string, ms int64) (*deadlineResult, error) {
	b, req, err := readDeadlineProjection(projection, ms)
	if err != nil {
		return nil, err
	}
	var observed inputDeadlineBinding
	var schema, exit int
	var pid, polls, limit, timeout, interval, wall, anchor uint64
	var authority, status, observation, stdout string
	var index *uint64
	var prediction, preflight big.Int
	var progress json.RawMessage
	fields := observed.fields("method")
	fields["schema"], fields["authority"], fields["status"], fields["observation"] = &schema, &authority, &status, &observation
	fields["prediction_ns"], fields["progress"], fields["pid"], fields["returncode"], fields["polls"] = &prediction, &progress, &pid, &exit, &polls
	fields["limit_ms"], fields["timeout_ms"], fields["poll_ms"], fields["wall_ns"] = &limit, &timeout, &interval, &wall
	fields["stdout_sha256"], fields["anchor_native_ns"], fields["reference_index"], fields["preflight_prediction_ns"] = &stdout, &anchor, &index, &preflight
	if err := readDeadlineReceipt(live, fields); err != nil {
		return nil, err
	}
	if schema != 1 || authority != durante.Authority || b != observed || limit != uint64(ms) ||
		(b.action == "reuse") != (index != nil) || !digestString(stdout) {
		return nil, fmt.Errorf("live receipt differs from the selected input, policy or limit")
	}
	if pid == 0 || pid > math.MaxInt32 || wall == 0 || interval == 0 || timeout == 0 || anchor == 0 || preflight.Sign() <= 0 || prediction.Sign() < 0 ||
		(status != "within_budget" && status != "budget_exceeded") {
		return nil, fmt.Errorf("incomplete live receipt")
	}
	if !((observation == "terminal" && exit == 0) || (observation == "live" && exit == -9 && polls > 0 && status == "budget_exceeded")) {
		return nil, fmt.Errorf("inconsistent live termination")
	}
	var p durante.WorkClockProgress
	var rep big.Int
	if err := deadlineObject(progress, map[string]any{
		"instructions": &p.Instructions, "movs_bytes": &p.MOVSBytes, "stos_bytes": &p.STOSBytes, "rep_bytes": &rep,
	}); err != nil {
		return nil, err
	}
	var total, stos big.Int
	total.SetUint64(p.MOVSBytes)
	if total.Add(&total, stos.SetUint64(p.STOSBytes)).Cmp(&rep) != 0 {
		return nil, fmt.Errorf("live REP total differs from MOVS and STOS progress")
	}
	exceeded := req.Exceeds(p)
	var budget big.Int
	if exceeded != (status == "budget_exceeded") || exceeded != (prediction.Cmp(budget.SetUint64(req.Budget)) > 0) {
		return nil, fmt.Errorf("projected Go work clock differs from the live decision")
	}
	if err := fileMatchesDigest(elf, b.elf); err != nil {
		return nil, err
	}
	if err := fileMatchesDigest(stdin, b.stdin); err != nil {
		return nil, err
	}
	return &deadlineResult{1, durante.Authority, b.selection, b.policy, ms, int64(pid), status, exceeded}, nil
}

func replayInputDeadline(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("replay-input-deadline", flag.ContinueOnError)
	flags.SetOutput(stderr)
	projection := flags.String("projection", "", "trusted Durante input deadline projection")
	live := flags.String("live", "", "wrapped exact-input Durante live result.json")
	elf := flags.String("elf", "", "original measured ELF file")
	stdin := flags.String("stdin", "", "original input file")
	ms := flags.Int64("limit-ms", 0, "selected certified deadline in milliseconds")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || *ms <= 0 || *projection == "" || *live == "" || *elf == "" || *stdin == "" {
		fmt.Fprintln(stderr, "require --projection, --live, --elf, --stdin and positive --limit-ms")
		return 1
	}
	result, err := replayDeadline(*projection, *live, *elf, *stdin, *ms)
	if err == nil {
		err = json.NewEncoder(stdout).Encode(result)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
