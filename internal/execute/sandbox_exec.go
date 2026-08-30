package execute

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/runvalidation"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/timing"
	"aonohako/internal/util"
	"aonohako/internal/workspacequota"
)

type execResult struct {
	Status           string
	ExitCode         *int
	Stdout           []byte
	Stderr           []byte
	StdoutTruncated  bool
	StderrTruncated  bool
	MemoryKB         int64
	WallTimeMs       int64
	CPUTimeMs        int64
	ProcessCPUTimeMs int64
	Reason           string
	VerdictSource    string
}

func cpuTimeAfterBaseline(usageNs, baselineNs uint64, baselineSet bool) (int64, bool) {
	if !baselineSet || usageNs < baselineNs {
		return 0, false
	}
	return timing.MilliFromNanoseconds(usageNs - baselineNs), true
}

type sandboxStreamConfig struct {
	stdin                     io.Reader
	stdinMaxBytes             int64
	liveStdin                 bool
	stdout                    io.Writer
	onStdoutDone              func()
	stderr                    io.Writer
	onStderrDone              func()
	identity                  sandboxIdentity
	extraFiles                []*os.File
	closeExtraFilesAfterStart bool
	onTargetReady             func()
	onTargetStarted           func()
	targetRelease             <-chan struct{}
	communicationRestricted   bool
}

type sandboxIdentity struct {
	uid uint32
	gid uint32
}

func sandboxSupplementaryGroups(identity sandboxIdentity, runLang string, mode pythonpolicy.LibraryMode) []uint32 {
	groups := []uint32{identity.gid}
	normalizedLang := profiles.NormalizeRunLang(runLang)
	if (normalizedLang == "python" || normalizedLang == "pypy") &&
		pythonpolicy.EffectiveLibraryMode(mode) == pythonpolicy.LibraryModeInstalled {
		groups = append(groups, pythonpolicy.ExternalLibraryGID)
	}
	return groups
}

const (
	defaultSandboxUID          uint32 = 65532
	defaultSandboxGID          uint32 = 65532
	interactiveJudgeSandboxUID uint32 = security.CommunicationManagerUID
	interactiveJudgeSandboxGID uint32 = security.CommunicationManagerGID
)

type sandboxPreparedStdin struct {
	file *os.File
}

func (s *sandboxPreparedStdin) Read(p []byte) (int, error) {
	if s == nil || s.file == nil {
		return 0, io.EOF
	}
	return s.file.Read(p)
}

type teeCaptureWriter struct {
	capture *cappedBuffer
	forward io.Writer
}

func hardenWorkspaceForIdentity(ws Workspace, runtimeBase string, identity sandboxIdentity) error {
	if err := os.Chown(ws.RootDir, os.Geteuid(), int(identity.gid)); err != nil {
		return fmt.Errorf("workspace chown failed: %w", err)
	}
	if err := os.Chmod(ws.RootDir, 0o710); err != nil {
		return fmt.Errorf("workspace chmod failed: %w", err)
	}
	for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
		if err := os.Chown(dir, int(identity.uid), int(identity.gid)); err != nil {
			return fmt.Errorf("workspace chown failed: %w", err)
		}
	}
	if err := os.Chmod(ws.BoxDir, 0o777|os.ModeSticky); err != nil {
		return fmt.Errorf("workspace chmod failed: %w", err)
	}
	if runtimeBase == "aonohako-gleam-run" {
		if err := filepath.WalkDir(ws.BoxDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("workspace path contains a symlink: %s", path)
			}
			return os.Chown(path, int(identity.uid), int(identity.gid))
		}); err != nil {
			return fmt.Errorf("workspace chown failed: %w", err)
		}
	}
	for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("workspace chmod failed: %w", err)
		}
	}
	return nil
}

func (w teeCaptureWriter) Write(p []byte) (int, error) {
	if w.capture != nil {
		if _, err := w.capture.Write(p); err != nil {
			return 0, err
		}
	}
	if w.forward != nil {
		n, err := w.forward.Write(p)
		if err != nil {
			return n, err
		}
		if n != len(p) {
			return n, io.ErrShortWrite
		}
	}
	return len(p), nil
}

func runCommandWithSandbox(parent context.Context, ws Workspace, command []string, req *model.RunRequest, stdinReader io.Reader, stdinMaxBytes int64, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	limits := req.Limits
	timeMs := max(1, limits.TimeMs)
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeMs)*time.Millisecond)
	defer cancel()

	return executeSandboxCommandWithStdinLimit(ctx, ws, command, req, stdinReader, stdinMaxBytes, hooks, outputLimitBytes, tuning, cgroupParentDir)
}

func executeSandboxCommand(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, stdinReader io.Reader, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	return executeSandboxCommandWithStdinLimit(ctx, ws, command, req, stdinReader, 0, hooks, outputLimitBytes, tuning, cgroupParentDir)
}

func executeSandboxCommandWithStdinLimit(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, stdinReader io.Reader, stdinMaxBytes int64, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	return executeSandboxCommandWithStreams(ctx, ws, command, req, sandboxStreamConfig{stdin: stdinReader, stdinMaxBytes: stdinMaxBytes}, hooks, outputLimitBytes, tuning, cgroupParentDir)
}

func executeSandboxCommandWithStreams(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, streams sandboxStreamConfig, hooks Hooks, outputLimitBytes int, tuning config.RuntimeTuningConfig, cgroupParentDir string) execResult {
	if len(command) == 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox command is empty"}
	}
	tuning = tuning.WithSafeDefaults()
	if os.Geteuid() != 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox requires root"}
	}
	if streams.communicationRestricted {
		restricted := *req
		restricted.EnableNetwork = false
		req = &restricted
	}
	timeLimitMs := max(1, req.Limits.TimeMs)
	memoryLimitKB := int64(0)
	if req.Limits.MemoryMB > 0 {
		memoryLimitKB = int64(req.Limits.MemoryMB) * 1024
	}
	if cgroupParentDir != "" && memoryLimitKB <= 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "cgroup execution requires a positive memory limit"}
	}
	identity := streams.identity
	if identity.uid == 0 || identity.gid == 0 {
		identity = sandboxIdentity{uid: defaultSandboxUID, gid: defaultSandboxGID}
	}
	workspaceLimitBytes := req.Limits.WorkspaceBytes
	if workspaceLimitBytes <= 0 {
		workspaceLimitBytes = defaultWorkspaceBytes
	}
	if workspaceLimitBytes > hardMaxWorkspaceBytes {
		workspaceLimitBytes = hardMaxWorkspaceBytes
	}
	baseEnv := util.BaseEnv()
	innerEnv := append(append(baseEnv[:0:0], baseEnv...), security.ThreadLimitEnv()...)
	innerEnv = append(innerEnv, security.WorkspaceScopedEnv(ws.RootDir)...)
	if !req.EnableNetwork {
		innerEnv = append(innerEnv, "http_proxy=", "https_proxy=", "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY=*", "no_proxy=*")
	}
	imgPath := firstImagePath(req.SidecarOutputs)
	if imgPath != "" {
		innerEnv = append(innerEnv, "IMG_CAPTURE=1")
	}

	runLang := profiles.NormalizeRunLang(req.Lang)
	isC3 := runLang == "c3"
	isGoBinary := runLang == "go-binary"
	isMojoBinary := runLang == "mojo-binary"
	isPonyBinary := runLang == "pony-binary"
	isShellRunLang := runLang == "bash" || runLang == "posix-sh"
	allowUnixSockets := false
	switch runLang {
	case "bash":
		innerEnv = append(innerEnv, "BASH_ENV=/dev/null", "ENV=/dev/null")
	case "posix-sh":
		innerEnv = append(innerEnv, "ENV=/dev/null")
	case "powershell":
		for i := range innerEnv {
			if strings.HasPrefix(innerEnv[i], "HOME=") {
				innerEnv[i] = "HOME=/var/empty"
				break
			}
		}
		innerEnv = append(innerEnv,
			"XDG_CONFIG_HOME=/var/empty/.config",
			"XDG_DATA_HOME=/var/empty/.local/share",
			"PSModulePath=/opt/microsoft/powershell/7/Modules",
			"POWERSHELL_TELEMETRY_OPTOUT=1",
			"POWERSHELL_UPDATECHECK=Off",
			"TERM=dumb",
			"NO_COLOR=1",
		)
	case "ocaml":
		innerEnv = append(innerEnv, "OCAMLRUNPARAM="+ocamlRunParam)
	case "elixir":
		innerEnv = append(innerEnv, "ERL_AFLAGS="+erlangAFlags(tuning))
		allowUnixSockets = true
	case "erlang", "gleam", "wasm", "assemblyscript":
		allowUnixSockets = true
	case "uhmlang":
		innerEnv = append(innerEnv, fmt.Sprintf("GOMEMLIMIT=%dMiB", goMemoryLimitMB(req.Limits.MemoryMB, tuning)), fmt.Sprintf("GOGC=%d", tuning.GoGOGC))
	case "deno":
		allowUnixSockets = true
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
	runtimeBase := sandboxCommandBase(finalCommand, ws.RootDir)
	if streams.communicationRestricted {
		// Communication-v1 accepts native binaries only. Never let a submitted
		// basename select a managed-runtime exception or shared runtime state.
		runLang = "binary"
		isC3 = false
		isGoBinary = false
		isMojoBinary = false
		isPonyBinary = false
		isShellRunLang = false
		runtimeBase = communicationSandboxRuntimeBase
	}
	if isJVMRunLang(runLang) && !isTrustedJVMRuntime(runLang, runtimeBase) {
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "JVM runtime executable is outside trusted system roots",
		}
	}
	isDotnet := runtimeBase == "dotnet"
	isPowerShell := runLang == "powershell" && runtimeBase == "pwsh"
	isFactor := runLang == "factor" && runtimeBase == "factor"
	isTLA := runtimeBase == "aonohako-tla-run"
	allowMemfdCreate := isDotnet || isTLA || runtimeBase == "wasmtime"
	trustedShellRuntime := false
	switch os.Getenv("AONOHAKO_IMAGE_NAME") {
	case "type-x", "ci-bash", "ci-posix-sh":
		trustedShellRuntime = runLang == "bash" && runtimeBase == "bash" ||
			runLang == "posix-sh" && runtimeBase == "dash"
	}
	if isShellRunLang && !trustedShellRuntime {
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "shell runtime is outside the dedicated trusted image",
		}
	}
	threadLimit := sandboxThreadLimit
	if trustedShellRuntime {
		threadLimit = shellSandboxThreadLimit
	}
	allowProcesses := false
	switch runtimeBase {
	case "aonohako-duckdb-run", "aonohako-gdl-run", "aonohako-gleam-run", "aonohako-tla-run", "aonohako-vhdl-run", "aonohako-why3-prove", "ghdl", "vvp":
		allowProcesses = true
	case "bash", "dash":
		allowProcesses = trustedShellRuntime
	}
	if isDotnet || isPowerShell {
		if heapLimit := dotnetGCHeapHardLimitHex(req.Limits.MemoryMB, tuning); heapLimit != "" {
			innerEnv = append(innerEnv, "DOTNET_GCHeapHardLimit="+heapLimit)
		}
	}
	if streams.communicationRestricted {
		allowUnixSockets = false
		allowMemfdCreate = false
		allowProcesses = false
	}
	runtimeState, err := security.AcquireRuntimeState(ws.RootDir, runtimeBase, int(identity.uid), int(identity.gid))
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "runtime state preparation failed: " + err.Error()}
	}
	defer func() {
		if releaseErr := runtimeState.Release(); releaseErr != nil {
			slog.Error("execute runtime state cleanup failed", "command", runtimeBase, "err", releaseErr)
		}
	}()
	// CoreCLR reserves a very large memfd-backed double-mapped region during
	// startup, so finite RLIMIT_AS values can fail before user code. Go binaries
	// likewise make large, ASLR-sensitive virtual reservations before main.
	// Pony binaries reserve a large MAP_NORESERVE arena before user code, so they
	// need the same virtual-address treatment without weakening physical limits.
	// Physical memory remains bounded by cgroup memory.max and the RSS watchdog.
	// The shared dotnet host also needs a high finite RLIMIT_FSIZE floor to start reliably.
	disableAddressSpaceLimit := isDotnet || isPowerShell || isC3 || isGoBinary || isMojoBinary || isPonyBinary || isTrustedJVMRuntime(runLang, runtimeBase) || runtimeBase == "java" || runtimeBase == "aonohako-tla-run"
	addressSpaceLimit := addressSpaceLimitBytes(runtimeBase, req.Limits.MemoryMB)
	if streams.communicationRestricted {
		// Native C++ communication targets do not need the broad managed-runtime
		// virtual-address allowance. Keep burst allocation close to the declared
		// RSS limit while leaving room for the loader and shared libraries.
		addressSpaceLimit = uint64(max(64, req.Limits.MemoryMB+64)) * 1024 * 1024
	}
	addressSpaceLimitKB := int64(addressSpaceLimit / 1024)
	openFileLimit := security.OpenFileLimitForCommand(runtimeBase)
	// A communication manager may open every /proc/self/fd/<n> argument as its
	// own stream while the inherited descriptors remain open. Leave additional
	// room for test data, the result stream, and runtime-library files.
	if required := 6 + len(streams.extraFiles)*2 + 32; openFileLimit < required {
		openFileLimit = required
	}

	if os.Geteuid() == 0 {
		if err := hardenWorkspaceForIdentity(ws, runtimeBase, identity); err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}

	helperReq := sandbox.ExecRequest{
		Command:                  finalCommand,
		Dir:                      ws.BoxDir,
		Env:                      innerEnv,
		Limits:                   req.Limits,
		ThreadLimit:              threadLimit,
		OpenFileLimit:            openFileLimit,
		StackLimitBytes:          security.StackLimitForCommand(runtimeBase),
		AddressSpaceLimitBytes:   addressSpaceLimit,
		FileSizeLimitBytes:       security.FileSizeLimitForCommand(runtimeBase, workspaceLimitBytes),
		EnableNetwork:            req.EnableNetwork,
		AllowUnixSockets:         allowUnixSockets,
		AllowUnixSocketMessages:  false,
		AllowProcesses:           allowProcesses,
		DenyThreads:              streams.communicationRestricted,
		AllowThreadSignals:       isFactor,
		AllowMemfdCreate:         allowMemfdCreate,
		AllowNumaPolicy:          isDotnet || isTLA,
		AllowChmod:               !streams.communicationRestricted && (isTLA || runtimeBase == "aonohako-gleam-run"),
		DisableAddressSpaceLimit: disableAddressSpaceLimit,
		DisableFileSizeLimit:     false,
	}
	for i := range streams.extraFiles {
		helperReq.PreserveFDs = append(helperReq.PreserveFDs, 6+i)
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

	targetReadyRead, targetReadyWrite, err := os.Pipe()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox target ready pipe failed: " + err.Error()}
	}
	defer targetReadyRead.Close()
	defer targetReadyWrite.Close()
	targetReleaseRead, targetReleaseWrite, err := os.Pipe()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox target release pipe failed: " + err.Error()}
	}
	defer targetReleaseRead.Close()
	defer targetReleaseWrite.Close()

	helperPath, err := os.Executable()
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "resolve helper failed: " + err.Error()}
	}

	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Dir = ws.BoxDir
	var stdinLiveReader io.Reader
	var stdinLiveWrite *os.File
	var stdinCopyDone chan struct{}
	var stdinCopyErr chan error
	if streams.liveStdin {
		stdinLiveReader = streams.stdin
		if stdinLiveReader == nil {
			stdinLiveReader = strings.NewReader("")
		}
		stdinRead, stdinWrite, err := os.Pipe()
		if err != nil {
			return execResult{Status: model.RunStatusInitFail, Reason: "stdin pipe failed: " + err.Error()}
		}
		stdinLiveWrite = stdinWrite
		stdinCopyDone = make(chan struct{})
		stdinCopyErr = make(chan error, 1)
		defer func() {
			_ = stdinRead.Close()
			_ = stdinWrite.Close()
			if closer, ok := stdinLiveReader.(io.Closer); ok {
				_ = closer.Close()
			}
			select {
			case <-stdinCopyDone:
			case <-time.After(100 * time.Millisecond):
			}
		}()
		cmd.Stdin = stdinRead
	} else {
		stdinReader := streams.stdin
		if stdinReader == nil {
			stdinReader = strings.NewReader(req.Stdin)
		}
		if prepared, ok := stdinReader.(*sandboxPreparedStdin); ok && prepared.file != nil {
			if _, err := prepared.file.Seek(0, io.SeekStart); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			cmd.Stdin = prepared.file
		} else {
			stdinMaxBytes := streams.stdinMaxBytes
			if stdinMaxBytes <= 0 {
				stdinMaxBytes = runvalidation.MaxTextFieldBytes
			}
			stdinTemp, err := os.CreateTemp(filepath.Join(ws.RootDir, ".tmp"), "stdin-*")
			if err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			defer func() {
				_ = stdinTemp.Close()
				_ = os.Remove(stdinTemp.Name())
			}()
			written, err := io.Copy(stdinTemp, io.LimitReader(stdinReader, stdinMaxBytes+1))
			if err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			if written > stdinMaxBytes {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin too large"}
			}
			if err := stdinTemp.Chown(int(identity.uid), int(identity.gid)); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			if err := stdinTemp.Chmod(0o400); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			if _, err := stdinTemp.Seek(0, 0); err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "stdin materialization failed: " + err.Error()}
			}
			cmd.Stdin = stdinTemp
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:    identity.uid,
			Gid:    identity.gid,
			Groups: sandboxSupplementaryGroups(identity, runLang, req.PythonLibraryMode),
		}
	}
	cmd.ExtraFiles = append([]*os.File{requestRead, targetReadyWrite, targetReleaseRead}, streams.extraFiles...)
	cmd.Env = append(
		append(baseEnv[:0:0], baseEnv...),
		sandbox.HelperModeEnv+"="+sandbox.HelperModeExec,
		sandbox.RequestFDEnv+"=3",
		sandbox.TargetReadyFDEnv+"=4",
		sandbox.TargetReleaseFDEnv+"=5",
	)

	stdoutBuf := cappedBuffer{limit: outputLimitBytes}
	stderrBuf := cappedBuffer{limit: outputLimitBytes}
	stdoutWriter := interface{ Write([]byte) (int, error) }(&stdoutBuf)
	if streams.stdout != nil {
		stdoutWriter = teeCaptureWriter{capture: &stdoutBuf, forward: streams.stdout}
	}
	stderrWriter := interface{ Write([]byte) (int, error) }(&stderrBuf)
	if streams.stderr != nil {
		stderrWriter = teeCaptureWriter{capture: &stderrBuf, forward: streams.stderr}
	}
	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter
	sandbox.TrimParentMemoryBeforeSandbox()
	if err := cmd.Start(); err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "start failed: " + err.Error()}
	}
	if streams.closeExtraFilesAfterStart {
		for _, file := range streams.extraFiles {
			if file != nil {
				_ = file.Close()
			}
		}
	}
	_ = targetReadyWrite.Close()
	_ = targetReleaseRead.Close()
	if streams.liveStdin {
		_ = cmd.Stdin.(*os.File).Close()
		go func() {
			defer close(stdinCopyDone)
			_, copyErr := ioCopy(stdinLiveWrite, stdinLiveReader)
			if closeErr := stdinLiveWrite.Close(); copyErr == nil {
				copyErr = closeErr
			}
			stdinCopyErr <- copyErr
		}()
	}
	var runGroup cgroup.Group
	cgroupCPUBaselineMicros := int64(0)
	var cgroupLimitBaseline cgroup.Stats
	cgroupLimitBaselineSet := false
	cpuBaselineNs := uint64(0)
	cpuBaselineSet := false
	if cgroupParentDir != "" {
		if err := cgroup.EnableControllers(cgroupParentDir, []string{"cpu", "memory", "pids"}); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return execResult{Status: model.RunStatusInitFail, Reason: "cgroup controller setup failed: " + err.Error()}
		}
		pidsMax := threadLimit + 16
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
			cleanupSandboxCgroup("execute", runGroup)
			return execResult{Status: model.RunStatusInitFail, Reason: "cgroup add process failed: " + err.Error()}
		}
		if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
			cgroupCPUBaselineMicros = stats.CPUUsageMicros
			cgroupLimitBaseline = stats
			cgroupLimitBaselineSet = true
		}
		defer func() {
			cleanupSandboxCgroup("execute", runGroup)
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

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	readyCh := make(chan error, 1)
	go func() {
		var ready [1]byte
		_, err := io.ReadFull(targetReadyRead, ready[:])
		readyCh <- err
	}()
	select {
	case err := <-readyCh:
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitCh
			return execResult{
				Status: model.RunStatusInitFail,
				Reason: "sandbox target synchronization failed: " + err.Error(),
				Stderr: stderrBuf.Bytes(),
			}
		}
	case waitErr := <-waitCh:
		reason := "sandbox helper exited before target synchronization"
		if waitErr != nil {
			reason += ": " + waitErr.Error()
		}
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: reason,
			Stderr: stderrBuf.Bytes(),
		}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "sandbox target synchronization timed out",
			Stderr: stderrBuf.Bytes(),
		}
	}
	if baselineNs, err := timing.ProcessCPUTimeNs(cmd.Process.Pid); err == nil {
		cpuBaselineNs = baselineNs
		cpuBaselineSet = true
	} else if runGroup.Path == "" {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "sandbox CPU baseline capture failed: " + err.Error(),
			Stderr: stderrBuf.Bytes(),
		}
	}
	if runGroup.Path != "" {
		if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
			cgroupLimitBaseline = stats
			cgroupLimitBaselineSet = true
			cgroupCPUBaselineMicros = stats.CPUUsageMicros
		}
	}
	if streams.onTargetReady != nil {
		streams.onTargetReady()
	}
	if streams.targetRelease != nil {
		select {
		case <-streams.targetRelease:
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitCh
			return execResult{
				Status: model.RunStatusInitFail,
				Reason: "sandbox target release canceled",
				Stderr: stderrBuf.Bytes(),
			}
		}
	}
	targetExecCh := make(chan error, 1)
	go func() {
		var unexpected [1]byte
		n, err := targetReadyRead.Read(unexpected[:])
		switch {
		case n != 0:
			err = fmt.Errorf("unexpected target synchronization data")
		case errors.Is(err, io.EOF):
			err = nil
		case err == nil:
			err = io.ErrNoProgress
		}
		targetExecCh <- err
	}()
	wallStart := timing.MonotonicNow()
	if n, err := targetReleaseWrite.Write([]byte{1}); err != nil || n != 1 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		if err == nil {
			err = io.ErrShortWrite
		}
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "sandbox target release failed: " + err.Error(),
			Stderr: stderrBuf.Bytes(),
		}
	}
	_ = targetReleaseWrite.Close()
	select {
	case err := <-targetExecCh:
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitCh
			return execResult{
				Status: model.RunStatusInitFail,
				Reason: "sandbox target exec synchronization failed: " + err.Error(),
				Stderr: stderrBuf.Bytes(),
			}
		}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		return execResult{
			Status: model.RunStatusInitFail,
			Reason: "sandbox target exec synchronization timed out",
			Stderr: stderrBuf.Bytes(),
		}
	}
	_ = targetReadyRead.Close()
	targetStarted := true
	if streams.onTargetStarted != nil {
		streams.onTargetStarted()
	}

	imageDone := make(chan struct{})
	stopImageStream := func() {}
	if imgPath != "" {
		imageCtx, cancelImage := context.WithCancel(ctx)
		stopImageStream = cancelImage
		go func() {
			streamImageEvents(imageCtx, ws, imgPath, hooks.OnImage)
			close(imageDone)
		}()
	} else {
		close(imageDone)
	}

	watchdog := time.NewTicker(1 * time.Millisecond)
	defer watchdog.Stop()
	lastWorkspaceScan := time.Time{}
	maxCPUTimeMs := int64(0)
	maxRSSKB := int64(0)
	maxVmSizeKB := int64(0)
	var waitErr error

	result := execResult{Status: "OK"}
	parentKillReason := ""
	killTarget := func(reason string) {
		if reason != "" && parentKillReason == "" {
			parentKillReason = reason
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	for {
		select {
		case <-ctx.Done():
			killTarget("wall_time")
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
			if targetStarted {
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
						killTarget(result.VerdictSource)
					}
				}
			}
			if runGroup.Path != "" {
				if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
					if stats.CPUUsageMicros > 0 {
						cpuUsageMicros := stats.CPUUsageMicros
						if cgroupCPUBaselineMicros > 0 && cpuUsageMicros > cgroupCPUBaselineMicros {
							cpuUsageMicros -= cgroupCPUBaselineMicros
						}
						cpuTimeMs := cpuUsageMicros / 1000
						if cpuTimeMs > maxCPUTimeMs {
							maxCPUTimeMs = cpuTimeMs
						}
						if targetStarted && result.Status == "OK" && cpuTimeMs > int64(timeLimitMs) {
							result.Status = model.RunStatusTLE
							result.Reason = "cpu time limit exceeded"
							result.VerdictSource = "cpu_time_cgroup"
							killTarget(result.VerdictSource)
						}
					}
					if targetStarted && result.Status == "OK" {
						switch cgroupLimitBreachSince(stats, cgroupLimitBaseline, cgroupLimitBaselineSet) {
						case cgroup.LimitBreachMemory:
							result.Status = model.RunStatusMLE
							result.Reason = "memory limit exceeded"
							result.VerdictSource = "memory_cgroup"
							if memoryLimitKB > maxRSSKB {
								maxRSSKB = memoryLimitKB
							}
							killTarget(result.VerdictSource)
						case cgroup.LimitBreachPids:
							result.Status = model.RunStatusRE
							result.Reason = "process limit exceeded"
							result.VerdictSource = "pids_cgroup"
							killTarget(result.VerdictSource)
						}
					}
				}
			}

			if targetStarted {
				if cpuNs, err := timing.ProcessCPUTimeNs(cmd.Process.Pid); err == nil {
					cpuTimeMs := timing.MilliFromNanoseconds(cpuNs)
					cpuTimeAvailable := true
					if targetCPUTimeMs, ok := cpuTimeAfterBaseline(cpuNs, cpuBaselineNs, cpuBaselineSet); ok {
						cpuTimeMs = targetCPUTimeMs
					} else if cpuBaselineSet {
						cpuTimeAvailable = false
					}
					if cpuTimeAvailable && cpuTimeMs > maxCPUTimeMs {
						maxCPUTimeMs = cpuTimeMs
					}
					if cpuTimeAvailable && result.Status == "OK" && cpuTimeMs > int64(timeLimitMs) {
						result.Status = model.RunStatusTLE
						result.Reason = "cpu time limit exceeded"
						result.VerdictSource = "cpu_time"
						killTarget(result.VerdictSource)
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
					killTarget(result.VerdictSource)
					continue
				}
				if errors.Is(err, workspacequota.ErrDepthExceeded) {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace depth exceeded"
					result.VerdictSource = "workspace_depth"
					killTarget(result.VerdictSource)
					continue
				}
				if err != nil {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace scan failed"
					result.VerdictSource = "workspace_scan"
					killTarget(result.VerdictSource)
					continue
				}
				if usage.Bytes > workspaceLimitBytes {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace quota exceeded"
					result.VerdictSource = "workspace_bytes"
					killTarget(result.VerdictSource)
				}
			}
		}
	}
done:
	killTarget("")
	stopImageStream()
	var stdinRelayErr error
	if streams.liveStdin {
		if closer, ok := stdinLiveReader.(io.Closer); ok {
			if err := closer.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) {
				stdinRelayErr = err
			}
		}
		select {
		case <-stdinCopyDone:
			if err := <-stdinCopyErr; err != nil {
				stdinRelayErr = err
			}
		case <-time.After(100 * time.Millisecond):
			stdinRelayErr = fmt.Errorf("stdin relay did not stop")
		}
	}

	if streams.onStdoutDone != nil {
		streams.onStdoutDone()
	}
	if streams.onStderrDone != nil {
		streams.onStderrDone()
	}
	<-imageDone
	if stdinRelayErr != nil && result.Status == "OK" {
		result.Status = model.RunStatusRE
		result.Reason = "sandbox stdin relay failed: " + stdinRelayErr.Error()
		result.VerdictSource = "stream_io"
	}

	result.WallTimeMs = timing.SinceMillis(wallStart)
	result.CPUTimeMs = maxCPUTimeMs
	result.Stdout = stdoutBuf.Bytes()
	result.Stderr = stderrBuf.Bytes()
	result.StdoutTruncated = stdoutBuf.Truncated()
	result.StderrTruncated = stderrBuf.Truncated()
	result.MemoryKB = maxRSSKB

	if runGroup.Path != "" {
		if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
			if stats.MemoryPeakBytes > 0 {
				peakKB := stats.MemoryPeakBytes / 1024
				if stats.MemoryPeakBytes%1024 != 0 {
					peakKB++
				}
				if peakKB > result.MemoryKB {
					result.MemoryKB = peakKB
				}
			}
			if stats.CPUUsageMicros > 0 {
				cpuUsageMicros := stats.CPUUsageMicros
				if cgroupCPUBaselineMicros > 0 && cpuUsageMicros > cgroupCPUBaselineMicros {
					cpuUsageMicros -= cgroupCPUBaselineMicros
				}
				if cpuTimeMs := cpuUsageMicros / 1000; cpuTimeMs > result.CPUTimeMs {
					result.CPUTimeMs = cpuTimeMs
				}
			}
			if result.Status == "OK" {
				switch cgroupLimitBreachSince(stats, cgroupLimitBaseline, cgroupLimitBaselineSet) {
				case cgroup.LimitBreachMemory:
					result.Status = model.RunStatusMLE
					result.Reason = "memory limit exceeded"
					result.VerdictSource = "memory_cgroup_final"
					if memoryLimitKB > result.MemoryKB {
						result.MemoryKB = memoryLimitKB
					}
				case cgroup.LimitBreachPids:
					result.Status = model.RunStatusRE
					result.Reason = "process limit exceeded"
					result.VerdictSource = "pids_cgroup_final"
				}
			}
		}
	}

	if result.Status == "OK" {
		usage, err := workspacequota.Scan(ws.RootDir)
		switch {
		case errors.Is(err, workspacequota.ErrEntryLimitExceeded):
			result.Status = model.RunStatusWLE
			result.Reason = "workspace entry limit exceeded"
			result.VerdictSource = "workspace_entries_final"
		case errors.Is(err, workspacequota.ErrDepthExceeded):
			result.Status = model.RunStatusWLE
			result.Reason = "workspace depth exceeded"
			result.VerdictSource = "workspace_depth_final"
		case err != nil:
			result.Status = model.RunStatusWLE
			result.Reason = "workspace scan failed"
			result.VerdictSource = "workspace_scan_final"
		case usage.Bytes > workspaceLimitBytes:
			result.Status = model.RunStatusWLE
			result.Reason = "workspace quota exceeded"
			result.VerdictSource = "workspace_bytes_final"
		}
	}

	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			if ws.Exited() {
				c := ws.ExitStatus()
				result.ExitCode = &c
			}
			if ws.Signaled() {
				result.Status, result.Reason, result.VerdictSource = classifySandboxSignal(
					result.Status,
					result.Reason,
					result.VerdictSource,
					ws.Signal(),
					ctx.Err(),
					parentKillReason,
				)
			}
		}
		if result.Status == "OK" && waitErr != nil && !waitErrorIsProcessExit(waitErr) {
			result.Status = model.RunStatusRE
			if streams.stdout != nil || streams.stderr != nil {
				result.Reason = "sandbox stream relay failed: " + waitErr.Error()
				result.VerdictSource = "stream_io"
			} else {
				result.Reason = "sandbox process wait failed: " + waitErr.Error()
				result.VerdictSource = "wait_status"
			}
		}
		if processCPU := ps.UserTime() + ps.SystemTime(); processCPU > 0 {
			result.ProcessCPUTimeMs = timing.MilliFromDuration(processCPU)
			if targetStarted && runGroup.Path == "" {
				usageCPUNs := uint64(processCPU.Nanoseconds())
				if finalCPUTimeMs, ok := cpuTimeAfterBaseline(usageCPUNs, cpuBaselineNs, cpuBaselineSet); ok {
					if finalCPUTimeMs > result.CPUTimeMs {
						result.CPUTimeMs = finalCPUTimeMs
					}
				}
			}
			if !targetStarted && result.CPUTimeMs <= 0 {
				result.CPUTimeMs = result.ProcessCPUTimeMs
			}
		}
	}
	helperRuntimeOOM := bytes.Contains(result.Stderr, []byte("fatal error: runtime: out of memory"))
	helperPageSummaryOOM := bytes.Contains(result.Stderr, []byte("fatal error: failed to reserve page summary memory"))
	if !targetStarted && result.Status != model.RunStatusTLE && bytes.Contains(result.Stderr, []byte("runtime stack:")) && (helperRuntimeOOM || helperPageSummaryOOM) {
		result.Status = model.RunStatusInitFail
		result.Reason = "sandbox helper failed before target start: out of memory"
		result.VerdictSource = "sandbox_helper_oom"
	}
	if addressSpaceProximityCanClassifyMLE(runtimeBase, runLang) && !disableAddressSpaceLimit && result.Status != model.RunStatusTLE && result.Status != model.RunStatusInitFail && memoryLimitKB > 0 && maxVmSizeKB > 0 && maxVmSizeKB+addressSpaceSlackKB >= addressSpaceLimitKB {
		result.Status = model.RunStatusMLE
		result.Reason = "memory limit exceeded"
		result.VerdictSource = "address_space"
	}
	result.Status, result.Reason, result.VerdictSource = applyFinalCPUTimeStatus(result.Status, result.Reason, result.VerdictSource, result.CPUTimeMs, timeLimitMs, runGroup.Path != "")
	if result.ExitCode != nil && *result.ExitCode == 120 && bytes.Contains(result.Stderr, []byte("sandbox-init:")) {
		result.Status = model.RunStatusInitFail
		result.Reason = clipUTF8(result.Stderr, responseStderrLimitBytes(req))
		if strings.TrimSpace(result.Reason) == "" {
			result.Reason = "sandbox initialization failed"
		}
		result.VerdictSource = "sandbox_init"
	}
	if result.Status == "OK" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = model.RunStatusTLE
		result.VerdictSource = "wall_time"
	}
	return result
}

// waitErrorIsProcessExit reports whether cmd.Wait returned the target's normal
// non-zero exit status. The caller must preserve this for the higher-level
// contestant, SPJ, or interactor verdict classifier instead of treating it as
// a sandbox transport failure.
func waitErrorIsProcessExit(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

func classifySandboxSignal(status, reason, source string, signal syscall.Signal, ctxErr error, parentKillReason string) (string, string, string) {
	if status != "OK" {
		return status, reason, source
	}
	if signal == syscall.SIGXCPU {
		return model.RunStatusTLE, "cpu time limit exceeded", "cpu_rlimit"
	}
	if signal != syscall.SIGKILL {
		return model.RunStatusRE, reason, "signal"
	}
	switch parentKillReason {
	case "wall_time":
		return model.RunStatusTLE, "wall time limit exceeded", "wall_time"
	case "cpu_time", "cpu_time_cgroup":
		return model.RunStatusTLE, "cpu time limit exceeded", parentKillReason
	case "memory_rss", "memory_cgroup":
		return model.RunStatusMLE, "memory limit exceeded", parentKillReason
	case "workspace_bytes", "workspace_entries", "workspace_depth", "workspace_scan":
		return model.RunStatusWLE, reason, parentKillReason
	case "pids_cgroup":
		return model.RunStatusRE, "process limit exceeded", parentKillReason
	}
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return model.RunStatusTLE, "wall time limit exceeded", "wall_time"
	}
	return model.RunStatusRE, "process received SIGKILL without a recorded sandbox limit", "signal_unattributed"
}

func cgroupLimitBreachSince(stats, baseline cgroup.Stats, hasBaseline bool) cgroup.LimitBreach {
	baseOOM := int64(0)
	baseMemoryMax := int64(0)
	basePidsMax := int64(0)
	if hasBaseline {
		baseOOM = baseline.OOMEvents()
		baseMemoryMax = baseline.MemoryMaxEvents()
		basePidsMax = baseline.PidsMaxEvents()
	}
	if stats.OOMEvents() > baseOOM || stats.MemoryMaxEvents() > baseMemoryMax {
		return cgroup.LimitBreachMemory
	}
	if stats.PidsMaxEvents() > basePidsMax {
		return cgroup.LimitBreachPids
	}
	return cgroup.LimitBreachNone
}

func cleanupSandboxCgroup(scope string, group cgroup.Group) {
	if strings.TrimSpace(group.Path) == "" {
		return
	}
	if err := group.KillAndRemoveWithRetry(250 * time.Millisecond); err != nil {
		slog.Warn("sandbox cgroup cleanup failed", "scope", scope, "path", group.Path, "err", err)
	}
}

func ioCopy(dst interface{ Write([]byte) (int, error) }, src any) (int64, error) {
	r, ok := src.(interface{ Read([]byte) (int, error) })
	if !ok {
		return 0, nil
	}
	var n int64
	buf := make([]byte, 16*1024)
	for {
		k, readErr := r.Read(buf)
		if k > 0 {
			nn, writeErr := dst.Write(buf[:k])
			n += int64(nn)
			if writeErr != nil {
				return n, writeErr
			}
			if nn != k {
				return n, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrClosedPipe) || errors.Is(readErr, os.ErrClosed) || errors.Is(readErr, context.Canceled) || strings.Contains(readErr.Error(), "file already closed") {
				return n, nil
			}
			return n, readErr
		}
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

func responseOutputLimitBytes(req *model.RunRequest) int {
	return min(outputLimitBytes(req), maxResponseOutputBytes)
}

func responseStdoutLimitBytes(req *model.RunRequest) int {
	if req == nil || req.CaptureLimits == nil {
		return responseOutputLimitBytes(req)
	}
	return responseCaptureLimitBytes(req.CaptureLimits.StdoutBytes, outputLimitBytes(req))
}

func responseStderrLimitBytes(req *model.RunRequest) int {
	if req == nil || req.CaptureLimits == nil {
		return responseOutputLimitBytes(req)
	}
	return responseCaptureLimitBytes(req.CaptureLimits.StderrBytes, outputLimitBytes(req))
}

func responseCaptureLimitBytes(configured *int, executionLimit int) int {
	if configured == nil {
		return min(executionLimit, maxResponseOutputBytes)
	}
	if *configured <= 0 {
		return 0
	}
	return min(*configured, executionLimit, maxResponseCaptureBytes)
}

func firstImagePath(paths []model.OutputFile) string {
	for _, p := range paths {
		clean := filepath.ToSlash(strings.ToLower(strings.TrimSpace(p.Path)))
		if !strings.HasPrefix(clean, "__img__/") {
			continue
		}
		base := filepath.Base(clean)
		ext := filepath.Ext(base)
		if ext != ".jsonl" && ext != ".ndjson" {
			continue
		}
		name := strings.TrimSuffix(base, ext)
		if name == "image" || name == "images" || name == "img" || strings.HasPrefix(name, "image-") || strings.HasPrefix(name, "img-") || strings.HasPrefix(name, "frame-") {
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
	var pendingLines []string

	readNew := func(final bool) bool {
		hasUnread := false
		if len(pendingLines) == 0 && streamBytes < maxImageStreamBytes {
			output, err := openWorkspaceReadOnly(ws, clean)
			if err == nil {
				if output.info.Size() > offset {
					if _, err := output.file.Seek(offset, 0); err == nil {
						remaining := maxImageStreamBytes - streamBytes
						available := output.info.Size() - offset
						if available > remaining {
							available = remaining
						}
						if available > maxImageReadChunkBytes {
							available = maxImageReadChunkBytes
						}
						if available > 0 {
							chunk := make([]byte, available)
							n, _ := output.file.Read(chunk)
							if n > 0 {
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
								pendingLines = append(pendingLines, lines...)
							}
						}
					}
				}
				hasUnread = output.info.Size() > offset && streamBytes < maxImageStreamBytes
				output.cleanup()
			}
		}
		if final && !hasUnread && carry != "" {
			pendingLines = append(pendingLines, carry)
			carry = ""
		}

		emitted := 0
		consumed := 0
		for consumed < len(pendingLines) {
			if emitted >= maxImageEventsPerRead {
				break
			}
			line := pendingLines[consumed]
			consumed++
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
		pendingLines = pendingLines[consumed:]
		if !final {
			return false
		}
		return len(pendingLines) > 0 || hasUnread || carry != "" || consumed > 0
	}

	for {
		select {
		case <-ctx.Done():
			for readNew(true) {
			}
			return
		case <-ticker.C:
			readNew(false)
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
			if errors.Is(err, io.EOF) {
				return out.Bytes(), nil
			}
			return out.Bytes(), err
		}
	}
}
