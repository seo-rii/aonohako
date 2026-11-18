package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"aonohako/internal/api"
	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/execute"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

// Describe with the same operator configuration as execution, without running a
// program or uploading the request. The generated identity is not qualification.
func describeRuntimeOptions(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("describe-runtime-options", flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("request", "", "RunRequest JSON file using the intended raw/peer limits (required)")
	if err := flags.Parse(args); err != nil {
		return 1
	}
	if flags.NArg() != 0 || *input == "" {
		fmt.Fprintln(stderr, "require --request with one RunRequest JSON file")
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	request, err := readRuntimeOptionsRequest(*input)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := api.ApplyRunRuntimePolicies(request, cfg); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	descriptions, err := execute.DescribeWorkMeasurementRuntimeOptions(request, cfg.Execution)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result := struct {
		Schema         int                                                `json:"schema"`
		Authority      string                                             `json:"authority"`
		RuntimeOptions []execute.WorkMeasurementRuntimeOptionsDescription `json:"runtime_options"`
	}{1, durante.Authority, descriptions}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func readRuntimeOptionsRequest(path string) (*model.RunRequest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const maxBytes = runvalidation.MaxJSONRequestBytes
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("runtime-options request exceeds %d bytes", maxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request *model.RunRequest
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	if request == nil {
		return nil, fmt.Errorf("runtime-options request must be an object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("runtime-options request contains trailing JSON or invalid data")
	}
	return request, nil
}
