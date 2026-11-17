package reference

import (
	"context"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/platform"
)

func collectionTestConfig() config.Config {
	return config.Config{
		MaxActiveRuns: 1, WorkRootMaxBytes: 128 << 20,
		InboundAuth: config.InboundAuthConfig{Mode: config.InboundAuthNone},
		Execution: config.ExecutionConfig{
			Platform: platform.RuntimeOptions{DeploymentTarget: platform.DeploymentTargetDev, ExecutionTransport: platform.ExecutionTransportEmbedded, SandboxBackend: platform.SandboxBackendHelper},
			WorkMeasurement: durante.Config{
				Families: []string{"cpp"}, Drrun: durante.DefaultDrrun, Client: durante.DefaultClient,
				DispatchProfile: durante.DispatchDefault, RuntimeImage: "sha256:" + strings.Repeat("a", 64),
				ResourcePolicy: &durante.ResourcePolicy{
					MaxLimits:      durante.ResourceLimits{CPUTimeMs: 10000, WallTimeMs: 30000, MemoryMB: 256},
					MemoryBudgetMB: 1024, ReservedMemoryMB: 128,
				},
			},
		},
	}
}

func collectionTestDataset() Dataset {
	return Dataset{
		Schema: Schema, CorpusID: CorpusID, Smoke: true, Repetitions: 1,
		SourceLanguage: "CPP20", DispatchProfile: durante.DispatchDefault,
		RuntimeImage: "sha256:" + strings.Repeat("a", 64), Environment: map[string]string{"cpu_model": "test CPU"},
		NativeLimits:  model.Limits{TimeMs: 10000, WallTimeMs: 30000, MemoryMB: 256},
		MeteredLimits: durante.ResourceLimits{CPUTimeMs: 10000, WallTimeMs: 30000, MemoryMB: 256},
	}
}

// A minimal ELF header is enough for the fake API. No test executes this data.
func collectionTestArtifact(dynamic bool) model.Artifact {
	data := make([]byte, 64)
	copy(data, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_X86_64))
	binary.LittleEndian.PutUint32(data[20:], uint32(elf.EV_CURRENT))
	binary.LittleEndian.PutUint16(data[52:], 64)
	if dynamic {
		binary.LittleEndian.PutUint64(data[32:], 64)
		binary.LittleEndian.PutUint16(data[54:], 56)
		binary.LittleEndian.PutUint16(data[56:], 1)
		data = append(data, make([]byte, 56)...)
		binary.LittleEndian.PutUint32(data[64:], uint32(elf.PT_INTERP))
	}
	return model.Artifact{Name: "Main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(data)}
}

func collectionTestNative() model.RunResponse {
	exit := 0
	return model.RunResponse{
		Status: model.RunStatusAccepted, ExitCode: &exit, VerdictSource: "stdout",
		CPUTimeMs: 3, CPUTimeNs: 2_500_001, TimeMs: 5, WallTimeMs: 5, MemoryKB: 32768,
		CPUAccounting: &model.CPUAccounting{SelectedSource: "wait_rusage", WaitTargetNs: 2_500_001},
	}
}

func collectionTestMetered(dataset Dataset) model.RunResponse {
	response := collectionTestNative()
	response.Status, response.VerdictSource = model.RunStatusInitFail, "durante_complete"
	match, verified := true, false
	instructions, zero := uint64(1000), uint64(0)
	response.WorkMeasurementExecution = &model.WorkMeasurementExecution{
		Authority: durante.Authority, State: "complete", OutputMatches: &match,
		NominalLimits: dataset.NativeLimits, RawLimits: dataset.MeteredLimits, TerminationSource: "stdout",
	}
	response.WorkMeasurement = &durante.Report{
		Schema: 1, ClientVersion: 1, Complete: true, Quality: "uninterrupted",
		Authority: durante.Authority, Metric: durante.Metric, Scope: durante.Scope,
		Family: "cpp", EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
		Instructions: &instructions, REPBytes: &zero, REPEntries: &zero, UnsupportedREPEntries: &zero,
		REPEnabled: true, REPTelemetryStatus: durante.REPMovsStosOnly, Processes: 1, Images: 1,
		IntegrityVerified: &verified, MeasurementContract: durante.DiagnosticContract(),
		Installation: &durante.InstallationIdentity{
			SourceCommit: "4016b33fc69b93d36a13efbd00953213b8f6317f", RuntimeSourceCommit: "6847bb210bea0ca11d1bf47704383d6a075da490",
			RuntimePatchSHA256:           "59fc79b5055a0c42b463703768891f0a10199ef1135d43028826c8c3894bab7c",
			RuntimeCodeReplayPatchSHA256: "aad9b561fb89e09d52d7a765124226659b8dbd8b886da61bc73d15fde28b5b72",
			LauncherSHA256:               strings.Repeat("a", 64), ClientSHA256: strings.Repeat("b", 64), CoreSHA256: strings.Repeat("c", 64), Unchanged: true,
		},
	}
	return response
}

func collectionTestWriteResult(t *testing.T, w http.ResponseWriter, result any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Error(err)
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = fmt.Fprintf(w, ": heartbeat\n\nevent: progress\ndata: {\"stage\":\"start\"}\n\nevent: result\ndata: %s\n\n", encoded)
}

func TestCollectionConfigurationRequiresExplicitComparableExecution(t *testing.T) {
	for _, options := range []CollectOptions{{Smoke: true, Repetitions: 1}, {Smoke: true, Repetitions: 9}, {Repetitions: 3}, {Repetitions: 9}} {
		if err := validateCollectionConfig(collectionTestConfig(), options, collectionTestDataset().MeteredLimits); err != nil {
			t.Fatalf("valid options %+v: %v", options, err)
		}
	}
	for name, edit := range map[string]func(*config.Config){
		"remote transport":          func(c *config.Config) { c.Execution.Platform.ExecutionTransport = platform.ExecutionTransportRemote },
		"no helper":                 func(c *config.Config) { c.Execution.Platform.SandboxBackend = platform.SandboxBackendNone },
		"parallel execution":        func(c *config.Config) { c.MaxActiveRuns = 2 },
		"normalization":             func(c *config.Config) { c.Execution.CPUNormalization.Enabled = true },
		"hidden repetitions":        func(c *config.Config) { c.Execution.CPUNormalization.ShortCaseSampling = true },
		"no runtime identity":       func(c *config.Config) { c.Execution.WorkMeasurement.RuntimeImage = "" },
		"mutable image tag":         func(c *config.Config) { c.Execution.WorkMeasurement.RuntimeImage = "runtime:latest" },
		"different dispatch":        func(c *config.Config) { c.Execution.WorkMeasurement.DispatchProfile = durante.DispatchGLIBC241 },
		"disabled cpp":              func(c *config.Config) { c.Execution.WorkMeasurement.Families = nil },
		"relative launcher":         func(c *config.Config) { c.Execution.WorkMeasurement.Drrun = "drrun" },
		"no resource policy":        func(c *config.Config) { c.Execution.WorkMeasurement.ResourcePolicy = nil },
		"insufficient CPU cap":      func(c *config.Config) { c.Execution.WorkMeasurement.ResourcePolicy.MaxLimits.CPUTimeMs = 9999 },
		"insufficient memory cap":   func(c *config.Config) { c.Execution.WorkMeasurement.ResourcePolicy.MaxLimits.MemoryMB = 128 },
		"insufficient total budget": func(c *config.Config) { c.Execution.WorkMeasurement.ResourcePolicy.MemoryBudgetMB = 256 },
		"authenticated API":         func(c *config.Config) { c.InboundAuth.Mode = config.InboundAuthBearer },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := collectionTestConfig()
			edit(&cfg)
			if err := validateCollectionConfig(cfg, CollectOptions{Repetitions: 3}, collectionTestDataset().MeteredLimits); err == nil {
				t.Fatal("incompatible collection configuration was accepted")
			}
		})
	}
	for _, options := range []CollectOptions{{Smoke: true}, {Smoke: true, Repetitions: 10}, {Repetitions: 1}, {Repetitions: 2}, {Repetitions: 10}} {
		if err := validateCollectionConfig(collectionTestConfig(), options, collectionTestDataset().MeteredLimits); err == nil {
			t.Fatalf("invalid repetitions accepted: %+v", options)
		}
	}
}

func TestCollectPreflightReturnsIncompleteDatasetWithoutExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dataset, err := Collect(ctx, collectionTestConfig(), CollectOptions{Smoke: true, Repetitions: 1})
	if !errors.Is(err, context.Canceled) || dataset.Complete || len(dataset.Failures) != 1 || len(dataset.Samples) != 0 || dataset.Schema != Schema || dataset.StartedAt == "" {
		t.Fatalf("cancelled collection lost failure metadata: dataset=%+v err=%v", dataset, err)
	}
	dataset, err = Collect(context.Background(), collectionTestConfig(), CollectOptions{Repetitions: 1})
	if err == nil || dataset.Complete || len(dataset.Failures) != 1 || dataset.RunnerSHA256 != "" {
		t.Fatalf("invalid options reached environment collection: dataset=%+v err=%v", dataset, err)
	}
}

func TestCollectCorpusUsesOneArtifactInOrderAndSavesIndependentSnapshots(t *testing.T) {
	dataset := collectionTestDataset()
	dataset.Repetitions = 2
	workload := Workload{ID: "unit", Split: SplitTrain, Source: "int main() { return 0; }", Cases: []Case{{Scale: 7, Input: "7\n", ExpectedStdout: "42\n"}}}
	artifact := collectionTestArtifact(false)
	var mu sync.Mutex
	var order []string
	var requests []model.RunRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/compile":
			var request model.CompileRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if request.Lang != "CPP20" || request.Target != "Main" || len(request.Sources) != 1 || request.Sources[0].Name != "Main.cpp" || request.Sources[0].DataB64 != base64.StdEncoding.EncodeToString([]byte(workload.Source)) {
				t.Errorf("compile request changed source or profile: %+v", request)
			}
			mu.Lock()
			order = append(order, "compile")
			mu.Unlock()
			collectionTestWriteResult(t, w, model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{artifact}})
		case "/execute":
			var request model.RunRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			requests = append(requests, request)
			order = append(order, fmt.Sprintf("metered=%t", request.WorkMeasurement))
			mu.Unlock()
			if request.WorkMeasurement {
				collectionTestWriteResult(t, w, collectionTestMetered(collectionTestDataset()))
			} else {
				collectionTestWriteResult(t, w, collectionTestNative())
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var saved []int
	options := CollectOptions{Smoke: true, Repetitions: 2, OnSample: func(snapshot Dataset) error {
		saved = append(saved, len(snapshot.Samples))
		snapshot.Environment["cpu_model"] = "changed"
		*snapshot.Samples[0].Metered.WorkMeasurement.Instructions = 0
		snapshot.Samples[0].NativeBefore.CPUAccounting.SelectedSource = "changed"
		return nil
	}}
	if err := collectCorpus(context.Background(), server.Client(), server.URL, []Workload{workload}, options, &dataset); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	wantOrder := []string{"compile", "metered=false", "metered=true", "metered=false", "metered=false", "metered=true", "metered=false"}
	if !reflect.DeepEqual(order, wantOrder) || !reflect.DeepEqual(saved, []int{1, 2}) || len(dataset.Samples) != 2 || len(dataset.Failures) != 0 {
		t.Fatalf("collection order or saved samples changed: order=%v saved=%v dataset=%+v", order, saved, dataset)
	}
	for i, request := range requests {
		if request.Lang != "CPP20" || request.WorkMeasurementTime || len(request.Binaries) != 1 || request.Binaries[0].DataB64 != artifact.DataB64 || request.Binaries[0].Name != "Main" || request.Binaries[0].Mode != "exec" || request.Stdin != "7\n" || request.ExpectedStdout != "42\n" || request.Limits != dataset.NativeLimits {
			t.Fatalf("request %d changed the immutable case: %+v", i, request)
		}
		if (request.WorkMeasurementLimits != nil) != (i%3 == 1) {
			t.Fatalf("request %d retained another phase's envelope: %+v", i, request)
		}
	}
	for i, sample := range dataset.Samples {
		if sample.Repetition != i+1 || sample.WorkloadID != workload.ID || sample.Scale != 7 || sample.SourceSHA256 != collectionSHA([]byte(workload.Source)) || sample.InputSHA256 != collectionSHA([]byte("7\n")) || sample.ExpectedSHA256 != collectionSHA([]byte("42\n")) || sample.BinarySHA256 != requests[0].Binaries[0].SHA256 {
			t.Fatalf("sample identity mismatch: %+v", sample)
		}
		if *sample.Metered.WorkMeasurement.Instructions != 1000 || sample.NativeBefore.CPUAccounting.SelectedSource != "wait_rusage" || dataset.Environment["cpu_model"] != "test CPU" {
			t.Fatal("save callback mutated the collection's nested state")
		}
	}
}

func TestCollectCorpusRetainsFailedTripletAndStops(t *testing.T) {
	for failedPhase, name := range []string{"native-before", "metered", "native-after"} {
		t.Run(name, func(t *testing.T) {
			dataset := collectionTestDataset()
			var executed atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/compile" {
					collectionTestWriteResult(t, w, model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(false)}})
					return
				}
				phase := int(executed.Add(1)) - 1
				response := collectionTestNative()
				if phase == 1 {
					response = collectionTestMetered(collectionTestDataset())
				}
				if phase == failedPhase {
					response.Status, response.Reason = model.RunStatusRE, "fixture execution failed"
				}
				collectionTestWriteResult(t, w, response)
			}))
			defer server.Close()
			callbacks := 0
			options := CollectOptions{Smoke: true, Repetitions: 2, OnSample: func(Dataset) error { callbacks++; return nil }}
			corpus := []Workload{{ID: "failed", Split: SplitTrain, Source: "int main(){}", Cases: []Case{{Scale: 1}}}}
			err := collectCorpus(context.Background(), server.Client(), server.URL, corpus, options, &dataset)
			if err == nil || !strings.Contains(err.Error(), name) || dataset.Complete || len(dataset.Failures) != 1 || len(dataset.Samples) != 1 || callbacks != 0 || executed.Load() != int32(failedPhase+1) {
				t.Fatalf("failure did not stop at its phase: dataset=%+v executions=%d callbacks=%d err=%v", dataset, executed.Load(), callbacks, err)
			}
			sample := dataset.Samples[0]
			responses := []model.RunResponse{sample.NativeBefore, sample.Metered, sample.NativeAfter}
			if responses[failedPhase].Status != model.RunStatusRE || responses[failedPhase].Reason != "fixture execution failed" || responses[failedPhase].CPUTimeNs != 2_500_001 {
				t.Fatal("failed phase lost its original response")
			}
			for i := failedPhase + 1; i < len(responses); i++ {
				if responses[i].Status != "" {
					t.Fatalf("phase %d ran after failure", i)
				}
			}
		})
	}
}

func TestCollectCorpusSaveFailureRetainsCompletedSample(t *testing.T) {
	dataset := collectionTestDataset()
	var executed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/compile" {
			collectionTestWriteResult(t, w, model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(false)}})
			return
		}
		phase := executed.Add(1)
		if phase%3 == 2 {
			collectionTestWriteResult(t, w, collectionTestMetered(collectionTestDataset()))
		} else {
			collectionTestWriteResult(t, w, collectionTestNative())
		}
	}))
	defer server.Close()
	saveErr := errors.New("fixture disk full")
	options := CollectOptions{Smoke: true, Repetitions: 2, OnSample: func(Dataset) error { return saveErr }}
	err := collectCorpus(context.Background(), server.Client(), server.URL, []Workload{{ID: "save", Source: "int main(){}", Cases: []Case{{Scale: 1}}}}, options, &dataset)
	if !errors.Is(err, saveErr) || len(dataset.Samples) != 1 || dataset.Samples[0].NativeAfter.Status != model.RunStatusAccepted || len(dataset.Failures) != 1 || dataset.Complete || executed.Load() != 3 {
		t.Fatalf("save failure lost the completed sample or continued: dataset=%+v executions=%d err=%v", dataset, executed.Load(), err)
	}
}

func TestPostCollectionSSEChecksProtocolAndBodyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, wantError string
		status                             int
	}{
		{"result", "text/event-stream; charset=utf-8", ": tick\n\nevent: result\ndata: {\"status\":\"Accepted\"}\n\n", "", 200},
		{"diagnostic error before result", "text/event-stream", "event: error\ndata: {\"message\":\"diagnostic complete\"}\n\nevent: result\ndata: {\"status\":\"Container Initialization Failed\"}\n\n", "", 200},
		{"wrong media type", "application/json", "{}", "text/event-stream", 200},
		{"HTTP error", "application/json", "request rejected", "HTTP 400", 400},
		{"missing result", "text/event-stream", "event: error\ndata: {\"message\":\"stopped\"}\n\n", "without a result: stopped", 200},
		{"malformed JSON", "text/event-stream", "event: result\ndata: {\n\n", "decode reference result", 200},
		{"null result", "text/event-stream", "event: result\ndata: null\n\n", "JSON object", 200},
		{"duplicate result", "text/event-stream", "event: result\ndata: {}\n\nevent: result\ndata: {}\n\n", "multiple results", 200},
		{"body budget", "text/event-stream", ": " + strings.Repeat("x", 512) + "\n\n", "exceeds 256 bytes", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			var result model.RunResponse
			err := postCollectionSSE(context.Background(), server.Client(), server.URL+"/execute", model.RunRequest{}, &result, 256, time.Second)
			if tc.wantError == "" {
				if err != nil || result.Status == "" {
					t.Fatalf("valid SSE result rejected: result=%+v err=%v", result, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("err=%v, want %q", err, tc.wantError)
			}
		})
	}
}

func TestPostCollectionSSEStopsOnCancellationAndDeadline(t *testing.T) {
	for _, explicitCancel := range []bool{true, false} {
		t.Run(fmt.Sprintf("explicit_cancel=%t", explicitCancel), func(t *testing.T) {
			started := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, ": waiting\n\n")
				w.(http.Flusher).Flush()
				close(started)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := 100 * time.Millisecond
			if explicitCancel {
				timeout = 5 * time.Second
			}
			done := make(chan error, 1)
			go func() {
				var result model.RunResponse
				done <- postCollectionSSE(ctx, server.Client(), server.URL+"/execute", model.RunRequest{}, &result, 256, timeout)
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("request never reached the test server")
			}
			want := context.DeadlineExceeded
			if explicitCancel {
				cancel()
				want = context.Canceled
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("err=%v, want %v", err, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocked SSE did not stop")
			}
		})
	}
}

func TestPostCollectionSSERejectsRedirectAndNonlocalEndpoints(t *testing.T) {
	var destinationCalled atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationCalled.Store(true)
		collectionTestWriteResult(t, w, model.RunResponse{Status: model.RunStatusAccepted})
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/execute", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	var result model.RunResponse
	err := postCollectionSSE(context.Background(), redirect.Client(), redirect.URL+"/execute", model.RunRequest{}, &result, 256, time.Second)
	if err == nil || destinationCalled.Load() {
		t.Fatalf("collector followed an API redirect: called=%t err=%v", destinationCalled.Load(), err)
	}
	for _, endpoint := range []string{"https://127.0.0.1/execute", "http://192.0.2.1/execute", destination.URL + "/other", destination.URL + "/execute?url=remote"} {
		if err := postCollectionSSE(context.Background(), destination.Client(), endpoint, model.RunRequest{}, &result, 256, time.Second); err == nil {
			t.Fatalf("unexpected endpoint admitted: %s", endpoint)
		}
	}
	if destinationCalled.Load() {
		t.Fatal("rejected endpoint reached HTTP")
	}
}

func TestCollectionRequiresOneStaticExecutableArtifact(t *testing.T) {
	good := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(false)}}
	if artifact, digest, err := collectionArtifact(good); err != nil || artifact.SHA256 != digest || len(digest) != 64 {
		t.Fatalf("valid artifact rejected: artifact=%+v digest=%q err=%v", artifact, digest, err)
	}
	for name, edit := range map[string]func(*model.CompileResponse){
		"failed compiler":     func(r *model.CompileResponse) { r.Status = model.CompileStatusCompileError },
		"multiple artifacts":  func(r *model.CompileResponse) { r.Artifacts = append(r.Artifacts, r.Artifacts[0]) },
		"unexpected name":     func(r *model.CompileResponse) { r.Artifacts[0].Name = "Other" },
		"nonexecutable":       func(r *model.CompileResponse) { r.Artifacts[0].Mode = "text" },
		"invalid encoding":    func(r *model.CompileResponse) { r.Artifacts[0].DataB64 = "not-base64" },
		"dynamic interpreter": func(r *model.CompileResponse) { r.Artifacts[0] = collectionTestArtifact(true) },
	} {
		t.Run(name, func(t *testing.T) {
			response := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(false)}}
			edit(&response)
			if _, _, err := collectionArtifact(response); err == nil {
				t.Fatal("incompatible artifact was accepted")
			}
		})
	}
}

func TestCollectionAcceptsZeroRSSWithValidFinalCPUAccounting(t *testing.T) {
	dataset := collectionTestDataset()
	for _, metered := range []bool{false, true} {
		t.Run(fmt.Sprintf("metered=%t", metered), func(t *testing.T) {
			response := collectionTestNative()
			if metered {
				response = collectionTestMetered(dataset)
			}
			response.MemoryKB = 0
			var err error
			if metered {
				err = validateCollectedMetered(response, dataset)
			} else {
				err = validateCollectedNative(response)
			}
			if err != nil {
				t.Fatalf("short execution with final CPU accounting and zero RSS was rejected: %v", err)
			}
			response.MemoryKB = -1
			if metered {
				err = validateCollectedMetered(response, dataset)
			} else {
				err = validateCollectedNative(response)
			}
			if err == nil {
				t.Fatal("negative RSS was accepted")
			}
		})
	}
}

func TestCollectionRejectsNormalizedOrIncompleteObservations(t *testing.T) {
	for name, edit := range map[string]func(*model.RunResponse){
		"normalized":            func(r *model.RunResponse) { r.CPUTimeNormalization = &model.CPUTimeNormalization{} },
		"raw alias":             func(r *model.RunResponse) { v := uint64(100); r.RawCPUTimeNs = &v },
		"sampled":               func(r *model.RunResponse) { r.CPUTimeSampling = &model.CPUTimeSampling{} },
		"no nanoseconds":        func(r *model.RunResponse) { r.CPUTimeNs = 0 },
		"no accounting":         func(r *model.RunResponse) { r.CPUAccounting = nil },
		"rounded unit mismatch": func(r *model.RunResponse) { r.CPUTimeMs = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			response := collectionTestNative()
			edit(&response)
			if err := validateCollectedNative(response); err == nil {
				t.Fatal("nonraw native observation was accepted")
			}
		})
	}
	for name, edit := range map[string]func(*model.RunResponse){
		"wrong output":     func(r *model.RunResponse) { *r.WorkMeasurementExecution.OutputMatches = false },
		"different limits": func(r *model.RunResponse) { r.WorkMeasurementExecution.RawLimits.MemoryMB++ },
		"missing report":   func(r *model.RunResponse) { r.WorkMeasurement = nil },
		"signal": func(r *model.RunResponse) {
			r.WorkMeasurement.SignalsObserved = 1
			r.WorkMeasurement.Quality = "signals_observed"
		},
		"unsupported REP": func(r *model.RunResponse) {
			v := uint64(1)
			r.WorkMeasurement.UnsupportedREPEntries = &v
			r.WorkMeasurement.REPTelemetryStatus = durante.REPPartial
		},
		"converted time": func(r *model.RunResponse) { r.WorkMeasurementExecution.TimeEstimate = &durante.TimeEstimate{} },
	} {
		t.Run(name, func(t *testing.T) {
			dataset := collectionTestDataset()
			response := collectionTestMetered(dataset)
			edit(&response)
			if err := validateCollectedMetered(response, dataset); err == nil {
				t.Fatal("incompatible metered observation was accepted")
			}
		})
	}
}
