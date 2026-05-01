package execute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/timing"
	"aonohako/internal/util"
	"aonohako/internal/workspacequota"
)

type execResult struct {
	Status          string
	ExitCode        *int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	MemoryKB        int64
	WallTimeMs      int64
	CPUTimeMs       int64
	Reason          string
	VerdictSource   string
}

func runCommandWithSandbox(parent context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	limits := req.Limits
	timeMs := max(1, limits.TimeMs)
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeMs)*time.Millisecond)
	defer cancel()

	return executeSandboxCommand(ctx, ws, command, req, hooks, outputLimitBytes, tuning, cgroupParentDir)
}

func executeSandboxCommand(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	if len(command) == 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox command is empty"}
	}
	tuning = tuning.WithSafeDefaults()
	if os.Geteuid() != 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox requires root"}
	}
	timeLimitMs := max(1, req.Limits.TimeMs)
	memoryLimitKB := int64(0)
	if req.Limits.MemoryMB > 0 {
		memoryLimitKB = int64(req.Limits.MemoryMB) * 1024
	}
	if cgroupParentDir != "" && memoryLimitKB <= 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "cgroup execution requires a positive memory limit"}
	}
	workspaceLimitBytes := req.Limits.WorkspaceBytes
	if workspaceLimitBytes <= 0 {
		workspaceLimitBytes = defaultWorkspaceBytes
	}
	if workspaceLimitBytes > hardMaxWorkspaceBytes {
		workspaceLimitBytes = hardMaxWorkspaceBytes
	}
	baseEnv := []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
	}
	innerEnv := append(append(baseEnv[:0:0], baseEnv...), security.ThreadLimitEnv()...)
	innerEnv = append(innerEnv, security.WorkspaceScopedEnv(ws.RootDir)...)
	if !req.EnableNetwork {
		innerEnv = append(innerEnv, "http_proxy=", "https_proxy=", "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*", "no_proxy=*")
	}

	runLang := profiles.NormalizeRunLang(req.Lang)
	allowUnixSockets := false
	switch runLang {
	case "ocaml":
		innerEnv = append(innerEnv, "OCAMLRUNPARAM="+ocamlRunParam)
	case "elixir":
		innerEnv = append(innerEnv, "ERL_AFLAGS="+erlangAFlags(tuning))
		allowUnixSockets = true
	case "erlang", "wasm":
		allowUnixSockets = true
	case "uhmlang":
		innerEnv = append(innerEnv, fmt.Sprintf("GOMEMLIMIT=%dMiB", goMemoryLimitMB(req.Limits.MemoryMB, tuning)), fmt.Sprintf("GOGC=%d", tuning.GoGOGC))
	}

	finalCommand := append([]string(nil), command...)
	if !filepath.IsAbs(finalCommand[0]) {
		path, err := util.ResolveCommandPath(finalCommand[0], innerEnv)
		if err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: "resolve command failed: " + err.Error()}
		}
		finalCommand[0] = path
	}
	if filepath.Base(finalCommand[0]) == "env" {
		for i := 1; i < len(finalCommand); i++ {
			if strings.Contains(finalCommand[i], "=") {
				continue
			}
			if filepath.IsAbs(finalCommand[i]) {
				break
			}
			path, err := util.ResolveCommandPath(finalCommand[i], innerEnv)
			if err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "resolve env command failed: " + err.Error()}
			}
			finalCommand[i] = path
			break
		}
	}
	runtimeBase := sandboxCommandBase(finalCommand)
	isDotnet := runtimeBase == "dotnet"
	allowMemfdCreate := isDotnet || runtimeBase == "wasmtime"
	if isDotnet {
		if heapLimit := dotnetGCHeapHardLimitHex(req.Limits.MemoryMB, tuning); heapLimit != "" {
			innerEnv = append(innerEnv, "DOTNET_GCHeapHardLimit="+heapLimit)
		}
	}
	if isDotnet {
		if err := security.ResetDotnetSharedState(); err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: "dotnet state cleanup failed: " + err.Error()}
		}
	}
	// CoreCLR reserves a very large memfd-backed double-mapped region during
	// startup, so finite RLIMIT_AS values can fail before user code.
	disableAddressSpaceLimit := isDotnet
	addressSpaceLimit := addressSpaceLimitBytes(runtimeBase, req.Limits.MemoryMB)
	addressSpaceLimitKB := int64(addressSpaceLimit / 1024)
	openFileLimit := security.OpenFileLimitForCommand(runtimeBase)

	if os.Geteuid() == 0 {
		const sandboxUID = 65532
		const sandboxGID = 65532
		if err := os.Chmod(ws.RootDir, 0o755); err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: "workspace chmod failed: " + err.Error()}
		}
		for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
			if err := os.Chown(dir, sandboxUID, sandboxGID); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "workspace chown failed: " + err.Error()}
			}
		}
		if err := os.Chmod(ws.BoxDir, 0o777|os.ModeSticky); err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: "workspace chmod failed: " + err.Error()}
		}
		for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
			if err := os.Chmod(dir, 0o700); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "workspace chmod failed: " + err.Error()}
			}
		}
	}

	helperReq := sandbox.ExecRequest{
		Command:                  finalCommand,
		Dir:                      ws.BoxDir,
		Env:                      innerEnv,
		Limits:                   req.Limits,
		ThreadLimit:              sandboxThreadLimit,
		OpenFileLimit:            openFileLimit,
		AddressSpaceLimitBytes:   addressSpaceLimit,
		FileSizeLimitBytes:       security.FileSizeLimitForCommand(runtimeBase, workspaceLimitBytes),
		EnableNetwork:            req.EnableNetwork,
		AllowUnixSockets:         allowUnixSockets,
		AllowUnixSocketMessages:  allowUnixSockets,
		AllowMemfdCreate:         allowMemfdCreate,
		DisableAddressSpaceLimit: disableAddressSpaceLimit,
		DisableFileSizeLimit:     isDotnet,
	}
	rawReq, err := json.Marshal(helperReq)
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request failed: " + err.Error()}
	}

	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request pipe failed: " + err.Error()}
	}
	defer requestRead.Close()
	defer requestWrite.Close()

	helperPath, err := os.Executable()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "resolve helper failed: " + err.Error()}
	}

	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Dir = ws.BoxDir
	cmd.Stdin = strings.NewReader(req.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: 65532, Gid: 65532}
	}
	cmd.ExtraFiles = []*os.File{requestRead}
	cmd.Env = append(append(baseEnv[:0:0], baseEnv...), sandbox.HelperModeEnv+"="+sandbox.HelperModeExec, sandbox.RequestFDEnv+"=3")

	stdoutBuf := cappedBuffer{limit: outputLimitBytes}
	stderrBuf := cappedBuffer{limit: outputLimitBytes}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "stdout pipe failed: " + err.Error()}
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "stderr pipe failed: " + err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "start failed: " + err.Error()}
	}
	var runGroup cgroup.Group
	cgroupCPUBaselineMicros := int64(0)
	if cgroupParentDir != "" {
		if err := cgroup.EnableControllers(cgroupParentDir, []string{"cpu", "memory", "pids"}); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return execResult{Status: model.RunStatusInitFail, Reason: "cgroup controller setup failed: " + err.Error()}
		}
		pidsMax := sandboxThreadLimit + 16
		group, err := cgroup.CreateRunGroup(cgroupParentDir, cgroup.RunName("execute"), cgroup.Limits{
			MemoryMaxBytes:  memoryLimitKB * 1024,
			PidsMax:         pidsMax,
			CPUQuotaMicros:  cgroup.SingleCPUQuotaMicros,
			CPUPeriodMicros: cgroup.DefaultCPUPeriodMicros,
		})
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return execResult{Status: model.RunStatusInitFail, Reason: "cgroup create failed: " + err.Error()}
		}
		runGroup = group
		if err := runGroup.AddProc(cmd.Process.Pid); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			_ = runGroup.Remove()
			return execResult{Status: model.RunStatusInitFail, Reason: "cgroup add process failed: " + err.Error()}
		}
		if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
			cgroupCPUBaselineMicros = stats.CPUUsageMicros
		}
		defer func() {
			_ = runGroup.Remove()
		}()
	}
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("1000\n"), 0o644)
	_ = requestRead.Close()
	if n, err := requestWrite.Write(rawReq); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request write failed: " + err.Error()}
	} else if n != len(rawReq) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request write failed: short write"}
	}
	if err := requestWrite.Close(); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request write failed: " + err.Error()}
	}

	imageDone := make(chan struct{})
	stopImageStream := func() {}
	if imgPath := firstImagePath(req.SidecarOutputs); imgPath != "" {
		imageCtx, cancelImage := context.WithCancel(ctx)
		stopImageStream = cancelImage
		go func() {
			streamImageEvents(imageCtx, ws, imgPath, hooks.OnImage)
			close(imageDone)
		}()
	} else {
		close(imageDone)
	}

	doneOut := make(chan struct{})
	doneErr := make(chan struct{})
	go func() {
		_, _ = ioCopy(&stdoutBuf, stdoutPipe)
		close(doneOut)
	}()
	go func() {
		_, _ = ioCopy(&stderrBuf, stderrPipe)
		close(doneErr)
	}()

	wallStart := timing.MonotonicNow()
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	resolvedHelperPath := helperPath
	if realHelperPath, err := filepath.EvalSymlinks(helperPath); err == nil && realHelperPath != "" {
		resolvedHelperPath = realHelperPath
	}
	cpuBaselineNs := uint64(0)
	targetStarted := false
	targetStartGraceDeadline := time.Now().Add(100 * time.Millisecond)
	watchdog := time.NewTicker(5 * time.Millisecond)
	defer watchdog.Stop()
	lastWorkspaceScan := time.Time{}
	maxCPUTimeMs := int64(0)
	maxRSSKB := int64(0)
	maxVmSizeKB := int64(0)
	var waitErr error

	result := execResult{Status: "OK"}
	for {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitCh
			if result.Status == "OK" {
				result.Status = model.RunStatusTLE
				result.Reason = "wall time limit exceeded"
				result.VerdictSource = "wall_time"
			}
			goto done
		case err := <-waitCh:
			waitErr = err
			goto done
		case <-watchdog.C:
			if !targetStarted {
				startTargetChecks := false
				exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid))
				if err != nil {
					startTargetChecks = !time.Now().Before(targetStartGraceDeadline)
				} else {
					if realExePath, err := filepath.EvalSymlinks(exePath); err == nil && realExePath != "" {
						exePath = realExePath
					}
					startTargetChecks = exePath != resolvedHelperPath || !time.Now().Before(targetStartGraceDeadline)
				}
				if startTargetChecks {
					// Some kernels/container settings hide /proc/<pid>/exe after
					// the helper sets PR_SET_DUMPABLE=0. CPU and workspace
					// enforcement start after a short grace period, but RSS is
					// sampled immediately below because the helper execs in-place.
					targetStarted = true
					cpuBaselineNs, _ = timing.ProcessCPUTimeNs(cmd.Process.Pid)
					lastWorkspaceScan = time.Time{}
				}
			}

			if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", cmd.Process.Pid)); err == nil {
				fields := strings.Fields(string(raw))
				if len(fields) >= 2 {
					pageKB := int64(os.Getpagesize() / 1024)
					if v, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
						v *= pageKB
						if v > maxVmSizeKB {
							maxVmSizeKB = v
						}
					}
					if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						v *= pageKB
						if v > maxRSSKB {
							maxRSSKB = v
						}
					}
				}
				if memoryLimitKB > 0 && (disableAddressSpaceLimit || maxRSSKB*10 >= memoryLimitKB*8) {
					if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", cmd.Process.Pid)); err == nil {
						scanner := bufio.NewScanner(bytes.NewReader(raw))
						for scanner.Scan() {
							fields := strings.Fields(scanner.Text())
							if len(fields) >= 2 && fields[0] == "Rss:" {
								if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil && v > maxRSSKB {
									maxRSSKB = v
								}
								break
							}
						}
					}
				}
				if result.Status == "OK" && memoryLimitKB > 0 && maxRSSKB > memoryLimitKB {
					result.Status = model.RunStatusMLE
					result.Reason = "memory limit exceeded"
					result.VerdictSource = "memory_rss"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}
			if runGroup.Path != "" {
				if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
					if stats.MemoryCurrentBytes > 0 && stats.MemoryCurrentBytes/1024 > maxRSSKB {
						maxRSSKB = stats.MemoryCurrentBytes / 1024
					}
					if stats.MemoryPeakBytes > 0 && stats.MemoryPeakBytes/1024 > maxRSSKB {
						maxRSSKB = stats.MemoryPeakBytes / 1024
					}
					if stats.CPUUsageMicros > 0 {
						cpuUsageMicros := stats.CPUUsageMicros
						if cgroupCPUBaselineMicros > 0 && cpuUsageMicros > cgroupCPUBaselineMicros {
							cpuUsageMicros -= cgroupCPUBaselineMicros
						}
						cpuTimeMs := cpuUsageMicros / 1000
						if cpuTimeMs > maxCPUTimeMs {
							maxCPUTimeMs = cpuTimeMs
						}
						if result.Status == "OK" && cpuTimeMs > int64(timeLimitMs) {
							result.Status = model.RunStatusTLE
							result.Reason = "cpu time limit exceeded"
							result.VerdictSource = "cpu_time_cgroup"
							_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
						}
					}
					if result.Status == "OK" && memoryLimitKB > 0 && stats.MemoryLimitBreached(memoryLimitKB*1024) {
						result.Status = model.RunStatusMLE
						result.Reason = "memory limit exceeded"
						result.VerdictSource = "memory_cgroup"
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
					if result.Status == "OK" && stats.PidsLimitBreached() {
						result.Status = model.RunStatusRE
						result.Reason = "process limit exceeded"
						result.VerdictSource = "pids_cgroup"
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
				}
			}

			if targetStarted {
				if cpuNs, err := timing.ProcessCPUTimeNs(cmd.Process.Pid); err == nil {
					cpuTimeMs := timing.MilliFromNanoseconds(cpuNs)
					if cpuBaselineNs > 0 && cpuNs > cpuBaselineNs {
						cpuTimeMs = timing.MilliFromNanoseconds(cpuNs - cpuBaselineNs)
					}
					if cpuTimeMs > maxCPUTimeMs {
						maxCPUTimeMs = cpuTimeMs
					}
					if result.Status == "OK" && cpuTimeMs > int64(timeLimitMs) {
						result.Status = model.RunStatusTLE
						result.Reason = "cpu time limit exceeded"
						result.VerdictSource = "cpu_time"
						_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					}
				}
			}

			if targetStarted && result.Status == "OK" && (lastWorkspaceScan.IsZero() || time.Since(lastWorkspaceScan) >= 25*time.Millisecond) {
				lastWorkspaceScan = time.Now()
				usage, err := workspacequota.Scan(ws.RootDir)
				if errors.Is(err, workspacequota.ErrEntryLimitExceeded) {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace entry limit exceeded"
					result.VerdictSource = "workspace_entries"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					continue
				}
				if errors.Is(err, workspacequota.ErrDepthExceeded) {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace depth exceeded"
					result.VerdictSource = "workspace_depth"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					continue
				}
				if err != nil {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace scan failed"
					result.VerdictSource = "workspace_scan"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
					continue
				}
				if usage.Bytes > workspaceLimitBytes {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace quota exceeded"
					result.VerdictSource = "workspace_bytes"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		}
	}
done:
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	stopImageStream()

	<-doneOut
	<-doneErr
	<-imageDone

	result.WallTimeMs = timing.SinceMillis(wallStart)
	result.CPUTimeMs = maxCPUTimeMs
	result.Stdout = stdoutBuf.Bytes()
	result.Stderr = stderrBuf.Bytes()
	result.StdoutTruncated = stdoutBuf.Truncated()
	result.StderrTruncated = stderrBuf.Truncated()
	result.MemoryKB = maxRSSKB

	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			if ws.Exited() {
				c := ws.ExitStatus()
				result.ExitCode = &c
			}
			if ws.Signaled() {
				if result.Status == "OK" {
					if ws.Signal() == syscall.SIGKILL || ws.Signal() == syscall.SIGXCPU {
						result.Status = model.RunStatusTLE
						if ctx.Err() != nil {
							result.VerdictSource = "wall_time"
						} else if ws.Signal() == syscall.SIGXCPU {
							result.VerdictSource = "cpu_rlimit"
						} else {
							result.VerdictSource = "signal"
						}
					} else {
						result.Status = model.RunStatusRE
						result.VerdictSource = "signal"
					}
				}
			}
		}
		if result.Status == "OK" && waitErr != nil {
			result.Status = model.RunStatusRE
			result.VerdictSource = "wait_status"
		}
		if sysu, ok := ps.SysUsage().(*syscall.Rusage); ok {
			if sysu.Maxrss > result.MemoryKB {
				result.MemoryKB = sysu.Maxrss
			}
		}
		if usageCPU := timing.MilliFromDuration(ps.UserTime() + ps.SystemTime()); usageCPU > result.CPUTimeMs {
			result.CPUTimeMs = usageCPU
		}
	}
	if addressSpaceProximityCanClassifyMLE(runtimeBase) && !disableAddressSpaceLimit && result.Status != model.RunStatusTLE && result.Status != model.RunStatusInitFail && memoryLimitKB > 0 && maxVmSizeKB > 0 && maxVmSizeKB+addressSpaceSlackKB >= addressSpaceLimitKB {
		result.Status = model.RunStatusMLE
		result.Reason = "memory limit exceeded"
		result.VerdictSource = "address_space"
	}
	if result.ExitCode != nil && *result.ExitCode == 120 && bytes.Contains(result.Stderr, []byte("sandbox-init:")) {
		result.Status = model.RunStatusInitFail
		result.Reason = clipUTF8(result.Stderr, outputLimitBytes)
		result.VerdictSource = "sandbox_init"
	}
	if result.Status == "OK" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = model.RunStatusTLE
		result.VerdictSource = "wall_time"
	}
	return result
}

func ioCopy(dst interface{ Write([]byte) (int, error) }, src any) (int64, error) {
	switch r := src.(type) {
	case *os.File:
		var n int64
		buf := make([]byte, 16*1024)
		for {
			k, err := r.Read(buf)
			if k > 0 {
				nn, _ := dst.Write(buf[:k])
				n += int64(nn)
			}
			if err != nil {
				if errors.Is(err, os.ErrClosed) || strings.Contains(err.Error(), "file already closed") {
					return n, nil
				}
				if err.Error() == "EOF" {
					return n, nil
				}
				return n, nil
			}
		}
	case interface{ Read([]byte) (int, error) }:
		var n int64
		buf := make([]byte, 16*1024)
		for {
			k, err := r.Read(buf)
			if k > 0 {
				nn, _ := dst.Write(buf[:k])
				n += int64(nn)
			}
			if err != nil {
				if errors.Is(err, os.ErrClosed) || strings.Contains(err.Error(), "file already closed") {
					return n, nil
				}
				if errors.Is(err, context.Canceled) {
					return n, nil
				}
				if err.Error() == "EOF" {
					return n, nil
				}
				return n, nil
			}
		}
	default:
		return 0, nil
	}
}

func outputLimitBytes(req *model.RunRequest) int {
	if req == nil || req.Limits.OutputBytes <= 0 {
		return defaultMaxOutputBytes
	}
	if req.Limits.OutputBytes > hardMaxOutputBytes {
		return hardMaxOutputBytes
	}
	return req.Limits.OutputBytes
}

func firstImagePath(paths []model.OutputFile) string {
	for _, p := range paths {
		if strings.Contains(strings.ToLower(p.Path), "image") || strings.Contains(strings.ToLower(p.Path), "img") {
			return p.Path
		}
	}
	return ""
}

func streamImageEvents(ctx context.Context, ws Workspace, relPath string, emit func(mime, b64 string, ts int64)) {
	if emit == nil {
		return
	}
	clean, err := util.ValidateRelativePath(relPath)
	if err != nil {
		return
	}
	ticker := time.NewTicker(80 * time.Millisecond)
	defer ticker.Stop()
	var offset int64
	var carry string
	var streamBytes int64

	readNew := func() {
		if streamBytes >= maxImageStreamBytes {
			return
		}
		output, err := openWorkspaceReadOnly(ws, clean)
		if err != nil {
			return
		}
		defer output.cleanup()
		if output.info.Size() <= offset {
			return
		}
		if _, err := output.file.Seek(offset, 0); err != nil {
			return
		}
		remaining := maxImageStreamBytes - streamBytes
		available := output.info.Size() - offset
		if available > remaining {
			available = remaining
		}
		if available > maxImageReadChunkBytes {
			available = maxImageReadChunkBytes
		}
		if available <= 0 {
			return
		}
		chunk := make([]byte, available)
		n, _ := output.file.Read(chunk)
		if n == 0 {
			return
		}
		chunk = chunk[:n]
		offset += int64(n)
		streamBytes += int64(n)
		text := carry + string(chunk)
		lines := strings.Split(text, "\n")
		if !strings.HasSuffix(text, "\n") {
			carry = lines[len(lines)-1]
			lines = lines[:len(lines)-1]
			if len(carry) > maxImageEventBytes {
				carry = ""
			}
		} else {
			carry = ""
		}
		emitted := 0
		for _, line := range lines {
			if emitted >= maxImageEventsPerRead {
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if len(line) > maxImageEventBytes {
				continue
			}
			var payload struct {
				Mime string `json:"mime"`
				B64  string `json:"b64"`
				TS   int64  `json:"ts"`
			}
			if err := json.Unmarshal([]byte(line), &payload); err != nil {
				continue
			}
			if payload.Mime == "" || payload.B64 == "" {
				continue
			}
			if len(payload.B64) > maxImageEventBytes {
				continue
			}
			ts := payload.TS
			if ts == 0 {
				ts = time.Now().UnixMilli()
			}
			emit(payload.Mime, payload.B64, ts)
			emitted++
		}
	}

	for {
		select {
		case <-ctx.Done():
			readNew()
			return
		case <-ticker.C:
			readNew()
		}
	}
}

func ioReadAll(r *bufio.Reader) ([]byte, error) {
	var out bytes.Buffer
	for {
		chunk, err := r.ReadBytes('\n')
		if len(chunk) > 0 {
			_, _ = out.Write(chunk)
		}
		if err != nil {
			if strings.Contains(err.Error(), "EOF") {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
	}
}
