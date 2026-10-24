package main

import (
	"fmt"
	"strconv"
)

const runtimeRefreshStages = "runtime-foundation,runtime-toolchain"

func parseRuntimeRefresh(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	refresh, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("AONOHAKO_RUNTIME_REFRESH must be a boolean: %w", err)
	}
	return refresh, nil
}

func runtimeRefreshOptions(refresh bool) []string {
	if !refresh {
		return nil
	}
	// Keep the deterministic Go-binary stages cacheable. External apt/pip/npm
	// repositories and download/install scripts are resolved again in both
	// runtime stages, even when a registry cache supplies identical RUN keys.
	return []string{"--pull", "--no-cache-filter", runtimeRefreshStages}
}
