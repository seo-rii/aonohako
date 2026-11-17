package reference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"aonohako/internal/api"
	"aonohako/internal/compilewire"
	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/profiles"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/remoteio"
	"aonohako/internal/runtimeidentity"
	"aonohako/internal/rustpolicy"
	"aonohako/internal/util"
)

const (
	collectionRequestTimeout   = 90 * time.Second
	collectionCompileBytes     = 16 << 20
	collectionRustCompileBytes = 24 << 20
	collectionExecuteBytes     = 2 << 20
	collectionArtifactBytes    = 8 << 20
	collectionCompilerFlags    = "-O2 -Wall -lm --static -pipe -DONLINE_JUDGE=1 -std=c++20"
)

type CollectOptions struct {
	Language string
	// Zero selects the documented corpus default: CPP20 256 MiB, runtime
	// corpus 512 MiB. The resolved value is recorded in both dataset limits.
	MemoryMB    int
	Smoke       bool
	Repetitions int
	OnSample    func(Dataset) error
}

// Collect runs the fixed corpus through a local instance of the normal HTTP
// API. Every sample is ordered native-before, metered, native-after, using one
// compiled artifact. Failed attempts remain in an incomplete returned dataset.
func Collect(ctx context.Context, cfg config.Config, options CollectOptions) (dataset Dataset, err error) {
	profile, profileErr := ResolveCorpusProfile(options.Language)
	memoryMB := options.MemoryMB
	if memoryMB == 0 {
		memoryMB = profile.DefaultMemoryMB
	}
	dataset = Dataset{
		Schema: Schema, CorpusID: profile.CorpusID, Smoke: options.Smoke,
		StartedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceLanguage: profile.SourceLanguage,
		RuntimeImage:    cfg.Execution.WorkMeasurement.RuntimeImage,
		DispatchProfile: cfg.Execution.WorkMeasurement.DispatchProfile,
		CPUIDProfile:    cfg.Execution.WorkMeasurement.CPUIDProfile,
		Repetitions:     options.Repetitions,
		NativeLimits:    model.Limits{TimeMs: 10000, WallTimeMs: 30000, MemoryMB: memoryMB},
		MeteredLimits:   durante.ResourceLimits{CPUTimeMs: 10000, WallTimeMs: 30000, MemoryMB: memoryMB},
	}
	dataset.RuntimeDeterminism = cfg.Execution.WorkMeasurement.RuntimeDeterminismFor(profiles.NormalizeRunLang(profile.SourceLanguage))
	defer func() { recordCollectionFailure(&dataset, err) }()
	if profileErr != nil {
		return dataset, profileErr
	}
	if err = ctx.Err(); err != nil {
		return dataset, err
	}
	if err = validateCollectionConfig(cfg, options, dataset.MeteredLimits); err != nil {
		return dataset, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		return dataset, fmt.Errorf("reference collection requires a root Linux amd64 process")
	}
	actualPlatform, platformErr := platform.CurrentRuntimeOptions()
	if platformErr != nil {
		return dataset, platformErr
	}
	if actualPlatform != cfg.Execution.Platform {
		return dataset, fmt.Errorf("reference execution configuration differs from the process environment")
	}
	dataset.RunnerSHA256, dataset.Environment, err = collectionEnvironment(ctx, profile)
	if err != nil {
		return dataset, fmt.Errorf("reference environment: %w", err)
	}
	server, err := api.New(cfg)
	if err != nil {
		return dataset, fmt.Errorf("initialize reference API: %w", err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer func() {
		server.BeginDrain()
		server.CancelWork()
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if cleanupErr := server.WaitForWork(cleanupCtx); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("reference API cleanup: %w", cleanupErr))
		}
		httpServer.CloseClientConnections()
		httpServer.Close()
	}()
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout:       30 * time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	corpus, err := CorpusForLanguage(profile.SourceLanguage, options.Smoke)
	if err != nil {
		return dataset, err
	}
	if err = collectCorpus(ctx, client, httpServer.URL, corpus, options, &dataset); err != nil {
		return dataset, err
	}
	if err = ctx.Err(); err != nil {
		return dataset, err
	}
	dataset.Complete = true
	return dataset, nil
}

func validateCollectionConfig(cfg config.Config, options CollectOptions, limits durante.ResourceLimits) error {
	profile, err := ResolveCorpusProfile(options.Language)
	if err != nil {
		return err
	}
	if options.MemoryMB < 0 || options.MemoryMB > 4096 || (options.MemoryMB != 0 && options.MemoryMB != limits.MemoryMB) {
		return fmt.Errorf("reference memory option must match the recorded raw limit within 1..4096 MiB")
	}
	minimum := 3
	if options.Smoke {
		minimum = 1
	}
	if options.Repetitions < minimum || options.Repetitions > 9 {
		return fmt.Errorf("reference collection requires %d..9 repetitions", minimum)
	}
	if cfg.Execution.Platform.ExecutionTransport != platform.ExecutionTransportEmbedded || cfg.Execution.Platform.SandboxBackend != platform.SandboxBackendHelper {
		return fmt.Errorf("reference collection requires local embedded/helper execution")
	}
	if cfg.MaxActiveRuns != 1 {
		return fmt.Errorf("reference collection requires max_active_runs=1")
	}
	if cfg.Execution.CPUNormalization.Enabled || cfg.Execution.CPUNormalization.ShortCaseSampling {
		return fmt.Errorf("reference collection requires CPU normalization and short-case sampling disabled")
	}
	if cfg.InboundAuth.Mode != "" && cfg.InboundAuth.Mode != config.InboundAuthNone {
		return fmt.Errorf("local reference API requires inbound_auth=none")
	}
	measurement := cfg.Execution.WorkMeasurement
	if !runtimeidentity.ValidImageDigest(measurement.RuntimeImage) {
		return fmt.Errorf("reference collection requires an explicit immutable runtime image digest")
	}
	if measurement.DispatchProfile != durante.DispatchDefault {
		return fmt.Errorf("reference corpus requires the default dispatch profile")
	}
	if _, err := measurement.Family(profile.SourceLanguage); err != nil {
		return err
	}
	if !filepath.IsAbs(measurement.Drrun) || !filepath.IsAbs(measurement.Client) {
		return fmt.Errorf("reference collection requires absolute Durante launcher and client paths")
	}
	if err := measurement.ResourcePolicy.ValidateAdmission(cfg.MaxActiveRuns, cfg.WorkRootMaxBytes); err != nil {
		return fmt.Errorf("reference resource admission: %w", err)
	}
	if err := measurement.ResourcePolicy.ValidateRequest(limits); err != nil {
		return fmt.Errorf("reference raw limits: %w", err)
	}
	return measurement.ValidateTimeConfiguration()
}

func collectCorpus(ctx context.Context, client *http.Client, baseURL string, corpus []Workload, options CollectOptions, dataset *Dataset) (err error) {
	defer func() { recordCollectionFailure(dataset, err) }()
	profile, err := referenceDatasetProfile(*dataset)
	if err != nil {
		return err
	}
	selected, err := ResolveCorpusProfile(options.Language)
	if err != nil || selected.SourceLanguage != profile.SourceLanguage {
		return fmt.Errorf("reference collection options do not match the dataset language")
	}
	if len(corpus) == 0 {
		return fmt.Errorf("reference corpus is empty")
	}
	emitLogs := false
	for _, workload := range corpus {
		if err := ctx.Err(); err != nil {
			return err
		}
		var compiled model.CompileResponse
		compileRequest := model.CompileRequest{
			Lang: profile.SourceLanguage, Target: "Main", EmitLogs: &emitLogs,
			Sources: []model.Source{{Name: profile.SourceName, DataB64: base64.StdEncoding.EncodeToString([]byte(workload.Source))}},
		}
		if profile.Family == "native" {
			compileRequest.RustCrateMode = rustpolicy.CrateModeStdlib
		}
		compileBytes := int64(collectionCompileBytes)
		if profile.SourceLanguage == "RUST2024" {
			// A normal rustc artifact can exceed the original 8 MiB C++
			// collector cap. Keep the API's 16 MiB single-file contract,
			// with room for its base64 encoding and response metadata.
			compileBytes = collectionRustCompileBytes
		}
		if err := postCollectionSSE(ctx, client, baseURL+"/compile", compileRequest, &compiled, compileBytes, collectionRequestTimeout); err != nil {
			return fmt.Errorf("%s compile: %w", workload.ID, err)
		}
		binary, binaryHash, err := collectionProfileArtifact(profile, compiled, workload.Source)
		if err != nil {
			return fmt.Errorf("%s compile: %w", workload.ID, err)
		}
		for _, input := range workload.Cases {
			for repetition := 1; repetition <= options.Repetitions; repetition++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				sample := Sample{
					WorkloadID: workload.ID, Split: workload.Split, Scale: input.Scale, Repetition: repetition,
					SourceSHA256: collectionSHA([]byte(workload.Source)), BinarySHA256: binaryHash,
					InputSHA256: collectionSHA([]byte(input.Input)), ExpectedSHA256: collectionSHA([]byte(input.ExpectedStdout)),
				}
				// Append before the first request so a failed or cancelled phase still
				// retains every response already received for this triplet.
				dataset.Samples = append(dataset.Samples, sample)
				current := &dataset.Samples[len(dataset.Samples)-1]
				request := model.RunRequest{
					Lang: profile.SourceLanguage, Binaries: []model.Binary{binary}, Stdin: input.Input,
					ExpectedStdout: input.ExpectedStdout, Limits: dataset.NativeLimits, EmitLogs: &emitLogs,
				}
				if profile.Family == "cpython" || profile.Family == "pypy" {
					request.PythonLibraryMode = pythonpolicy.LibraryModeStdlib
				}
				phases := []struct {
					name    string
					metered bool
					result  *model.RunResponse
				}{
					{"native-before", false, &current.NativeBefore},
					{"metered", true, &current.Metered},
					{"native-after", false, &current.NativeAfter},
				}
				for _, phase := range phases {
					runRequest := request
					if phase.metered {
						limits := dataset.MeteredLimits
						runRequest.WorkMeasurement = true
						runRequest.WorkMeasurementLimits = &limits
					}
					err := postCollectionSSE(ctx, client, baseURL+"/execute", runRequest, phase.result, collectionExecuteBytes, collectionRequestTimeout)
					if err == nil {
						if phase.metered {
							err = validateCollectedMetered(*phase.result, *dataset)
						} else {
							err = validateCollectedNative(*phase.result)
						}
					}
					if err != nil {
						return fmt.Errorf("%s scale=%d repetition=%d %s: %w", workload.ID, input.Scale, repetition, phase.name, err)
					}
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if options.OnSample != nil {
					// Give the callback an independent snapshot, including nested
					// reports and maps, rather than writable collection state.
					encoded, err := json.Marshal(dataset)
					if err != nil {
						return fmt.Errorf("encode reference snapshot: %w", err)
					}
					var snapshot Dataset
					if err := json.Unmarshal(encoded, &snapshot); err != nil {
						return fmt.Errorf("decode reference snapshot: %w", err)
					}
					if err := options.OnSample(snapshot); err != nil {
						return fmt.Errorf("save reference sample: %w", err)
					}
				}
			}
		}
	}
	return nil
}

func collectionArtifact(response model.CompileResponse) (model.Binary, string, error) {
	profile, _ := ResolveCorpusProfile("CPP20")
	return collectionProfileArtifact(profile, response, "")
}

func collectionProfileArtifact(profile CorpusProfile, response model.CompileResponse, source string) (model.Binary, string, error) {
	if response.Status != model.CompileStatusOK {
		return model.Binary{}, "", fmt.Errorf("status=%s reason=%s stderr=%s", response.Status, collectionExcerpt(response.Reason), collectionExcerpt(response.Stderr))
	}
	var artifact model.Artifact
	if profile.Family == "cpython" || profile.Family == "pypy" {
		if len(response.Artifacts) != 2 {
			return model.Binary{}, "", fmt.Errorf("expected source and compiled bytecode artifacts")
		}
		seen := map[string]bool{}
		for _, candidate := range response.Artifacts {
			if seen[candidate.Name] || candidate.Mode != "" {
				return model.Binary{}, "", fmt.Errorf("invalid Python artifact name or mode")
			}
			seen[candidate.Name] = true
			switch candidate.Name {
			case profile.SourceName:
				if candidate.DataB64 != base64.StdEncoding.EncodeToString([]byte(source)) {
					return model.Binary{}, "", fmt.Errorf("compiler changed the reference Python source")
				}
			case profile.ArtifactName:
				artifact = candidate
			default:
				return model.Binary{}, "", fmt.Errorf("unexpected Python artifact")
			}
		}
	} else {
		if len(response.Artifacts) != 1 || response.Artifacts[0].Name != profile.ArtifactName || response.Artifacts[0].Mode != "exec" {
			return model.Binary{}, "", fmt.Errorf("expected one executable Main artifact")
		}
		artifact = response.Artifacts[0]
	}
	maxArtifactBytes := collectionArtifactBytes
	if profile.SourceLanguage == "RUST2024" {
		maxArtifactBytes = compilewire.ArtifactBytes
	}
	if len(artifact.DataB64) > base64.StdEncoding.EncodedLen(maxArtifactBytes) {
		return model.Binary{}, "", fmt.Errorf("reference artifact exceeds %d bytes", maxArtifactBytes)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(artifact.DataB64)
	if err != nil || len(data) == 0 || base64.StdEncoding.EncodeToString(data) != artifact.DataB64 {
		return model.Binary{}, "", fmt.Errorf("reference artifact is not canonical nonempty base64")
	}
	if len(data) > maxArtifactBytes {
		return model.Binary{}, "", fmt.Errorf("reference artifact exceeds %d bytes", maxArtifactBytes)
	}
	if profile.Family == "cpython" || profile.Family == "pypy" {
		// compileall is explicitly asked for a checked-hash PEP 552 header.
		// The interpreter, whose version is recorded separately, validates its
		// own magic/code format on execution. No source fallback is supplied.
		if len(data) <= 16 || data[2] != '\r' || data[3] != '\n' || binary.LittleEndian.Uint32(data[4:8]) != 3 {
			return model.Binary{}, "", fmt.Errorf("reference bytecode lacks a checked-hash header")
		}
		digest := collectionSHA(data)
		return model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode, SHA256: digest}, digest, nil
	}
	file, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return model.Binary{}, "", fmt.Errorf("reference artifact is not ELF: %w", err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return model.Binary{}, "", fmt.Errorf("reference artifact is not an amd64 executable")
	}
	for _, segment := range file.Progs {
		if segment.Type == elf.PT_INTERP && profile.SourceLanguage == "CPP20" {
			return model.Binary{}, "", fmt.Errorf("reference artifact unexpectedly requires a dynamic interpreter")
		}
	}
	digest := collectionSHA(data)
	return model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode, SHA256: digest}, digest, nil
}

func validateCollectedNative(response model.RunResponse) error {
	if response.Status != model.RunStatusAccepted || response.ExitCode == nil || *response.ExitCode != 0 || response.VerdictSource != "stdout" {
		return fmt.Errorf("native execution did not accept: status=%s reason=%s", response.Status, collectionExcerpt(response.Reason))
	}
	if err := validateCollectedRawCPU(response); err != nil {
		return err
	}
	if response.Score != nil || response.WorkMeasurement != nil || response.WorkMeasurementExecution != nil || len(response.WorkMeasurements) != 0 || len(response.Steps) != 0 || response.Match != nil {
		return fmt.Errorf("native execution returned unexpected scoring or measurement metadata")
	}
	return nil
}

func validateCollectedRawCPU(response model.RunResponse) error {
	if response.CPUTimeNormalization != nil || response.CPUTimeSampling != nil || response.RawCPUTimeNs != nil || response.RawCPUTimeMs != nil {
		return fmt.Errorf("reference CPU observation was normalized or sampled")
	}
	// Short executions may finish between RSS polls. A zero RSS observation
	// does not invalidate their final CPU accounting or imply zero memory use.
	if response.CPUTimeNs == 0 || response.CPUTimeMs <= 0 || response.MemoryKB < 0 || response.WallTimeMs < 0 || response.TimeMs < 0 || response.ProcessCPUTimeMs < 0 || response.CPUAccounting == nil {
		return fmt.Errorf("reference CPU or resource accounting is missing")
	}
	if response.CPUTimeNs > 10_000_000_000 || response.CPUTimeMs != int64((response.CPUTimeNs+999_999)/1_000_000) {
		return fmt.Errorf("reference CPU observation is outside the raw limits or has inconsistent units")
	}
	switch response.CPUAccounting.SelectedSource {
	case "process_clock", "wait_rusage", "cgroup":
	default:
		return fmt.Errorf("reference CPU accounting has an unknown source")
	}
	if response.StdoutTruncated || response.StderrTruncated {
		return fmt.Errorf("reference execution truncated its output")
	}
	return nil
}

func validateCollectedMetered(response model.RunResponse, dataset Dataset) error {
	profile, err := referenceDatasetProfile(dataset)
	if err != nil {
		return err
	}
	if err := validateCollectedRawCPU(response); err != nil {
		return err
	}
	d := response.WorkMeasurementExecution
	if response.Status != model.RunStatusInitFail || response.Score != nil || response.ExitCode == nil || *response.ExitCode != 0 || response.VerdictSource != "durante_complete" || d == nil {
		return fmt.Errorf("metered execution did not complete: status=%s reason=%s", response.Status, collectionExcerpt(response.Reason))
	}
	if d.State != "complete" || d.Authority != durante.Authority || d.OutputMatches == nil || !*d.OutputMatches || d.TerminationSource != "stdout" || d.RawLimits != dataset.MeteredLimits || d.NominalLimits != dataset.NativeLimits {
		return fmt.Errorf("metered execution returned inconsistent completion or limits")
	}
	if d.TimeEstimate != nil || d.NominalTimeExceeded != nil || d.TimeEstimateUnavailable != "" || d.TimeEstimateEligibility != nil || len(response.WorkMeasurements) != 0 || len(response.Steps) != 0 || response.Match != nil {
		return fmt.Errorf("raw corpus collection returned unsolicited converted time or peer metadata")
	}
	r := response.WorkMeasurement
	if r == nil || r.Family != profile.Family || r.EngineProfile != durante.EngineProfile || r.DispatchProfile != dataset.DispatchProfile || r.CPUIDProfile != dataset.CPUIDProfile ||
		r.RuntimeDeterminism != dataset.RuntimeDeterminism {
		return fmt.Errorf("metered observation is absent, incomplete, or incompatible")
	}
	if !r.ValidInstallationIdentity() {
		return fmt.Errorf("metered observation lacks a compatible installation identity")
	}
	if eligibility := r.TimeConversionEligibility(durante.TimeMethod); !eligibility.Eligible {
		return fmt.Errorf("metered observation is not convertible: %s", strings.Join(eligibility.ReasonCodes, ", "))
	}
	return nil
}

func postCollectionSSE(ctx context.Context, client *http.Client, endpoint string, payload, result any, maxBytes int64, timeout time.Duration) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !net.ParseIP(parsed.Hostname()).IsLoopback() || (parsed.Path != "/compile" && parsed.Path != "/execute") {
		return fmt.Errorf("reference requests must use the local loopback compile/execute API")
	}
	if maxBytes <= 0 || maxBytes > collectionRustCompileBytes || timeout <= 0 {
		return fmt.Errorf("invalid reference HTTP budget")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	requestBytes := collectionCompileBytes
	if request, ok := payload.(model.RunRequest); ok && request.Lang == "RUST2024" {
		requestBytes = collectionRustCompileBytes
	}
	if len(body) > requestBytes {
		return fmt.Errorf("reference request exceeds body limit")
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	localClient := *client
	localClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return fmt.Errorf("reference API redirects are not allowed")
	}
	response, err := localClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 4096))
		if readErr != nil {
			return fmt.Errorf("HTTP %s: %w", response.Status, readErr)
		}
		return fmt.Errorf("HTTP %s: %s", response.Status, collectionExcerpt(string(data)))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return fmt.Errorf("reference API did not return text/event-stream")
	}
	limited := &io.LimitedReader{R: response.Body, N: maxBytes + 1}
	reader := remoteio.NewSSEReader(limited)
	receivedResult, lastError := false, ""
	for {
		event, err := reader.Next()
		if limited.N == 0 {
			return fmt.Errorf("reference SSE response exceeds %d bytes", maxBytes)
		}
		if errors.Is(err, io.EOF) {
			if !receivedResult {
				return fmt.Errorf("reference SSE ended without a result: %s", lastError)
			}
			return requestCtx.Err()
		}
		if err != nil {
			return fmt.Errorf("read reference SSE: %w", err)
		}
		switch event.Name {
		case "error":
			var message struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(event.Data), &message); err != nil {
				return fmt.Errorf("decode reference error: %w", err)
			}
			lastError = collectionExcerpt(message.Message)
		case "result":
			if receivedResult {
				return fmt.Errorf("reference SSE returned multiple results")
			}
			data := bytes.TrimSpace([]byte(event.Data))
			if len(data) == 0 || data[0] != '{' {
				return fmt.Errorf("reference result must be a JSON object")
			}
			if err := json.Unmarshal(data, result); err != nil {
				return fmt.Errorf("decode reference result: %w", err)
			}
			receivedResult = true
		}
	}
}

func collectionEnvironment(ctx context.Context, profile CorpusProfile) (string, map[string]string, error) {
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return "", nil, err
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, executable)
	closeErr := executable.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", nil, err
	}
	kernel, err := collectionReadFile("/proc/sys/kernel/osrelease", 4096)
	if err != nil {
		return "", nil, err
	}
	cpuInfo, err := collectionReadFile("/proc/cpuinfo", 4<<20)
	if err != nil {
		return "", nil, err
	}
	cpuModel := ""
	for _, line := range strings.Split(string(cpuInfo), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "model name" {
			cpuModel = strings.TrimSpace(value)
			break
		}
	}
	if cpuModel == "" || strings.TrimSpace(string(kernel)) == "" {
		return "", nil, fmt.Errorf("kernel or CPU model is unavailable")
	}
	compiler, err := util.ResolveCommandPath(profile.Compiler, util.BaseEnv())
	if err != nil {
		return "", nil, err
	}
	versionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(versionCtx, compiler, "--version")
	command.Env = util.BaseEnv()
	command.WaitDelay = time.Second
	var output collectionOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		return "", nil, fmt.Errorf("compiler version: %w", err)
	}
	version := strings.TrimSpace(output.String())
	if version == "" {
		return "", nil, fmt.Errorf("compiler version is empty")
	}
	environment := map[string]string{
		"os": runtime.GOOS, "arch": runtime.GOARCH, "kernel": strings.TrimSpace(string(kernel)),
		"cpu_model": cpuModel, "logical_cpus": strconv.Itoa(runtime.NumCPU()),
		"compiler_path": compiler, "compiler_version": version, "compiler_flags": profile.CompilerFlags,
		"source_language": profile.SourceLanguage, "runtime_family": profile.Family,
		"entry_artifact": profile.ArtifactName,
	}
	return hex.EncodeToString(hash.Sum(nil)), environment, nil
}

type collectionOutput struct{ buffer bytes.Buffer }

func (b *collectionOutput) String() string { return b.buffer.String() }

func (b *collectionOutput) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > 16<<10 {
		return 0, fmt.Errorf("compiler version output exceeds limit")
	}
	return b.buffer.Write(p)
}

func collectionReadFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("environment file %s exceeds limit", path)
	}
	return data, nil
}

func collectionSHA(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func collectionExcerpt(value string) string {
	if len(value) > 1024 {
		return value[:1024] + "..."
	}
	return value
}

func recordCollectionFailure(dataset *Dataset, err error) {
	if err == nil {
		return
	}
	dataset.Complete = false
	message := err.Error()
	if len(dataset.Failures) == 0 || dataset.Failures[len(dataset.Failures)-1] != message {
		dataset.Failures = append(dataset.Failures, message)
	}
}
