package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/durante/reference"
)

func TestFrozenValidationReceiptsBindFilesOnFailure(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	policyPath := filepath.Join(dir, "frozen.json")
	data := filepath.Join(dir, "dataset.json")
	output := filepath.Join(dir, "result")
	if err := os.WriteFile(manifest, []byte(`{"schema":1,"language":"CPP20","workloads":[{"id":"external","source":"int main() {}","cases":[{"scale":1,"input":"","expected_stdout":"1"}]}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	policy := durante.TimePolicy{ModelID: "frozen", ModelVersion: 1, ReferenceID: "ref",
		RuntimeID: "CPP20@sha256:" + strings.Repeat("a", 64), Family: "cpp", EngineProfile: durante.EngineProfile,
		DispatchProfile: durante.DispatchDefault, InstructionNSNumerator: 1, REPByteNSNumerator: 2, NSDenominator: 3}
	if err := writeJSON(policyPath, policy); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(data, reference.Dataset{}); err != nil {
		t.Fatal(err)
	}
	args := []string{"validate", "--input", data, "--validation-manifest", manifest, "--frozen-policy", policyPath, "--output", output}
	if code := run(context.Background(), args, io.Discard, io.Discard); code != 1 {
		t.Fatalf("incomplete dataset exit = %d", code)
	}
	raw, err := os.ReadFile(filepath.Join(output, "evaluation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Corpus string               `json:"corpus_sha256"`
		Policy string               `json:"frozen_policy_sha256"`
		Data   string               `json:"dataset_sha256"`
		Eval   reference.Evaluation `json:"evaluation"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || receipt.Eval.Passed || len(receipt.Eval.Failures) == 0 {
		t.Fatalf("incomplete validation receipt: %s, %v", raw, err)
	}
	for name, digest := range map[string]string{manifest: receipt.Corpus, policyPath: receipt.Policy, data: receipt.Data} {
		b, err := os.ReadFile(name)
		sum := sha256.Sum256(b)
		if err != nil || digest != hex.EncodeToString(sum[:]) {
			t.Fatalf("%s: wrong exact-byte digest %s: %v", name, digest, err)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "policy.json")); !os.IsNotExist(err) {
		t.Fatalf("validation published policy: %v", err)
	}
	if _, _, err := readValidationInputs(manifest, ""); err == nil {
		t.Fatal("accepted manifest without a frozen policy")
	}
	if err := os.WriteFile(policyPath, []byte(`{"model_id":"one","model_id":"two"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readValidationInputs(manifest, policyPath); err == nil {
		t.Fatal("accepted ambiguous frozen policy")
	}
}
