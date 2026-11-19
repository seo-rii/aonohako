package reference

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

const exampleValidationCase = `{"scale":1,"input":"","expected_stdout":"42\n"}`
const exampleValidationWorkload = `{"id":"abc-3","source":"int main(){}","cases":[` + exampleValidationCase + `]}`
const exampleValidationManifest = `{"schema":1,"language":"CPP20","workloads":[` + exampleValidationWorkload + `]}`

func TestLoadValidationCorpusBindsExactFileAndKeepsPrivateSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corpus.json")
	data := []byte(exampleValidationManifest + "\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadValidationCorpus(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	workloads, err := c.snapshot()
	if err != nil || c.SHA256() != collectionSHA(data) || len(workloads) != 1 || workloads[0].Split != SplitValidation || workloads[0].Cases[0].ExpectedStdout != "42\n" || workloads[0].Cases[0].Input != "" {
		t.Fatalf("unexpected manifest: %v, %v", c, err)
	}
	if _, err := (&ValidationCorpus{}).snapshot(); err == nil {
		t.Fatal("accepted a corpus that was never loaded")
	}
}

func TestExternalManifestRejectsAmbiguousOrUnboundedInputs(t *testing.T) {
	bad := map[string]string{
		"language":           strings.Replace(exampleValidationManifest, "CPP20", "PYTHON3", 1),
		"schema":             strings.Replace(exampleValidationManifest, `"schema":1`, `"schema":2`, 1),
		"duplicate":          strings.Replace(exampleValidationManifest, `"schema"`, `"schema":1,"schema"`, 1),
		"case folded":        strings.Replace(exampleValidationManifest, `"id"`, `"ID"`, 1),
		"unknown":            strings.Replace(exampleValidationManifest, `"cases"`, `"split":"train","cases"`, 1),
		"missing input":      strings.Replace(exampleValidationManifest, `"input":"",`, "", 1),
		"null input":         strings.Replace(exampleValidationManifest, `"input":""`, `"input":null`, 1),
		"blank oracle":       strings.Replace(exampleValidationManifest, `"42\n"`, `" \t\n"`, 1),
		"invalid UTF8":       strings.Replace(exampleValidationManifest, "abc-3", string([]byte{0xff}), 1),
		"unpaired surrogate": strings.Replace(exampleValidationManifest, `"42\n"`, `"\udc0f"`, 1),
		"high surrogate":     strings.Replace(exampleValidationManifest, `"42\n"`, `"\uD800\u0034"`, 1),
		"bad ID":             strings.Replace(exampleValidationManifest, "abc-3", "abc/3", 1),
		"zero scale":         strings.Replace(exampleValidationManifest, `"scale":1`, `"scale":0`, 1),
		"extra document":     exampleValidationManifest + `{}`,
		"empty workloads":    strings.Replace(exampleValidationManifest, exampleValidationWorkload, "", 1),
		"empty cases":        strings.Replace(exampleValidationManifest, exampleValidationCase, "", 1),
		"repeat workload":    strings.Replace(exampleValidationManifest, exampleValidationWorkload, exampleValidationWorkload+","+exampleValidationWorkload, 1),
		"repeat scale":       strings.Replace(exampleValidationManifest, exampleValidationCase, exampleValidationCase+","+exampleValidationCase, 1),
		"oversize input":     strings.Replace(exampleValidationManifest, `"input":""`, `"input":"`+strings.Repeat("x", validationInputBytes+1)+`"`, 1),
	}
	for name, data := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeValidationCorpus([]byte(data)); err == nil {
				t.Fatal("invalid external corpus was accepted")
			}
		})
	}
	for _, value := range []string{`\ud83d\ude00`, `\\udc00`, `\ufffd`} {
		if _, err := decodeValidationCorpus([]byte(strings.Replace(exampleValidationManifest, `42\n`, value, 1))); err != nil {
			t.Errorf("rejected valid Unicode input %q: %v", value, err)
		}
	}
}

func TestValidationManifestCardinalityLimits(t *testing.T) {
	cases := make([]string, validationMaxCases+1)
	for i := range cases {
		cases[i] = strings.Replace(exampleValidationCase, `"scale":1`, fmt.Sprintf(`"scale":%d`, i+1), 1)
	}
	workloads := make([]string, validationMaxWorkloads+1)
	for i := range workloads {
		workloads[i] = strings.Replace(exampleValidationWorkload, "abc-3", fmt.Sprintf("submission-%d", i), 1)
	}
	// The total case budget applies across programs as well as within one.
	manyCases := strings.Replace(workloads[0], exampleValidationCase, strings.Join(cases[:validationMaxCases], ","), 1)
	for _, ws := range []string{manyCases + "," + workloads[1], strings.Join(workloads, ",")} {
		raw := strings.Replace(exampleValidationManifest, exampleValidationWorkload, ws, 1)
		if _, err := decodeValidationCorpus([]byte(raw)); err == nil {
			t.Fatal("accepted too many cases or workloads")
		}
	}
	for _, ws := range []string{manyCases, strings.Join(workloads[:validationMaxWorkloads], ",")} {
		raw := strings.Replace(exampleValidationManifest, exampleValidationWorkload, ws, 1)
		if _, err := decodeValidationCorpus([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExternalCollectionUsesManifestAndPredeclaredPolicy(t *testing.T) {
	corpus, err := decodeValidationCorpus([]byte(exampleValidationManifest))
	if err != nil {
		t.Fatal(err)
	}
	policy := durante.TimePolicy{ModelID: "frozen", ModelVersion: 1, ReferenceID: "development", Family: "cpp", RuntimeID: "CPP20@sha256:" + strings.Repeat("a", 64), EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault, InstructionNSNumerator: 1, REPByteNSNumerator: 2, NSDenominator: 3}
	frozen := freezeTestPolicy(t, policy)
	opts := CollectOptions{ValidationCorpus: corpus, FrozenPolicy: frozen, Repetitions: 3}
	// Stop before starting the API, after the collector has bound its receipts.
	cfg := collectionTestConfig()
	cfg.MaxActiveRuns = 2
	dataset, err := Collect(context.Background(), cfg, opts)
	if err == nil || !strings.Contains(err.Error(), "max_active_runs") || dataset.CorpusID != ExternalCorpusID || dataset.CorpusSHA256 != corpus.SHA256() || dataset.FrozenPolicySHA256 != frozen.SHA256() {
		t.Fatalf("unexpected collection receipt or error: %+v, %v", dataset, err)
	}
	if _, err := referenceDatasetProfile(dataset); err == nil {
		t.Fatal("fixed training profile accepted the external data")
	}
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
		if request.ExpectedStdout != "42\n" || request.Stdin != "" {
			t.Errorf("wrong external case values")
		}
		if request.WorkMeasurement {
			collectionTestWriteResult(t, w, collectionTestMetered(dataset))
		} else {
			collectionTestWriteResult(t, w, collectionTestNative())
		}
	}))
	defer server.Close()
	dataset.Failures = nil
	if err := collectCorpus(context.Background(), server.Client(), server.URL, corpus.workloads, opts, &dataset); err != nil {
		t.Fatal(err)
	}
	if len(dataset.Samples) != 3 || dataset.Samples[0].Split != SplitValidation || dataset.Samples[0].SourceSHA256 != collectionSHA([]byte(corpus.workloads[0].Source)) {
		t.Fatal("external trial did not record all three held-out samples")
	}
	incompatible := policy
	incompatible.CPUIDProfile = "x86-64-v2"
	latency := policy
	latency.Method, latency.LatencyX16NSNumerator, latency.InstructionNSNumerator = durante.TimeMethodLatencyBoundREP, 1, 0
	for _, bad := range []CollectOptions{
		{ValidationCorpus: corpus, Repetitions: 3, FrozenPolicy: freezeTestPolicy(t, incompatible)},
		{ValidationCorpus: corpus, Repetitions: 3, FrozenPolicy: freezeTestPolicy(t, latency)},
		{ValidationCorpus: corpus, Repetitions: 3},
		{ValidationCorpus: corpus, Repetitions: 3, FrozenPolicy: &FrozenPolicy{}},
		{ValidationCorpus: corpus, Smoke: true, Repetitions: 1, FrozenPolicy: frozen},
		{ValidationCorpus: corpus, Language: "PYTHON3", Repetitions: 3, FrozenPolicy: frozen},
		{FrozenPolicy: frozen, Repetitions: 3},
	} {
		if _, err := Collect(context.Background(), cfg, bad); err == nil || strings.Contains(err.Error(), "max_active_runs") {
			t.Fatalf("failed to reject invalid binding: %+v", bad)
		}
	}
}
