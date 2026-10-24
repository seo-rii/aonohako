package main

import (
	"reflect"
	"testing"
)

func TestRuntimeRefreshBoolean(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "false": false, "0": false, "true": true, "1": true} {
		got, err := parseRuntimeRefresh(raw)
		if err != nil || got != want {
			t.Errorf("parseRuntimeRefresh(%q) = %v, %v; want %v", raw, got, err, want)
		}
	}
	if _, err := parseRuntimeRefresh("tru"); err == nil {
		t.Fatal("a misspelled refresh must fail instead of silently reusing caches")
	}
}

func TestRuntimeRefreshOptions(t *testing.T) {
	if got := runtimeRefreshOptions(false); len(got) != 0 {
		t.Fatalf("ordinary build unexpectedly disables caches: %v", got)
	}
	want := []string{"--pull", "--no-cache-filter", "runtime-foundation,runtime-toolchain"}
	if got := runtimeRefreshOptions(true); !reflect.DeepEqual(got, want) {
		t.Fatalf("refresh options = %v, want %v", got, want)
	}
}
