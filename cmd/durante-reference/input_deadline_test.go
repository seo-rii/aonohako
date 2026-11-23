package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/durante"
)

type deadlineFixture struct {
	projection, live map[string]any
	dir              string
}

func newDeadlineFixture(t *testing.T) deadlineFixture {
	t.Helper()
	dir := t.TempDir()
	for name, value := range map[string]string{"elf": "measured bytes", "stdin": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	hash := func(value string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(value))) }
	projection := map[string]any{
		"kind": "aonohako_input_deadline_projection", "selection_method": "footprint",
		"calibration_sha256": hash("calibration"), "limits_ms": []int{1, 2},
		"weights": map[string]any{"method": durante.TimeMethod, "instruction_ns_numerator": 1,
			"rep_byte_ns_numerator": 2, "ns_denominator": 1},
	}
	live := map[string]any{
		"method": "footprint", "reference_index": 0, "pid": 101, "returncode": 0, "polls": 1,
		"limit_ms": 1, "poll_ms": 5, "timeout_ms": 120000, "wall_ns": 2000000, "anchor_native_ns": 1000000,
		"stdout_sha256": hash("output"), "status": "within_budget", "observation": "terminal",
		"prediction_ns": 1000000, "preflight_prediction_ns": 1000000,
		"progress": map[string]any{"instructions": 999994, "movs_bytes": 1, "stos_bytes": 2, "rep_bytes": 3},
	}
	for k, v := range map[string]any{"schema": 1, "action": "reuse", "authority": "diagnostic_only",
		"elf_sha256": hash("measured bytes"), "stdin_sha256": hash(""), "policy_sha256": hash("policy"),
		"reference_sha256": hash("reference"), "selection_sha256": hash("selection"),
		"current_sample_sha256": hash("sample"), "current_result_sha256": hash("result")} {
		projection[k], live[k] = v, v
	}
	return deadlineFixture{projection, live, dir}
}

func (f deadlineFixture) replay(t *testing.T, ms string, mutate func(string) string) (int, string) {
	t.Helper()
	for name, obj := range map[string]any{"projection": f.projection, "live": f.live} {
		raw, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		if name == "live" && mutate != nil {
			raw = []byte(mutate(string(raw)))
		}
		if err := os.WriteFile(filepath.Join(f.dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	path := func(name string) string { return filepath.Join(f.dir, name) }
	code := run(context.Background(), []string{"replay-input-deadline", "--projection", path("projection"),
		"--live", path("live"), "--elf", path("elf"), "--stdin", path("stdin"), "--limit-ms", ms}, &stdout, &stderr)
	if code != 0 && (stdout.Len() != 0 || stderr.Len() == 0) {
		t.Fatalf("failure must only explain on stderr: %q / %q", stdout.String(), stderr.String())
	}
	if code == 0 && stderr.Len() != 0 {
		t.Fatalf("unexpected stderr on success: %s", stderr.String())
	}
	return code, stdout.String()
}

func TestReplayDeadlineUsesStrictWeightedCrossing(t *testing.T) {
	for _, mode := range []string{"equality", "live", "late-terminal", "fractional", "wide-rep"} {
		t.Run(mode, func(t *testing.T) {
			f := newDeadlineFixture(t)
			p := f.live["progress"].(map[string]any)
			exceeded := mode != "equality"
			if exceeded {
				p["instructions"] = 999995
				f.live["status"] = "budget_exceeded"
				f.live["prediction_ns"] = 1000001
			}
			if mode == "live" {
				f.live["observation"], f.live["returncode"] = "live", -9
			}
			if mode == "fractional" {
				// Go sees 1 ms plus 1/3 ns; the producer rounds the same side of the deadline.
				f.projection["weights"].(map[string]any)["ns_denominator"] = 3
				p["instructions"] = 2999995
			}
			if mode == "wide-rep" {
				p["movs_bytes"], p["stos_bytes"] = uint64(math.MaxUint64), uint64(math.MaxUint64)
				p["rep_bytes"] = json.RawMessage("36893488147419103230")
				f.live["prediction_ns"] = json.RawMessage("73786976294839206455")
				f.projection["action"], f.live["action"], f.live["reference_index"] = "calibrate", "calibrate", nil
			}
			code, result := f.replay(t, "1", nil)
			var got deadlineResult
			if err := json.Unmarshal([]byte(result), &got); err != nil || code != 0 || got.Authority != durante.Authority ||
				got.Exceeded != exceeded || got.Status != f.live["status"] || got.Selection != f.projection["selection_sha256"] {
				t.Fatalf("replay code %d, result %q, error %v", code, result, err)
			}
		})
	}
}

func TestReplayDeadlineRefusesMismatchedInputsAndReceipts(t *testing.T) {
	for _, field := range []string{"elf", "stdin", "policy_sha256", "reference_sha256", "selection_sha256", "current_sample_sha256", "current_result_sha256",
		"method", "reference_index", "limit_ms", "action", "status", "returncode", "prediction_ns", "absent-limit", "absent-selection", "unsorted", "weight", "wrong-clock", "rep-total", "missing-counter", "overflow", "overflow-mask", "null-limit", "oversize"} {
		t.Run(field, func(t *testing.T) {
			f := newDeadlineFixture(t)
			ms := "1"
			switch field {
			case "elf", "stdin":
				if err := os.WriteFile(filepath.Join(f.dir, field), []byte("other"), 0600); err != nil {
					t.Fatal(err)
				}
			case "method":
				f.live[field] = "temporal"
			case "reference_index":
				f.live[field] = nil
			case "action":
				f.live[field] = "calibrate"
			case "limit_ms":
				f.live[field] = 2
			case "status":
				f.live[field] = "budget_exceeded"
			case "returncode":
				f.live[field] = -9
			case "prediction_ns":
				f.live[field] = 1000001
			case "absent-limit":
				ms = "3"
			case "absent-selection":
				delete(f.live, "selection_sha256")
			case "unsorted":
				f.projection["limits_ms"] = []int{2, 1}
			case "weight":
				f.projection["weights"].(map[string]any)["instruction_ns_numerator"] = 0
			case "wrong-clock":
				f.projection["weights"].(map[string]any)["rep_byte_ns_numerator"] = 3
			case "rep-total":
				f.live["progress"].(map[string]any)["rep_bytes"] = 4
			case "missing-counter":
				delete(f.live["progress"].(map[string]any), "movs_bytes")
			case "overflow":
				f.live["progress"].(map[string]any)["stos_bytes"] = json.RawMessage("18446744073709551616")
			case "overflow-mask":
				progress := f.live["progress"].(map[string]any)
				progress["instructions"], progress["overflow_mask"] = uint64(math.MaxUint64), 1
				f.live["status"], f.live["prediction_ns"] = "budget_exceeded", uint64(math.MaxUint64)
			case "null-limit":
				f.live["limit_ms"] = nil
			case "oversize":
				f.live["stdout_sha256"] = strings.Repeat("0", maxDeadlineJSON)
			default:
				f.live[field] = strings.Repeat("1", 64)
			}
			if code, _ := f.replay(t, ms, nil); code == 0 {
				t.Fatal("inconsistent input accepted")
			}
		})
	}
	for name, mutate := range map[string]func(string) string{
		"duplicate": func(s string) string { return strings.Replace(s, `"movs_bytes":1`, `"movs_bytes":0,"movs_bytes":1`, 1) },
		"alias":     func(s string) string { return strings.Replace(s, "instructions", "Instructions", 1) },
		"trailing":  func(s string) string { return s + " {}" },
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := newDeadlineFixture(t).replay(t, "1", mutate); code == 0 {
				t.Fatal("ambiguous JSON accepted")
			}
		})
	}
}
