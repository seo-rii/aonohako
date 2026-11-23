package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/durante/reference"
)

func TestCollectRejectsUnknownLanguageBeforeCreatingOutput(t *testing.T) {
	for _, extra := range [][]string{
		{"--language", "JAVA21"}, {"--language", "binary"}, {"--language", "PYTHON3", "--memory-mb", "-1"},
		{"--language", "PYTHON3", "--corpus-id", reference.RustLongCorpusID},
		{"--language", "RUST2024", "--corpus-id", "unknown"},
		{"--language", "CPP20", "--corpus-id", reference.CorpusID, "--validation-manifest", "manifest"},
	} {
		output := filepath.Join(t.TempDir(), "collection")
		args := append([]string{"collect", "--output", output, "--smoke", "--repetitions", "1"}, extra...)
		var stderr bytes.Buffer
		if code := run(context.Background(), args, io.Discard, &stderr); code != 1 {
			t.Fatalf("invalid selection returned %d: %s", code, stderr.String())
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("invalid runtime selection created output", err)
		}
	}
}

func TestCollectRecordsExplicitRustCorpus(t *testing.T) {
	runtimeOptionsCLIEnvironment(t)
	output := filepath.Join(t.TempDir(), "collection")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	args := []string{"collect", "--output", output, "--language", "RUST2024", "--corpus-id", reference.RustLongCorpusID}
	if code := run(ctx, args, io.Discard, &stderr); code != 1 || !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("expected interrupted collection: code=%d %s", code, stderr.String())
	}
	data, _, err := readDataset(filepath.Join(output, "dataset.json"))
	if err != nil || data.CorpusID != reference.RustLongCorpusID || data.SourceLanguage != "RUST2024" || data.Complete {
		t.Fatalf("incorrect corpus identity in collection receipt: %+v %v", data, err)
	}
}

func TestRuntimeFitRejectsUnsupportedMethodWithoutPolicyExport(t *testing.T) {
	directory := t.TempDir()
	input, output := filepath.Join(directory, "dataset.json"), filepath.Join(directory, "fit")
	data := reference.Dataset{Schema: 1, CorpusID: reference.RuntimeCorpusID, SourceLanguage: "PYTHON3"}
	if err := writeJSON(input, data); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"fit", "--input", input, "--output", output, "--method", "unknown-method", "--model-id", "test", "--model-version", "1", "--reference-id", "test"}, io.Discard, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "supports only") {
		t.Fatalf("unsupported method did not fail explicitly: code=%d %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(output, "evaluation.json")); err != nil {
		t.Fatal("failure receipt missing", err)
	}
	if _, err := os.Stat(filepath.Join(output, "policy.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsupported method exported a policy", err)
	}
	decoded, _, err := readDataset(input)
	if err != nil || decoded.SourceLanguage != "PYTHON3" || decoded.CorpusID != reference.RuntimeCorpusID {
		t.Fatal("runtime data was reinterpreted as C++", err)
	}
}
