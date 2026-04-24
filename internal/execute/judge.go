package execute

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

func evaluateRunStatus(ctx context.Context, ws Workspace, req *model.RunRequest, res execResult, judgeOut []byte) (string, *float64, string) {
	status := res.Status
	if status == "OK" && req.Limits.MemoryMB > 0 && res.MemoryKB > int64(req.Limits.MemoryMB*1024) {
		status = model.RunStatusMLE
	}
	if status == "OK" && res.ExitCode != nil && *res.ExitCode != 0 {
		status = model.RunStatusRE
	}

	var score *float64
	reason := ""
	outputOK := false
	evaluateOutputs := status == "OK" || (status == model.RunStatusTLE && req.IgnoreTLE)
	if evaluateOutputs {
		if hasSPJ(req) {
			ok, sc, spjErr := runSPJ(ctx, ws, req, string(judgeOut))
			if sc != nil {
				score = sc
			}
			if spjErr != nil {
				if status == "OK" {
					status = model.RunStatusRE
					reason = spjErr.Error()
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
	return status, score, reason
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
	return req != nil && req.SPJ != nil && req.SPJ.Binary != nil && req.SPJ.Binary.Name != "" && req.SPJ.Binary.DataB64 != ""
}

func runSPJ(ctx context.Context, ws Workspace, req *model.RunRequest, userStdout string) (bool, *float64, error) {
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

	inputPath, err := writeTempFile(filepath.Join(spjWS.RootDir, ".tmp"), "spj-input-*", req.Stdin)
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
	args := buildCommand(spjPath, spjLang, spjReq)
	args = append(args, inputPath, solutionPath, outputPath)
	res := runCommandWithSandbox(ctx, spjWS, args, &model.RunRequest{Lang: spjLang, Limits: spjLimits, EnableNetwork: false, Stdin: userStdout}, Hooks{}, outputLimitBytes(spjReq))
	if res.Status == model.RunStatusTLE || res.Status == model.RunStatusMLE || res.Status == model.RunStatusWLE || res.Status == model.RunStatusInitFail {
		return false, nil, fmt.Errorf("spj failed: %s", res.Status)
	}
	if res.ExitCode != nil && *res.ExitCode == 0 {
		if req.SPJ.EmitScore {
			raw := strings.TrimSpace(string(res.Stdout))
			scoreVal := 0.0
			if raw != "" {
				parsed, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					return false, nil, err
				}
				if parsed < 0 || parsed > 1 {
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

func writeTempFile(dir, pattern, content string) (string, error) {
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(content); err != nil {
		file.Close()
		os.Remove(file.Name())
		return "", err
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

func validateCapturedOutput(ws Workspace, rel string) (string, os.FileInfo, error) {
	clean, err := util.ValidateRelativePath(rel)
	if err != nil {
		return "", nil, err
	}
	full, err := existingWorkspacePath(ws, clean)
	if err != nil {
		return "", nil, err
	}
	st, err := os.Lstat(full)
	if err != nil {
		return "", nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("symlink outputs are not allowed: %s", rel)
	}
	if !st.Mode().IsRegular() {
		return "", nil, fmt.Errorf("output is not a regular file: %s", rel)
	}
	return full, st, nil
}

func existingWorkspacePath(ws Workspace, rel string) (string, error) {
	for _, candidate := range workspacePathCandidates(ws, rel) {
		if _, err := os.Lstat(candidate); err == nil {
			return candidate, nil
		}
	}
	return "", os.ErrNotExist
}

func workspacePathCandidates(ws Workspace, rel string) []string {
	return []string{
		filepath.Join(ws.BoxDir, rel),
		filepath.Join(ws.RootDir, rel),
	}
}

func clipUTF8(b []byte, n int) string {
	if len(b) <= n {
		if utf8.Valid(b) {
			return string(b)
		}
		k := len(b)
		for k > 0 && !utf8.Valid(b[:k]) {
			k--
		}
		return string(b[:k])
	}
	k := n
	for k > 0 && !utf8.Valid(b[:k]) {
		k--
	}
	if k == 0 {
		return ""
	}
	return string(b[:k])
}

func sandboxCommandBase(command []string) string {
	if len(command) == 0 {
		return ""
	}
	base := filepath.Base(command[0])
	if base != "env" {
		return base
	}
	for _, arg := range command[1:] {
		if strings.Contains(arg, "=") {
			continue
		}
		return filepath.Base(arg)
	}
	return base
}

func addressSpaceLimitBytes(commandBase string, memMB int) uint64 {
	memoryMB := max(64, memMB)
	limitMB := memoryMB + 64
	switch commandBase {
	case "node", "umjunsik-lang-go":
		limitMB = max(1024, memoryMB*4+512)
	case "wasmtime":
		limitMB = max(1024, memoryMB*4+1024)
	case "dotnet":
		limitMB = max(2048, memoryMB*6+2048)
	default:
		limitMB = max(512, limitMB)
	}
	return uint64(limitMB) * 1024 * 1024
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
