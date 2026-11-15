package reference

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func TestCollectCorpusPreservesOptionalREPBreakdown(t *testing.T) {
	for _, tc := range []struct {
		name      string
		bytes     uint64
		entries   uint64
		breakdown *durante.REPBreakdown
	}{
		{name: "unspecified", bytes: 14, entries: 3},
		{name: "observed zero", breakdown: &durante.REPBreakdown{Version: 1}},
		{name: "zero count entries", entries: 3, breakdown: &durante.REPBreakdown{Version: 1, MOVSEntries: 2, STOSEntries: 1}},
		{name: "copy and fill", bytes: 14, entries: 3, breakdown: &durante.REPBreakdown{Version: 1, MOVSBytes: 9, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dataset := collectionTestDataset()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/compile" {
					collectionTestWriteResult(t, w, model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(false)}})
					return
				}
				var request model.RunRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				response := collectionTestNative()
				if request.WorkMeasurement {
					response = collectionTestMetered(collectionTestDataset())
					report := response.WorkMeasurement
					report.REPBytes, report.REPEntries = &tc.bytes, &tc.entries
					report.REPBreakdown = tc.breakdown
				}
				collectionTestWriteResult(t, w, response)
			}))
			defer server.Close()
			var checkpoint []byte
			callbacks := 0
			options := CollectOptions{Smoke: true, Repetitions: 1, OnSample: func(snapshot Dataset) error {
				callbacks++
				var err error
				checkpoint, err = json.Marshal(snapshot)
				if err != nil {
					return err
				}
				if detail := snapshot.Samples[0].Metered.WorkMeasurement.REPBreakdown; detail != nil {
					detail.MOVSBytes = math.MaxUint64
					detail.STOSEntries = math.MaxUint64
				}
				return nil
			}}
			corpus := []Workload{{ID: "split-unit", Source: "int main(){}", Cases: []Case{{Scale: 1}}}}
			if err := collectCorpus(context.Background(), server.Client(), server.URL, corpus, options, &dataset); err != nil {
				t.Fatal(err)
			}
			if callbacks != 1 || len(dataset.Samples) != 1 {
				t.Fatalf("callbacks=%d samples=%d", callbacks, len(dataset.Samples))
			}
			encoded, err := json.Marshal(dataset)
			if err != nil {
				t.Fatal(err)
			}
			for _, data := range [][]byte{checkpoint, encoded} {
				if bytes.Contains(data, []byte(`"rep_breakdown"`)) != (tc.breakdown != nil) {
					t.Fatalf("unspecified split and measured zero became indistinguishable: %s", data)
				}
				var decoded Dataset
				if err := json.Unmarshal(data, &decoded); err != nil {
					t.Fatal(err)
				}
				sample := decoded.Samples[0]
				report := sample.Metered.WorkMeasurement
				if !reflect.DeepEqual(report.REPBreakdown, tc.breakdown) || *report.REPBytes != tc.bytes || *report.REPEntries != tc.entries {
					t.Fatalf("collection or snapshot changed the observed split: %+v", report)
				}
				if sample.NativeBefore.WorkMeasurement != nil || sample.NativeAfter.WorkMeasurement != nil {
					t.Fatal("a native observation acquired metered counters")
				}
			}
		})
	}
}

func TestReferenceRejectsInvalidREPPartitions(t *testing.T) {
	for name, edit := range map[string]func(*durante.REPBreakdown){
		"unknown version": func(d *durante.REPBreakdown) { d.Version = 2 },
		"byte partition":  func(d *durante.REPBreakdown) { d.MOVSBytes++ },
		"entry partition": func(d *durante.REPBreakdown) { d.STOSEntries++ },
		"MOVS no entries": func(d *durante.REPBreakdown) { d.MOVSEntries, d.STOSEntries = 0, 3 },
		"STOS no entries": func(d *durante.REPBreakdown) { d.MOVSEntries, d.STOSEntries = 3, 0 },
		"byte overflow":   func(d *durante.REPBreakdown) { d.MOVSBytes, d.STOSBytes = math.MaxUint64, 1 },
		"entry overflow":  func(d *durante.REPBreakdown) { d.MOVSEntries, d.STOSEntries = math.MaxUint64, 1 },
	} {
		t.Run(name, func(t *testing.T) {
			dataset := collectionTestDataset()
			response := collectionTestMetered(dataset)
			combinedBytes, entries := uint64(14), uint64(3)
			response.WorkMeasurement.REPBytes, response.WorkMeasurement.REPEntries = &combinedBytes, &entries
			response.WorkMeasurement.REPBreakdown = &durante.REPBreakdown{Version: 1, MOVSBytes: 9, STOSBytes: 5, MOVSEntries: 2, STOSEntries: 1}
			if err := validateCollectedMetered(response, dataset); err != nil {
				t.Fatalf("valid fixture: %v", err)
			}
			edit(response.WorkMeasurement.REPBreakdown)
			if err := validateCollectedMetered(response, dataset); err == nil {
				t.Fatal("collection accepted an invalid operation partition")
			}
			full := syntheticDataset()
			full.Samples[0].Metered.WorkMeasurement = response.WorkMeasurement
			evaluation, err := Fit(full, syntheticFitOptions())
			if err == nil || evaluation.Passed || evaluation.CandidatePolicy != nil {
				t.Fatalf("invalid operation partition reached coefficient fitting: %+v, %v", evaluation, err)
			}
		})
	}
}

func TestFitPreservesREPDetailWithoutChangingCombinedModel(t *testing.T) {
	dataset, options := syntheticDataset(), syntheticFitOptions()
	baseline := requireFit(t, dataset, options)
	if !baseline.Passed {
		t.Fatalf("valid unspecified fixture failed: %+v", baseline)
	}
	for i := range dataset.Samples {
		sample := &dataset.Samples[i]
		report := sample.Metered.WorkMeasurement
		detail := &durante.REPBreakdown{Version: 1}
		if sample.WorkloadID == "rep_fill" || sample.Repetition == 2 {
			detail.STOSBytes, detail.STOSEntries = *report.REPBytes, *report.REPEntries
		} else {
			detail.MOVSBytes, detail.MOVSEntries = *report.REPBytes, *report.REPEntries
		}
		report.REPBreakdown = detail
	}
	before, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	got := requireFit(t, dataset, options)
	if !reflect.DeepEqual(got, baseline) {
		t.Fatalf("operation detail changed the existing two-coefficient evaluation: got=%+v baseline=%+v", got, baseline)
	}
	after, err := json.Marshal(dataset)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("fitting removed or rewrote observed operation details: %v", err)
	}
}

func TestLegacyDatasetRetainsItsIdentityAndCannotQualifyCurrentRuntime(t *testing.T) {
	dataset := syntheticDataset()
	legacy := durante.InstallationIdentity{
		SourceCommit: "21397969d18c74ceef816779aba026358f483250", RuntimeSourceCommit: "6847bb210bea0ca11d1bf47704383d6a075da490",
		RuntimePatchSHA256: "7b2fbaca84e259aeeafdfc4902ce7a330c48cec51e16804fe74ee3b99ea39f6d",
		LauncherSHA256:     strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
	}
	for i := range dataset.Samples {
		identity := legacy
		report := dataset.Samples[i].Metered.WorkMeasurement
		report.Installation = &identity
		// Preserve the historical profile too; this fixture must never be
		// relabeled as a current-runtime observation merely to make Fit pass.
		report.EngineProfile = "dynamorio-11.3-durante-v3-vm32m"
	}
	before, err := json.Marshal(dataset)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte(`"rep_breakdown"`)) || bytes.Contains(before, []byte(`"runtime_code_replay_patch_sha256"`)) {
		t.Fatal("legacy fixture must contain neither operation detail nor the later replay-patch identity")
	}
	var decoded Dataset
	if err := json.Unmarshal(before, &decoded); err != nil {
		t.Fatalf("cannot read the original legacy evidence: %v", err)
	}
	for _, sample := range decoded.Samples {
		report := sample.Metered.WorkMeasurement
		if report.REPBreakdown != nil || report.Installation == nil || *report.Installation != legacy || report.EngineProfile != "dynamorio-11.3-durante-v3-vm32m" {
			t.Fatal("decoding upgraded legacy counters or installation identity")
		}
	}
	evaluation, err := Fit(decoded, syntheticFitOptions())
	if err == nil || evaluation.Passed || evaluation.CandidatePolicy != nil || !strings.Contains(err.Error(), "installation identity") {
		t.Fatalf("legacy installation qualified as the current runtime: %+v, %v", evaluation, err)
	}
	after, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("rejected legacy evaluation rewrote the preserved evidence: %v", err)
	}
}
