package compile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aonohako/internal/isolation/cgroup"
	"aonohako/internal/model"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/util"
	"aonohako/internal/workspacequota"
)

func RunSandboxedCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	stdout, stderr, status, reason = runSandboxedCommandWithGroups(ctx, workDir, bin, args, env, nil, nil)
	stdout, _ = capCompileOutputValue(stdout)
	stderr, _ = capCompileOutputValue(stderr)
	return stdout, stderr, status, reason
}

func runSandboxedCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	return runSandboxedCommandWithGroups(ctx, workDir, bin, args, env, nil, nil)
}

func runSandboxedCommandWithGroups(ctx context.Context, workDir, bin string, args, env []string, supplementaryGroups []uint32, additionalWorkspaceDirs []string) (stdout, stderr, status, reason string) {
	workspaceDirs := security.WorkspaceScopedDirs(workDir)
	seenWorkspaceDirs := make(map[string]struct{}, len(workspaceDirs)+len(additionalWorkspaceDirs))
	for _, dir := range workspaceDirs {
		seenWorkspaceDirs[dir] = struct{}{}
	}
	for _, rel := range additionalWorkspaceDirs {
		clean, err := util.ValidateRelativePath(rel)
		if err != nil {
			return "", "", model.CompileStatusInternal, "invalid command workspace directory"
		}
		dir := filepath.Join(workDir, clean)
		if _, exists := seenWorkspaceDirs[dir]; exists {
			continue
		}
		seenWorkspaceDirs[dir] = struct{}{}
		workspaceDirs = append(workspaceDirs, dir)
	}
	for _, dir := range workspaceDirs {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			slog.Warn("compile sandbox workspace preparation failed", "err", err)
			return "", "", model.CompileStatusInternal, "workspace preparation failed"
		}
	}
	if os.Geteuid() == 0 {
		scopedDirs := make(map[string]struct{}, len(workspaceDirs))
		for _, dir := range workspaceDirs {
			scopedDirs[dir] = struct{}{}
		}
		if err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() {
				return nil
			}
			if path != workDir {
				if _, ok := scopedDirs[path]; ok {
					return filepath.SkipDir
				}
			}
			return os.Chmod(path, 0o777|os.ModeSticky)
		}); err != nil {
			slog.Warn("compile sandbox workspace permission walk failed", "err", err)
			return "", "", model.CompileStatusInternal, "workspace preparation failed"
		}
		for _, dir := range workspaceDirs {
			if err := os.Chown(dir, 65532, 65532); err != nil {
				slog.Warn("compile sandbox workspace ownership failed", "err", err)
				return "", "", model.CompileStatusInternal, "workspace preparation failed"
			}
			if err := os.Chmod(dir, 0o700); err != nil {
				slog.Warn("compile sandbox workspace chmod failed", "err", err)
				return "", "", model.CompileStatusInternal, "workspace preparation failed"
			}
		}
	}
	finalEnv := make(map[string]string, len(util.BaseEnv())+len(security.WorkspaceScopedEnv(workDir))+len(env))
	for _, item := range util.BaseEnv() {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	for _, item := range security.WorkspaceScopedEnv(workDir) {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	for _, item := range env {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			finalEnv[parts[0]] = parts[1]
		}
	}
	for _, key := range []string{"http_proxy", "https_proxy", "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "no_proxy"} {
		finalEnv[key] = ""
	}
	command := append([]string{bin}, args...)
	helperEnv := make([]string, 0, len(finalEnv))
	for key, value := range finalEnv {
		helperEnv = append(helperEnv, key+"="+value)
	}
	sort.Strings(helperEnv)
	if !filepath.IsAbs(command[0]) {
		path, err := util.ResolveCommandPath(command[0], helperEnv)
		if err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				return "", "", model.CompileStatusInternal, bin + " not found"
			}
			return "", "", model.CompileStatusInternal, err.Error()
		}
		command[0] = path
	}
	commandName := filepath.Base(command[0])
	isDotnet := commandName == "dotnet"
	isDotnetLike := isDotnet || commandName == "dafny"
	isPowerShell := commandName == "pwsh"
	isIsabelle := commandName == "isabelle"
	isSyntaxOnlySchemeReader := commandName == "chezscheme"
	runtimeState, err := security.AcquireRuntimeState(workDir, commandName, 65532, 65532)
	if err != nil {
		return "", "", model.CompileStatusInternal, "runtime state preparation failed: " + err.Error()
	}
	defer func() {
		if releaseErr := runtimeState.Release(); releaseErr != nil {
			slog.Error("compile runtime state cleanup failed", "command", commandName, "err", releaseErr)
			if status == "" || status == model.CompileStatusOK {
				status = model.CompileStatusInternal
				reason = "runtime state cleanup failed"
			}
		}
	}()
	// CoreCLR and some toolchain WebAssembly runtimes reserve very large virtual
	// address ranges during startup, so finite RLIMIT_AS values can fail before
	// user code. Dotnet-like commands get a high finite RLIMIT_FSIZE floor
	// because lower file-size rlimits can break CoreCLR/F# startup before user code.
	disableAddressSpaceLimit := isDotnetLike || isPowerShell || commandName == "aonohako-acl2-check" || commandName == "aonohako-alloy-check" || commandName == "aonohako-assemblyscript-compile" || commandName == "aonohako-kframework-check" || commandName == "c3c" || commandName == "carbon" || commandName == "fstar.exe" || commandName == "kotlinc" || commandName == "kompile" || commandName == "spago" || isIsabelle
	allowProcessGroups := commandName == "aonohako-kframework-check" || commandName == "swiftc" || commandName == "hare" || commandName == "kompile" || isIsabelle
	allowProcesses := commandName != "aonohako-assemblyscript-compile" && commandName != "factor" && !isSyntaxOnlySchemeReader
	allowUnixSockets := commandName != "aonohako-assemblyscript-compile" && commandName != "factor" && !isSyntaxOnlySchemeReader
	allowThreadSignals := isDotnetLike || commandName == "factor"
	allowChmod := isDotnetLike || commandName == "aonohako-kframework-check" || commandName == "apecc" || commandName == "gleam" || commandName == "hare" || commandName == "idris2" || commandName == "kompile" || commandName == "rescript" || commandName == "zero" || isIsabelle
	allowExecveat := commandName == "hare"
	openFileLimit := security.OpenFileLimitForCommand(command[0])
	memoryLimitMB := compileSandboxMemoryMB
	if commandName == "kotlinc-native" {
		memoryLimitMB = 4096
	}
	if commandName == "aonohako-acl2-check" || commandName == "aonohako-alloy-check" || commandName == "aonohako-kframework-check" || commandName == "kotlinc" || commandName == "kompile" || commandName == "dafny" || commandName == "isabelle" || commandName == "deno" || commandName == "spago" {
		memoryLimitMB = 4096
	}
	memoryLimitKB := int64(memoryLimitMB) * 1024
	threadLimit := compileSandboxThreadLimit
	if commandName == "dafny" {
		threadLimit = 1024
	}
	addressSpaceLimit := compileAddressSpaceLimitBytes(commandName, memoryLimitMB)
	helperReq := sandbox.ExecRequest{
		Command: append([]string(nil), command...),
		Dir:     workDir,
		Env:     helperEnv,
		Limits: model.Limits{
			TimeMs:         int(buildTimeout / time.Millisecond),
			MemoryMB:       memoryLimitMB,
			WorkspaceBytes: compileWorkspaceBytes,
		},
		ThreadLimit:              threadLimit,
		OpenFileLimit:            openFileLimit,
		StackLimitBytes:          security.StackLimitForCommand(command[0]),
		AddressSpaceLimitBytes:   addressSpaceLimit,
		FileSizeLimitBytes:       security.FileSizeLimitForCommand(command[0], compileWorkspaceBytes),
		EnableNetwork:            false,
		AllowUnixSockets:         allowUnixSockets,
		AllowSocketBind:          isIsabelle,
		AllowSocketConnect:       isIsabelle,
		AllowSocketServer:        isIsabelle,
		AllowProcesses:           allowProcesses,
		AllowProcessGroups:       allowProcessGroups,
		AllowThreadSignals:       allowThreadSignals,
		AllowMemfdCreate:         isDotnetLike || isIsabelle,
		AllowNumaPolicy:          isDotnetLike || isIsabelle,
		AllowChmod:               allowChmod,
		AllowExecveat:            allowExecveat,
		DisableAddressSpaceLimit: disableAddressSpaceLimit,
		DisableFileSizeLimit:     false,
	}
	rawReq, err := json.Marshal(helperReq)
	if err != nil {
		return "", "", model.CompileStatusInternal, "sandbox request failed: " + err.Error()
	}

	requestRead, requestWrite, err := os.Pipe()
	if err != nil {
		return "", "", model.CompileStatusInternal, "sandbox request pipe failed: " + err.Error()
	}
	defer requestRead.Close()
	defer requestWrite.Close()
	helperPath, err := os.Executable()
	if err != nil {
		return "", "", model.CompileStatusInternal, "resolve helper failed: " + err.Error()
	}
	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Dir = workDir
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		sandbox.HelperModeEnv + "=" + sandbox.HelperModeExec,
		sandbox.RequestFDEnv + "=3",
	}
	cmd.ExtraFiles = []*os.File{requestRead}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if os.Geteuid() == 0 {
		cmd.SysProcAttr.Credential = &syscall.Credential{
			Uid:    65532,
			Gid:    65532,
			Groups: compileHelperCredentialGroups(supplementaryGroups),
		}
	}
	stdoutFile, err := os.CreateTemp(filepath.Join(workDir, ".tmp"), "compile-stdout-*")
	if err != nil {
		return "", "", model.CompileStatusInternal, "stdout capture failed: " + err.Error()
	}
	defer func() {
		_ = stdoutFile.Close()
		_ = os.Remove(stdoutFile.Name())
	}()
	stderrFile, err := os.CreateTemp(filepath.Join(workDir, ".tmp"), "compile-stderr-*")
	if err != nil {
		return "", "", model.CompileStatusInternal, "stderr capture failed: " + err.Error()
	}
	defer func() {
		_ = stderrFile.Close()
		_ = os.Remove(stderrFile.Name())
	}()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	sandbox.TrimParentMemoryBeforeSandbox()
	if err := cmd.Start(); err != nil {
		return "", "", model.CompileStatusInternal, "start failed: " + err.Error()
	}
	cgroupParentDir := compileCgroupParentFromContext(ctx)
	var runGroup cgroup.Group
	if cgroupParentDir != "" {
		if err := cgroup.EnableControllers(cgroupParentDir, []string{"cpu", "memory", "pids"}); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return "", "", model.CompileStatusInternal, "cgroup controller setup failed: " + err.Error()
		}
		group, err := cgroup.CreateRunGroup(cgroupParentDir, cgroup.RunName("compile"), cgroup.Limits{
			MemoryMaxBytes:  memoryLimitKB * 1024,
			PidsMax:         threadLimit + 32,
			CPUQuotaMicros:  cgroup.SingleCPUQuotaMicros,
			CPUPeriodMicros: cgroup.DefaultCPUPeriodMicros,
		})
		if err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			return "", "", model.CompileStatusInternal, "cgroup create failed: " + err.Error()
		}
		runGroup = group
		if err := runGroup.AddProc(cmd.Process.Pid); err != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
			cleanupCompileCgroup(runGroup)
			return "", "", model.CompileStatusInternal, "cgroup add process failed: " + err.Error()
		}
		defer func() {
			cleanupCompileCgroup(runGroup)
		}()
	}
	_ = os.WriteFile(fmt.Sprintf("/proc/%d/oom_score_adj", cmd.Process.Pid), []byte("1000\n"), 0o644)
	_ = requestRead.Close()
	if n, err := requestWrite.Write(rawReq); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: " + err.Error()
	} else if n != len(rawReq) {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: short write"
	}
	if err := requestWrite.Close(); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
		return "", "", model.CompileStatusInternal, "sandbox request write failed: " + err.Error()
	}
	pgid := cmd.Process.Pid
	descendantPIDs := func() map[int]bool {
		descendants := map[int]bool{pgid: true}
		for changed := true; changed; {
			changed = false
			entries, err := os.ReadDir("/proc")
			if err != nil {
				break
			}
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				pid, err := strconv.Atoi(entry.Name())
				if err != nil || descendants[pid] {
					continue
				}
				raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
				if err != nil {
					continue
				}
				ppid := 0
				for _, line := range strings.Split(string(raw), "\n") {
					if strings.HasPrefix(line, "PPid:") {
						fields := strings.Fields(line)
						if len(fields) >= 2 {
							ppid, _ = strconv.Atoi(fields[1])
						}
						break
					}
				}
				if ppid > 0 && descendants[ppid] {
					descendants[pid] = true
					changed = true
				}
			}
		}
		return descendants
	}
	processTreeRSSKB := func(pids map[int]bool) int64 {
		pageKB := int64(os.Getpagesize() / 1024)
		var total int64
		for pid := range pids {
			raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/statm", pid))
			if err != nil {
				continue
			}
			fields := strings.Fields(string(raw))
			if len(fields) < 2 {
				continue
			}
			rssPages, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				continue
			}
			total += rssPages * pageKB
		}
		return total
	}
	killSandbox := func() {
		descendants := descendantPIDs()
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		for pid := range descendants {
			if pid != pgid {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			pid, err := strconv.Atoi(entry.Name())
			if err != nil || pid == pgid || descendants[pid] {
				continue
			}
			status, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "status"))
			if err != nil {
				continue
			}
			sandboxUID := false
			for _, line := range strings.Split(string(status), "\n") {
				if !strings.HasPrefix(line, "Uid:") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) >= 2 && fields[1] == "65532" {
					sandboxUID = true
				}
				break
			}
			if !sandboxUID {
				continue
			}
			cwd, err := os.Readlink(filepath.Join("/proc", entry.Name(), "cwd"))
			if err != nil {
				continue
			}
			if cwd == workDir || strings.HasPrefix(cwd, strings.TrimRight(workDir, string(os.PathSeparator))+string(os.PathSeparator)) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	readCaptured := func(file *os.File) string {
		if _, err := file.Seek(0, 0); err != nil {
			return ""
		}
		data, err := io.ReadAll(io.LimitReader(file, compileOutputCaptureBytes+1))
		if err != nil {
			return ""
		}
		return string(data)
	}
	defer killSandbox()
	watchdog := time.NewTicker(25 * time.Millisecond)
	defer watchdog.Stop()
	lastWorkspaceScan := time.Time{}
	for {
		select {
		case <-ctx.Done():
			killSandbox()
			<-waitCh
			return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusTimeout, ctx.Err().Error()
		case <-watchdog.C:
			if runGroup.Path != "" {
				if stats, err := cgroup.ReadStats(runGroup.Path); err == nil {
					switch stats.FirstLimitBreach(memoryLimitKB * 1024) {
					case cgroup.LimitBreachMemory:
						killSandbox()
						<-waitCh
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
					case cgroup.LimitBreachPids:
						killSandbox()
						<-waitCh
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "process limit exceeded"
					}
				}
			}
			if rssKB := processTreeRSSKB(descendantPIDs()); rssKB > memoryLimitKB {
				killSandbox()
				<-waitCh
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
			}
			if lastWorkspaceScan.IsZero() || time.Since(lastWorkspaceScan) >= 25*time.Millisecond {
				lastWorkspaceScan = time.Now()
				usage, err := workspacequota.Scan(workDir)
				if errors.Is(err, workspacequota.ErrEntryLimitExceeded) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace entry limit exceeded"
				}
				if errors.Is(err, workspacequota.ErrDepthExceeded) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace depth exceeded"
				}
				if err != nil {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace scan failed"
				}
				if usage.Bytes > int64(compileWorkspaceBytes) {
					killSandbox()
					<-waitCh
					return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace quota exceeded"
				}
			}
		case err := <-waitCh:
			if runGroup.Path != "" {
				if stats, statsErr := cgroup.ReadStats(runGroup.Path); statsErr == nil {
					switch stats.FirstLimitBreach(memoryLimitKB * 1024) {
					case cgroup.LimitBreachMemory:
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "memory limit exceeded"
					case cgroup.LimitBreachPids:
						return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "process limit exceeded"
					}
				}
			}
			if usage, scanErr := workspacequota.Scan(workDir); errors.Is(scanErr, workspacequota.ErrEntryLimitExceeded) {
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace entry limit exceeded"
			} else if errors.Is(scanErr, workspacequota.ErrDepthExceeded) {
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace depth exceeded"
			} else if scanErr != nil {
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace scan failed"
			} else if usage.Bytes > int64(compileWorkspaceBytes) {
				return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusCompileError, "workspace quota exceeded"
			}
			if err != nil {
				reason := err.Error()
				status := model.CompileStatusCompileError
				if ps := cmd.ProcessState; ps != nil {
					if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
						status, reason = classifyCompileWaitStatus(ws)
					}
				}
				return readCaptured(stdoutFile), readCaptured(stderrFile), status, reason
			}
			return readCaptured(stdoutFile), readCaptured(stderrFile), model.CompileStatusOK, ""
		}
	}
}

func compileHelperCredentialGroups(supplementaryGroups []uint32) []uint32 {
	groups := []uint32{65532}
	seen := map[uint32]struct{}{65532: {}}
	for _, gid := range supplementaryGroups {
		if _, exists := seen[gid]; exists {
			continue
		}
		seen[gid] = struct{}{}
		groups = append(groups, gid)
	}
	return groups
}

func classifyCompileWaitStatus(status syscall.WaitStatus) (string, string) {
	if status.Signaled() {
		if status.Signal() == syscall.SIGXCPU {
			return model.CompileStatusTimeout, "sandbox command exceeded CPU time limit"
		}
		return model.CompileStatusInternal, fmt.Sprintf("sandbox command killed by signal %s", status.Signal())
	}
	if status.Exited() {
		return model.CompileStatusCompileError, fmt.Sprintf("sandbox command exited with code %d", status.ExitStatus())
	}
	return model.CompileStatusCompileError, "sandbox command failed"
}

func cleanupCompileCgroup(group cgroup.Group) {
	if strings.TrimSpace(group.Path) == "" {
		return
	}
	if err := group.KillAndRemoveWithRetry(250 * time.Millisecond); err != nil {
		slog.Warn("compile cgroup cleanup failed", "path", group.Path, "err", err)
	}
}

func compileAddressSpaceLimitBytes(commandBase string, memoryMB int) uint64 {
	switch commandBase {
	case "deno":
		limitMB := max(65536, memoryMB*4+1024)
		return uint64(limitMB) * 1024 * 1024
	default:
		return 0
	}
}

func runCommand(ctx context.Context, workDir, bin string, args, env []string) (stdout, stderr, status, reason string) {
	return runSandboxedCommand(ctx, workDir, bin, args, env)
}
