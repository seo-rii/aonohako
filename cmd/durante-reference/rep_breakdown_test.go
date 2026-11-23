package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/durante/reference"
	"aonohako/internal/model"
)

// This tests the dataset file codec, not whether an incomplete fixture can
// qualify a model. Fit independently validates the full corpus and identity.
func TestDatasetFilePreservesOptionalREPBreakdown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bytes     uint64
		entries   uint64
		breakdown *durante.REPBreakdown
	}{
		{name: "unspecified", bytes: 18, entries: 3},
		{name: "observed zero", breakdown: &durante.REPBreakdown{Version: 1}},
		{name: "copy and fill", bytes: 18, entries: 3, breakdown: &durante.REPBreakdown{Version: 1, MOVSBytes: 13, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := reference.Dataset{Schema: 1, Samples: []reference.Sample{{
				Metered: model.RunResponse{WorkMeasurement: &durante.Report{
					REPBytes: &tc.bytes, REPEntries: &tc.entries, REPBreakdown: tc.breakdown,
				}},
			}}}
			path := filepath.Join(t.TempDir(), "dataset.json")
			if err := writeJSON(path, original); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(data, []byte(`"rep_breakdown"`)) != (tc.breakdown != nil) {
				t.Fatalf("dataset file confused unspecified detail with measured zero: %s", data)
			}
			decoded, _, err := readDataset(path)
			if err != nil {
				t.Fatal(err)
			}
			report := decoded.Samples[0].Metered.WorkMeasurement
			if !reflect.DeepEqual(report.REPBreakdown, tc.breakdown) || *report.REPBytes != tc.bytes || *report.REPEntries != tc.entries {
				t.Fatalf("dataset reader lost operation details: %+v", report)
			}
			roundTrip, err := json.Marshal(decoded)
			if err != nil || bytes.Contains(roundTrip, []byte(`"rep_breakdown"`)) != (tc.breakdown != nil) {
				t.Fatalf("dataset round trip invented or dropped operation details: %s, %v", roundTrip, err)
			}
		})
	}
}
