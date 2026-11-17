// durante-reference collects native/observed runtime pairs and evaluates an explicit
// diagnostic time policy. Collection always uses a temporary local Aonohako API.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/durante/reference"
	"aonohako/internal/processhardening"
	"aonohako/internal/sandbox"
)

func main() {
	if sandbox.MaybeRunFromEnv() {
		return
	}
	if err := processhardening.DisableDumpability(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: durante-reference collect|fit|describe-runtime-options [options]")
		return 1
	}
	switch args[0] {
	case "collect":
		return collect(ctx, args[1:], stdout, stderr)
	case "fit":
		return fit(ctx, args[1:], stdout, stderr)
	case "describe-runtime-options":
		return describeRuntimeOptions(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "expected collect, fit or describe-runtime-options")
		return 1
	}
}

func collect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("collect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	output := flags.String("output", "", "new private output directory (required)")
	language := flags.String("language", "CPP20", "source profile: CPP20, PYTHON3, PYPY3 or RUST2024")
	memoryMB := flags.Int("memory-mb", 0, "native/raw RSS limit in MiB (0: corpus default, CPP20 256; other profiles 512)")
	smoke := flags.Bool("smoke", false, "small integration corpus; cannot qualify a model")
	repetitions := flags.Int("repetitions", 3, "ordered native/observed/native triplets per case (3..9; smoke 1..9)")
	timeout := flags.Duration("timeout", 20*time.Minute, "total collection timeout (at most 1h)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	minimum := 3
	if *smoke {
		minimum = 1
	}
	if flags.NArg() != 0 || *output == "" || *repetitions < minimum || *repetitions > 9 || *timeout <= 0 || *timeout > time.Hour || *memoryMB < 0 || *memoryMB > 4096 {
		fmt.Fprintln(stderr, "require --output, valid repetition count, timeout in (0,1h], and memory-mb in [0,4096]")
		return 1
	}
	profile, err := reference.ResolveCorpusProfile(*language)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		fmt.Fprintln(stderr, "create fresh output directory:", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	path := filepath.Join(*output, "dataset.json")
	dataset, collectErr := reference.Collect(ctx, cfg, reference.CollectOptions{
		Language: profile.SourceLanguage, MemoryMB: *memoryMB, Smoke: *smoke, Repetitions: *repetitions,
		OnSample: func(snapshot reference.Dataset) error { return writeJSON(path, snapshot) },
	})
	if err := writeJSON(path, dataset); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if collectErr != nil {
		fmt.Fprintln(stderr, "collection incomplete; retained", path+":", collectErr)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Dataset  string `json:"dataset"`
		Complete bool   `json:"complete"`
		Samples  int    `json:"samples"`
	}{path, dataset.Complete, len(dataset.Samples)}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func fit(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	flags := flag.NewFlagSet("fit", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "collected dataset.json (required)")
	output := flags.String("output", "", "new private output directory (required)")
	var options reference.FitOptions
	flags.StringVar(&options.Method, "method", durante.TimeMethod, "conversion method (currently only weighted-work-nanoseconds-v1)")
	flags.StringVar(&options.ModelID, "model-id", "", "explicit diagnostic model identity (required)")
	flags.Uint64Var(&options.ModelVersion, "model-version", 0, "positive model version (required)")
	flags.StringVar(&options.ReferenceID, "reference-id", "", "reference collection identity (required)")
	flags.IntVar(&options.MinRepetitions, "min-repetitions", 3, "minimum complete repetitions per case")
	minimumMS := flags.Uint64("min-native-ms", 20, "minimum native CPU time for every observation")
	flags.Uint64Var(&options.MaxNativeSpreadPPM, "max-native-spread-ppm", 100000, "maximum native range/median (100000 = 10%)")
	flags.Uint64Var(&options.MaxWorkSpreadPPM, "max-work-spread-ppm", 1000, "maximum counter range/median (1000 = 0.1%)")
	flags.Uint64Var(&options.MaxErrorPPM, "max-error-ppm", 200000, "maximum per-observation prediction error (200000 = 20%)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || *input == "" || *output == "" || *minimumMS == 0 || *minimumMS > uint64(^uint64(0)/1_000_000) {
		fmt.Fprintln(stderr, "require --input, --output and a positive, representable minimum native duration")
		return 1
	}
	options.MinNativeNs = *minimumMS * 1_000_000
	dataset, digest, err := readDataset(*input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		fmt.Fprintln(stderr, "create fresh output directory:", err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	evaluation, fitErr := reference.Fit(dataset, options)
	code, err := writeEvaluation(ctx, *output, digest, evaluation, fitErr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if fitErr != nil {
		fmt.Fprintln(stderr, fitErr)
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Evaluation string `json:"evaluation"`
		Passed     bool   `json:"passed"`
	}{filepath.Join(*output, "evaluation.json"), code == 0}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return code
}

func readDataset(path string) (reference.Dataset, string, error) {
	var dataset reference.Dataset
	f, err := os.Open(path)
	if err != nil {
		return dataset, "", err
	}
	defer f.Close()
	const maxBytes = 32 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return dataset, "", err
	}
	if len(data) > maxBytes {
		return dataset, "", fmt.Errorf("dataset exceeds 32 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&dataset); err != nil {
		return dataset, "", err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return dataset, "", fmt.Errorf("dataset contains trailing JSON or invalid data")
	}
	digest := sha256.Sum256(data)
	return dataset, hex.EncodeToString(digest[:]), nil
}

// The caller owns a fresh directory. The policy is the last output and exists
// only if the entire evaluation passed. A rejected candidate remains in its
// receipt for diagnosis, never at the policy path used by operators.
func writeEvaluation(ctx context.Context, directory, digest string, evaluation reference.Evaluation, fitErr error) (int, error) {
	fitErr = errors.Join(fitErr, ctx.Err())
	if fitErr != nil {
		evaluation.Passed = false
		if len(evaluation.Failures) == 0 || evaluation.Failures[len(evaluation.Failures)-1] != fitErr.Error() {
			evaluation.Failures = append(evaluation.Failures, fitErr.Error())
		}
	}
	if evaluation.Passed && evaluation.CandidatePolicy == nil {
		return 1, fmt.Errorf("passed evaluation is missing a candidate policy")
	}
	if evaluation.Passed {
		if err := evaluation.CandidatePolicy.Validate(); err != nil {
			return 1, err
		}
	}
	receipt := struct {
		DatasetSHA256 string               `json:"dataset_sha256"`
		Evaluation    reference.Evaluation `json:"evaluation"`
	}{digest, evaluation}
	if err := writeJSON(filepath.Join(directory, "evaluation.json"), receipt); err != nil {
		return 1, err
	}
	if fitErr != nil {
		return 1, nil
	}
	if !evaluation.Passed {
		return 2, nil
	}
	if err := ctx.Err(); err != nil {
		return writeEvaluation(context.Background(), directory, digest, evaluation, err)
	}
	if err := writeJSON(filepath.Join(directory, "policy.json"), evaluation.CandidatePolicy); err != nil {
		return 1, err
	}
	return 0, nil
}

func writeJSON(path string, value any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".reference-*.json")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
