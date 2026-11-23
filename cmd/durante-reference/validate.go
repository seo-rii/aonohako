package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"aonohako/internal/durante/reference"
)

func readValidationInputs(manifest, frozen string) (*reference.ValidationCorpus, *reference.FrozenPolicy, error) {
	if manifest == "" || frozen == "" {
		return nil, nil, errors.New("both --validation-manifest and --frozen-policy are required")
	}
	corpus, err := reference.LoadValidationCorpus(manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("validation manifest: %w", err)
	}
	policy, err := reference.LoadFrozenPolicy(frozen)
	if err != nil {
		return nil, nil, fmt.Errorf("frozen policy: %w", err)
	}
	return corpus, policy, nil
}

func validateFrozen(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "collected dataset.json")
	output := flags.String("output", "", "new private output directory")
	manifest := flags.String("validation-manifest", "", "external manifest used for collection")
	frozen := flags.String("frozen-policy", "", "standalone policy used for collection")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || *input == "" || *output == "" {
		fmt.Fprintln(stderr, "require --input, --output, --validation-manifest and --frozen-policy")
		return 1
	}
	corpus, policy, err := readValidationInputs(*manifest, *frozen)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dataset, datasetDigest, err := readDataset(*input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.Mkdir(*output, 0700); err != nil {
		fmt.Fprintln(stderr, "create fresh output directory:", err)
		return 1
	}
	evaluation, evalErr := reference.ValidateFrozen(dataset, corpus, policy)
	if err := ctx.Err(); err != nil {
		evalErr = errors.Join(evalErr, err)
		evaluation.Passed = false
		evaluation.Failures = append(evaluation.Failures, err.Error())
	}
	receipt := struct {
		DatasetSHA256 string               `json:"dataset_sha256"`
		CorpusSHA256  string               `json:"corpus_sha256"`
		PolicySHA256  string               `json:"frozen_policy_sha256"`
		Evaluation    reference.Evaluation `json:"evaluation"`
	}{datasetDigest, corpus.SHA256(), policy.SHA256(), evaluation}
	path := filepath.Join(*output, "evaluation.json")
	if err := writeJSON(path, receipt); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(struct {
		Evaluation string `json:"evaluation"`
		Passed     bool   `json:"passed"`
	}{path, evaluation.Passed}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if evalErr != nil {
		fmt.Fprintln(stderr, evalErr)
		return 1
	}
	if !evaluation.Passed {
		return 2
	}
	return 0
}
