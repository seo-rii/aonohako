package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/durante/reference"
)

func TestEvaluationPublishesOnlyPassedPolicy(t *testing.T) {
	policy := &durante.TimePolicy{
		ModelID: "test", ModelVersion: 1, ReferenceID: "fixture", RuntimeID: "CPP20@sha256:" + strings.Repeat("a", 64),
		Family: "cpp", EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
		InstructionNSNumerator: 7, REPByteNSNumerator: 3, NSDenominator: 10,
	}
	for _, tc := range []struct {
		name    string
		passed  bool
		failure error
		code    int
	}{
		{"accepted", true, nil, 0},
		{"quality_rejected", false, nil, 2},
		{"error_overrides_pass", true, errors.New("incomplete dataset"), 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			evaluation := reference.Evaluation{Schema: 1, Authority: durante.Authority, Passed: tc.passed, CandidatePolicy: policy}
			code, err := writeEvaluation(context.Background(), directory, "dataset-digest", evaluation, tc.failure)
			if err != nil || code != tc.code {
				t.Fatalf("code=%d err=%v", code, err)
			}
			receipt, err := os.ReadFile(filepath.Join(directory, "evaluation.json"))
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				DatasetSHA256 string               `json:"dataset_sha256"`
				Evaluation    reference.Evaluation `json:"evaluation"`
			}
			if err := json.Unmarshal(receipt, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.DatasetSHA256 != "dataset-digest" || decoded.Evaluation.Passed != (tc.code == 0) || decoded.Evaluation.CandidatePolicy == nil {
				t.Fatalf("receipt=%s", receipt)
			}
			data, err := os.ReadFile(filepath.Join(directory, "policy.json"))
			if tc.code != 0 {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected fit published policy: %s, %v", data, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var exported durante.TimePolicy
			if err := json.Unmarshal(data, &exported); err != nil || exported != *policy {
				t.Fatalf("exported=%s err=%v", data, err)
			}
			info, err := os.Stat(filepath.Join(directory, "policy.json"))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("policy permissions: %v %v", info, err)
			}
		})
	}
}

func TestFitPreservesExistingOutput(t *testing.T) {
	directory := t.TempDir()
	input := filepath.Join(directory, "dataset.json")
	if err := writeJSON(input, reference.Dataset{}); err != nil {
		t.Fatal(err)
	}
	policy := filepath.Join(directory, "policy.json")
	if err := os.WriteFile(policy, []byte("existing policy"), 0600); err != nil {
		t.Fatal(err)
	}
	code := run(context.Background(), []string{"fit", "--input", input, "--output", directory, "--model-id", "test", "--model-version", "1", "--reference-id", "fixture"}, io.Discard, io.Discard)
	data, err := os.ReadFile(policy)
	if code != 1 || err != nil || string(data) != "existing policy" {
		t.Fatalf("code=%d policy=%s err=%v", code, data, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "evaluation.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("overwrote output: %v", err)
	}
}

func TestDatasetRequiresOneKnownDocument(t *testing.T) {
	for _, input := range []string{`{"schema":1,"unknown":true}`, `{"schema":1} {"schema":2}`, `{"schema":1} trailing`} {
		path := filepath.Join(t.TempDir(), "dataset.json")
		if err := os.WriteFile(path, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := readDataset(path); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}

func TestCancelledFitDoesNotPublish(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	directory := t.TempDir()
	evaluation := reference.Evaluation{Passed: true}
	code, err := writeEvaluation(ctx, directory, "digest", evaluation, nil)
	if code != 1 || err != nil {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if _, err := os.Stat(filepath.Join(directory, "policy.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("published cancelled policy: %v", err)
	}
	if code := run(ctx, []string{"fit"}, io.Discard, io.Discard); code != 1 {
		t.Fatalf("cancelled fit returned %d", code)
	}
}
