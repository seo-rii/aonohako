package execute

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runvalidation"
	"aonohako/internal/util"
)

func evaluateRunStatus(ctx context.Context, ws Workspace, req *model.RunRequest, res execResult, judgeOut []byte, judgeSource, judgeInputPath string, spjSidecars []model.SidecarOutput, tuning config.RuntimeTuningConfig, cgroupParentDir string) (string, *float64, string, string) {
	status, reason, source := classifyRunStatusWithoutOutput(req, res)

	var score *float64
	outputOK := false
	evaluateOutputs := status == "OK" || (status == model.RunStatusTLE && req.IgnoreTLE)
	stdoutLimitExceeded := judgeSource == "stdout" && res.StdoutTruncated
	if evaluateOutputs {
		if stdoutLimitExceeded {
			if status == "OK" {
				reason = "stdout exceeded output limit"
				source = "stdout_limit"
			}
		} else if hasSPJ(req) {
			ok, sc, spjErr := runSPJ(ctx, ws, req, string(judgeOut), judgeInputPath, spjSidecars, tuning, cgroupParentDir)
			if sc != nil {
				score = sc
			}
			if spjErr != nil {
				if status == "OK" {
					status = model.RunStatusRE
					reason = spjErr.Error()
					source = "spj"
					if !strings.HasPrefix(reason, "spj ") {
						reason = "spj failed: " + reason
					}
				}
			} else {
				outputOK = ok
			}
		} else {
			outputOK = compareOutputs([]byte(req.ExpectedStdout), judgeOut)
		}
	}

	if status == "OK" && evaluateOutputs {
		if stdoutLimitExceeded {
			source = "stdout_limit"
		} else if hasSPJ(req) {
			source = "spj"
		} else if judgeSource != "" {
			source = judgeSource
		} else {
			source = "stdout"
		}
		if outputOK {
			status = model.RunStatusAccepted
		} else {
			status = model.RunStatusWA
		}
	}

	if status == model.RunStatusTLE && req.IgnoreTLE && score == nil {
		v := 0.0
		if outputOK {
			v = 1
		}
		score = &v
	}
	return status, score, reason, source
}

func classifyRunStatusWithoutOutput(req *model.RunRequest, res execResult) (string, string, string) {
	status := res.Status
	source := res.VerdictSource
	reason := ""
	status, reason, source = applyFinalCPUTimeStatus(status, reason, source, res.CPUTimeMs, req.Limits.TimeMs, strings.HasPrefix(source, "cpu_time_cgroup"))
	if status == "OK" && req.Limits.MemoryMB > 0 && res.MemoryKB > int64(req.Limits.MemoryMB*1024) {
		status = model.RunStatusMLE
		source = "memory_reported"
	}
	if status == "OK" && res.ExitCode != nil && *res.ExitCode != 0 {
		status = model.RunStatusRE
		source = "exit_code"
	}
	return status, reason, source
}

func applyFinalCPUTimeStatus(status, reason, source string, cpuTimeMs int64, limitMs int, cgroupBacked bool) (string, string, string) {
	if limitMs <= 0 || cpuTimeMs <= int64(limitMs) {
		return status, reason, source
	}
	if status != "OK" && status != model.RunStatusAccepted {
		return status, reason, source
	}
	source = "cpu_time_final"
	if cgroupBacked {
		source = "cpu_time_cgroup_final"
	}
	return model.RunStatusTLE, "cpu time limit exceeded", source
}

func captureFileOutput(ws Workspace, spec model.OutputFile) ([]byte, error) {
	output, err := openCapturedOutput(ws, spec.Path)
	if err != nil {
		return nil, err
	}
	defer output.cleanup()
	if output.info.Size() > maxCapturedFileBytes {
		return nil, fmt.Errorf("captured output too large")
	}
	if _, err := output.file.Seek(0, 0); err != nil {
		return nil, err
	}
	data, err := ioReadAll(bufio.NewReader(output.file))
	if err != nil {
		return nil, err
	}
	return data, nil
}

func captureSidecarOutputs(ws Workspace, specs []model.OutputFile) ([]model.SidecarOutput, []model.SidecarError) {
	outputs := make([]model.SidecarOutput, 0, len(specs))
	errs := make([]model.SidecarError, 0)
	var totalBytes int64
	for _, spec := range specs {
		output, err := openCapturedOutput(ws, spec.Path)
		if err != nil {
			errs = append(errs, model.SidecarError{Path: spec.Path, Reason: "capture failed"})
			continue
		}
		if output.info.Size() > maxCapturedFileBytes {
			output.cleanup()
			errs = append(errs, model.SidecarError{Path: spec.Path, Reason: "file too large"})
			continue
		}
		totalBytes += output.info.Size()
		if totalBytes > maxCapturedSidecarTotalBytes {
			output.cleanup()
			errs = append(errs, model.SidecarError{Path: spec.Path, Reason: "sidecar total size exceeded"})
			continue
		}
		if _, err := output.file.Seek(0, 0); err != nil {
			output.cleanup()
			errs = append(errs, model.SidecarError{Path: spec.Path, Reason: "read failed"})
			continue
		}
		data, err := ioReadAll(bufio.NewReader(output.file))
		output.cleanup()
		if err != nil {
			errs = append(errs, model.SidecarError{Path: spec.Path, Reason: "read failed"})
			continue
		}
		outputs = append(outputs, model.SidecarOutput{Path: spec.Path, DataB64: util.EncodeB64(data)})
	}
	return outputs, errs
}

func hasSPJ(req *model.RunRequest) bool {
	return req != nil && req.SPJ != nil
}

func runSPJ(ctx context.Context, ws Workspace, req *model.RunRequest, userStdout, judgeInputPath string, sidecars []model.SidecarOutput, tuning config.RuntimeTuningConfig, cgroupParentDir string) (bool, *float64, error) {
	if req == nil || req.SPJ == nil {
		return false, nil, fmt.Errorf("spj is required")
	}
	if err := runvalidation.ValidateSPJ(req.SPJ); err != nil {
		return false, nil, err
	}
	spjRoot := filepath.Join(ws.RootDir, ".spj")
	spjWS, err := prepareWorkspaceDirs(spjRoot)
	if err != nil {
		return false, nil, err
	}
	defer os.RemoveAll(spjRoot)

	spjPath := filepath.Join(spjWS.RootDir, "spj-runner")
	data, err := base64.StdEncoding.DecodeString(req.SPJ.Binary.DataB64)
	if err != nil {
		return false, nil, err
	}
	if len(data) > maxBinaryFileBytes {
		return false, nil, fmt.Errorf("spj binary too large")
	}
	if err := os.WriteFile(spjPath, data, 0o555); err != nil {
		return false, nil, err
	}
	defer os.Remove(spjPath)

	inputPath, err := writeStdinTempFile(ctx, filepath.Join(spjWS.RootDir, ".tmp"), "spj-input-*", req, judgeInputPath)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(inputPath)

	solutionPath, err := writeTempFile(filepath.Join(spjWS.RootDir, ".tmp"), "spj-solution-*", req.ExpectedStdout)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(solutionPath)

	outputPath, err := writeTempFile(filepath.Join(spjWS.RootDir, ".tmp"), "spj-output-*", userStdout)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(outputPath)

	if err := materializeSPJSidecars(spjWS.BoxDir, req.SPJ.SidecarOutputs, sidecars); err != nil {
		return false, nil, err
	}

	spjLang := profiles.NormalizeRunLang(req.SPJ.Lang)
	if spjLang == "" || spjLang == "binary" {
		spjLang = "binary"
	}
	spjLimits := model.Limits{
		TimeMs:         defaultSPJTimeMs,
		MemoryMB:       defaultSPJMemoryMB,
		OutputBytes:    req.Limits.OutputBytes,
		WorkspaceBytes: defaultWorkspaceBytes,
	}
	if req.SPJ.Limits != nil {
		spjLimits = *req.SPJ.Limits
		if spjLimits.TimeMs <= 0 {
			spjLimits.TimeMs = defaultSPJTimeMs
		}
		if spjLimits.MemoryMB <= 0 {
			spjLimits.MemoryMB = defaultSPJMemoryMB
		}
		if spjLimits.WorkspaceBytes <= 0 {
			spjLimits.WorkspaceBytes = defaultWorkspaceBytes
		}
	}
	spjReq := &model.RunRequest{Lang: spjLang, Limits: spjLimits, EnableNetwork: false}
	args := buildCommandWithRuntimeTuning(spjPath, spjLang, spjReq, tuning)
	args = append(args, inputPath, outputPath, solutionPath)
	res := runCommandWithSandbox(ctx, spjWS, args, spjReq, nil, 0, Hooks{}, outputLimitBytes(spjReq), tuning, cgroupParentDir)
	if res.Status == model.RunStatusTLE || res.Status == model.RunStatusMLE || res.Status == model.RunStatusWLE || res.Status == model.RunStatusInitFail {
		return false, nil, fmt.Errorf("spj failed: %s", res.Status)
	}
	if res.ExitCode != nil && *res.ExitCode == 0 {
		if req.SPJ.EmitScore {
			if res.StdoutTruncated {
				return false, nil, fmt.Errorf("score output exceeded output limit")
			}
			raw := strings.TrimSpace(string(res.Stdout))
			scoreVal := 0.0
			if raw != "" {
				parsed, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return false, nil, err
				}
				if math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1 {
					return false, nil, fmt.Errorf("spj score out of range")
				}
				scoreVal = parsed
			}
			return true, &scoreVal, nil
		}
		return true, nil, nil
	}
	if req.SPJ.EmitScore {
		s := 0.0
		return false, &s, nil
	}
	return false, nil, nil
}

func materializeSPJSidecars(root string, specs []model.OutputFile, sidecars []model.SidecarOutput) error {
	if len(specs) == 0 && len(sidecars) == 0 {
		return nil
	}
	byPath := make(map[string]string, len(sidecars))
	for _, sidecar := range sidecars {
		clean, err := util.ValidateRelativePath(sidecar.Path)
		if err != nil {
			continue
		}
		byPath[filepath.ToSlash(clean)] = sidecar.DataB64
	}
	if len(specs) == 0 {
		specs = make([]model.OutputFile, 0, len(byPath))
		for clean := range byPath {
			specs = append(specs, model.OutputFile{Path: clean})
		}
	}
	for _, spec := range specs {
		clean, err := util.ValidateRelativePath(spec.Path)
		if err != nil {
			return err
		}
		raw, ok := byPath[filepath.ToSlash(clean)]
		if !ok {
			continue
		}
		data, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return err
		}
		dest := filepath.Join(root, "sidecar", clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, data, 0o444); err != nil {
			return err
		}
	}
	return nil
}

func writeStdinTempFile(ctx context.Context, dir, pattern string, req *model.RunRequest, preparedPath string) (string, error) {
	if strings.TrimSpace(preparedPath) != "" {
		prepared, err := os.Open(preparedPath)
		if err != nil {
			return "", err
		}
		defer prepared.Close()
		return writeTempFileFromReader(dir, pattern, prepared, stdinURLMaxBytes(req.Limits))
	}
	if strings.TrimSpace(req.StdinURL) == "" {
		return writeTempFile(dir, pattern, req.Stdin)
	}
	maxBytes := stdinURLMaxBytes(req.Limits)
	stdinURLReader, err := openStdinURL(ctx, req.StdinURL, maxBytes, nil)
	if err != nil {
		return "", err
	}
	defer stdinURLReader.Close()
	return writeTempFileFromReader(dir, pattern, stdinURLReader, maxBytes)
}

func writeTempFile(dir, pattern, content string) (string, error) {
	return writeTempFileFromReader(dir, pattern, strings.NewReader(content), int64(len(content)))
}

func writeTempFileFromReader(dir, pattern string, reader io.Reader, maxBytes int64) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	written, err := io.Copy(file, io.LimitReader(reader, maxBytes+1))
	if err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
	}
	if written > maxBytes {
		file.Close()
		os.Remove(file.Name())
		return "", fmt.Errorf("stdin too large")
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	if err := os.Chmod(file.Name(), 0o444); err != nil {
		os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func clipUTF8(b []byte, n int) string {
	if n <= 0 {
		return ""
	}
	if n > len(b) {
		n = len(b)
	}
	validEnd := 0
	for i := 0; i < n; {
		r, size := utf8.DecodeRune(b[i:n])
		if r == utf8.RuneError && size == 1 {
			break
		}
		i += size
		validEnd = i
	}
	return string(b[:validEnd])
}

func sandboxCommandBase(command []string, workspaceRoots ...string) string {
	if len(command) == 0 {
		return ""
	}
	executable := command[0]
	if filepath.Base(executable) == "env" {
		for _, arg := range command[1:] {
			if strings.Contains(arg, "=") {
				continue
			}
			executable = arg
			break
		}
	}
	if !filepath.IsAbs(executable) {
		return ""
	}
	cleanExecutable := filepath.Clean(executable)
	resolvedExecutable := cleanExecutable
	if resolved, err := filepath.EvalSymlinks(cleanExecutable); err == nil && resolved != "" {
		resolvedExecutable = filepath.Clean(resolved)
	}
	for _, workspaceRoot := range workspaceRoots {
		if strings.TrimSpace(workspaceRoot) == "" {
			continue
		}
		cleanRoot := filepath.Clean(workspaceRoot)
		for _, candidate := range []string{cleanExecutable, resolvedExecutable} {
			rel, err := filepath.Rel(cleanRoot, candidate)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				return ""
			}
		}
	}
	for _, trustedRoot := range []string{
		"/bin",
		"/opt",
		"/sbin",
		"/usr/bin",
		"/usr/lib/erlang",
		"/usr/local/bin",
		"/usr/local/cargo/bin",
		"/usr/local/go/bin",
		"/usr/local/sbin",
		"/usr/sbin",
	} {
		rel, err := filepath.Rel(trustedRoot, resolvedExecutable)
		if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
			return filepath.Base(cleanExecutable)
		}
	}
	return ""
}

func addressSpaceLimitBytes(commandBase string, memMB int) uint64 {
	memoryMB := max(64, memMB)
	limitMB := memoryMB + 64
	switch commandBase {
	case "python3", "pypy3":
		limitMB = max(1024, memoryMB*3+512)
	case "sbcl":
		limitMB = max(8192, memoryMB*6+2048)
	case "node", "umjunsik-lang-go":
		limitMB = max(1024, memoryMB*4+512)
	case "deno":
		limitMB = max(65536, memoryMB*4+1024)
	case "wasmtime":
		limitMB = max(1024, memoryMB*4+1024)
	case "erlexec", "erl", "beam.smp", "aonohako-gleam-run":
		limitMB = max(8192, memoryMB*6+2048)
	case "ghdl", "vvp":
		limitMB = max(2048, memoryMB*4+512)
	case "dotnet":
		limitMB = max(2048, memoryMB*6+2048)
	default:
		limitMB = max(512, limitMB)
	}
	return uint64(limitMB) * 1024 * 1024
}

func addressSpaceProximityCanClassifyMLE(commandBase string) bool {
	switch commandBase {
	case "aonohako-gleam-run", "beam.smp", "deno", "dotnet", "erl", "erlexec", "ghdl", "node", "pypy3", "python3", "sbcl", "umjunsik-lang-go", "vvp", "wasmtime":
		return false
	default:
		return true
	}
}
