package execute

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
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
	"unicode/utf8"

	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/profiles"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/timing"
	"aonohako/internal/util"
)

const (
	defaultMaxOutputBytes        = 64 << 10
	hardMaxOutputBytes           = 8 << 20
	defaultWorkspaceBytes        = 128 << 20
	hardMaxWorkspaceBytes        = 1 << 30
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = 16 << 20
	maxBinaryTotalBytes          = 48 << 20
	maxCapturedFileBytes         = 8 << 20
	maxCapturedSidecarTotalBytes = 16 << 20
	ocamlRunParam                = "s=32k"
	elixirERLAFlags              = "+MIscs 128 +S 1:1 +A 1"
)

type Hooks struct {
	OnImage func(mime, b64 string, ts int64)
	OnLog   func(stream, msg string)
}

type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		if _, err := b.buf.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	startWall := timing.MonotonicNow()
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	if len(req.FileOutputs) > 1 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "at most one file output is supported"}
	}
	capturedOutputLimit := outputLimitBytes(req)
	if len(req.Binaries) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "no binaries"}
	}

	workDir, err := util.CreateWorkDir("aonohako-run-*")
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "mkdtemp failed: " + err.Error()}
	}
	defer os.RemoveAll(workDir)

	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "workspace prep failed: " + err.Error()}
	}

	primaryPath, runLang, err := materializeFiles(ws, req)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "materialize failed: " + err.Error()}
	}

	cmdArgs := buildCommand(primaryPath, runLang, req)
	if len(cmdArgs) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "empty command"}
	}

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, hooks, capturedOutputLimit)

	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		return model.RunResponse{Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs, CPUTimeMs: 0, Reason: res.Reason}
	}

	rawOut := res.Stdout
	judgeOut := rawOut
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err != nil {
			if res.Status == "OK" {
				res.Status = model.RunStatusRE
				res.Reason = "file output capture failed: " + err.Error()
			}
		} else {
			judgeOut = captured
		}
	}

	sidecarOutputs := captureSidecarOutputs(ws, req.SidecarOutputs)

	status := res.Status
	if status == "OK" && req.Limits.MemoryMB > 0 && res.MemoryKB > int64(req.Limits.MemoryMB*1024) {
		status = model.RunStatusMLE
	}
	if status == "OK" && res.ExitCode != nil && *res.ExitCode != 0 {
		status = model.RunStatusRE
	}

	var score *float64
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

	var outResp, errResp string
	if status == model.RunStatusWA || status == model.RunStatusRE || (status == model.RunStatusTLE && req.IgnoreTLE) {
		outResp = clipUTF8(judgeOut, capturedOutputLimit)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp = clipUTF8(fullErr, capturedOutputLimit)
	}

	if hooks.OnLog != nil {
		if len(rawOut) > 0 {
			hooks.OnLog("stdout", clipUTF8(rawOut, capturedOutputLimit))
		}
		if len(fullErr) > 0 {
			hooks.OnLog("stderr", clipUTF8(fullErr, capturedOutputLimit))
		}
	}

	return model.RunResponse{
		Status:         status,
		TimeMs:         res.WallTimeMs,
		WallTimeMs:     res.WallTimeMs,
		CPUTimeMs:      res.CPUTimeMs,
		MemoryKB:       res.MemoryKB,
		ExitCode:       res.ExitCode,
		Stdout:         outResp,
		Stderr:         errResp,
		Reason:         res.Reason,
		Score:          score,
		SidecarOutputs: sidecarOutputs,
	}
}

type execResult struct {
	Status     string
	ExitCode   *int
	Stdout     []byte
	Stderr     []byte
	MemoryKB   int64
	WallTimeMs int64
	CPUTimeMs  int64
	Reason     string
}

type Workspace struct {
	RootDir string
	BoxDir  string
}

func prepareWorkspaceDirs(workDir string) (Workspace, error) {
	ws := Workspace{
		RootDir: workDir,
		BoxDir:  filepath.Join(workDir, "box"),
	}
	dirs := security.WorkspaceScopedDirs(workDir)
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return Workspace{}, err
		}
	}
	if err := os.MkdirAll(ws.BoxDir, 0o777); err != nil {
		return Workspace{}, err
	}
	if err := os.Chmod(ws.BoxDir, 0o777|os.ModeSticky); err != nil {
		return Workspace{}, err
	}
	return ws, nil
}

func materializeFiles(ws Workspace, req *model.RunRequest) (primaryPath string, lang string, err error) {
	lang = profiles.NormalizeRunLang(req.Lang)
	if lang == "" {
		lang = "binary"
	}
	var jarPath string
	var pyPath string
	classFiles := make([]string, 0)
	totalBytes := 0
	for i, b := range req.Binaries {
		clean, err := util.ValidateRelativePath(b.Name)
		if err != nil {
			return "", "", err
		}
		data, err := base64.StdEncoding.DecodeString(b.DataB64)
		if err != nil {
			return "", "", fmt.Errorf("decode %s: %w", clean, err)
		}
		if len(data) > maxBinaryFileBytes {
			return "", "", fmt.Errorf("binary too large: %s", clean)
		}
		totalBytes += len(data)
		if totalBytes > maxBinaryTotalBytes {
			return "", "", fmt.Errorf("binaries total size exceeded")
		}
		dest := filepath.Join(ws.BoxDir, clean)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", "", err
		}
		mode := os.FileMode(0o444)
		if b.Mode == "exec" || isLikelyExec(clean) {
			mode = 0o555
		}
		if err := os.WriteFile(dest, data, mode); err != nil {
			return "", "", err
		}
		if i == 0 {
			primaryPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".jar") {
			jarPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".py") && pyPath == "" {
			pyPath = dest
		}
		if strings.HasSuffix(strings.ToLower(clean), ".class") {
			classFiles = append(classFiles, clean)
		}
	}

	switch lang {
	case "binary", "javascript", "ruby", "php", "lua", "perl", "uhmlang", "csharp", "fsharp", "text", "ocaml", "elixir", "sqlite", "julia", "whitespace", "brainfuck", "wasm":
		return primaryPath, lang, nil
	case "python", "pypy":
		if pyPath == "" {
			pyPath = primaryPath
		}
		return pyPath, lang, nil
	case "erlang":
		hasBeam := false
		for _, binary := range req.Binaries {
			if strings.HasSuffix(strings.ToLower(binary.Name), ".beam") {
				hasBeam = true
				break
			}
		}
		if !hasBeam {
			return "", "", fmt.Errorf("erlang requires .beam files")
		}
		return ws.BoxDir, lang, nil
	case "java":
		if jarPath != "" {
			return jarPath, lang, nil
		}
		jar, err := buildSubmissionJar(ws.BoxDir, req.EntryPoint, classFiles)
		if err != nil {
			return "", "", err
		}
		return jar, lang, nil
	case "scala":
		if len(classFiles) == 0 {
			return "", "", fmt.Errorf("scala requires .class files")
		}
		return ws.BoxDir, lang, nil
	default:
		return "", "", fmt.Errorf("unsupported run lang: %s", lang)
	}
}

func isLikelyExec(name string) bool {
	l := strings.ToLower(name)
	return strings.HasSuffix(l, ".out") || strings.HasSuffix(l, ".bin") || strings.HasSuffix(l, ".run") || strings.HasSuffix(l, ".kexe") || (!strings.Contains(l, ".") && !strings.HasSuffix(l, "/"))
}

func buildSubmissionJar(workDir, entryPoint string, classes []string) (string, error) {
	if len(classes) == 0 {
		return "", fmt.Errorf("java requires .class files")
	}
	mainClass := strings.TrimSpace(entryPoint)
	if mainClass == "" {
		mainClass = "Main"
	}
	mainClass = strings.ReplaceAll(mainClass, "/", ".")
	jarPath := filepath.Join(workDir, "submission.jar")
	file, err := os.Create(jarPath)
	if err != nil {
		return "", err
	}
	zw := zip.NewWriter(file)
	mf, err := zw.Create("META-INF/MANIFEST.MF")
	if err != nil {
		zw.Close()
		file.Close()
		return "", err
	}
	_, _ = mf.Write([]byte(fmt.Sprintf("Manifest-Version: 1.0\r\nMain-Class: %s\r\n\r\n", mainClass)))

	err = filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".class") {
			rel, err := filepath.Rel(workDir, path)
			if err != nil {
				return err
			}
			entry, err := zw.Create(filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			_, err = entry.Write(data)
			return err
		}
		return nil
	})
	if err != nil {
		zw.Close()
		file.Close()
		return "", err
	}
	if err := zw.Close(); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	_ = os.Chmod(jarPath, 0o500)
	return jarPath, nil
}

func buildCommand(primaryPath, lang string, req *model.RunRequest) []string {
	switch lang {
	case "binary":
		return []string{primaryPath}
	case "python":
		return []string{"python3", primaryPath}
	case "pypy":
		return []string{"pypy3", primaryPath}
	case "erlang":
		module := "main"
		function := "main"
		entryPoint := strings.TrimSpace(req.EntryPoint)
		if entryPoint != "" {
			if left, right, ok := strings.Cut(entryPoint, ":"); ok {
				module = left
				function = right
			} else {
				module = entryPoint
			}
		}
		module = strings.TrimSuffix(filepath.Base(strings.TrimSpace(module)), filepath.Ext(strings.TrimSpace(module)))
		function = strings.TrimSpace(function)
		if module == "" {
			module = "main"
		}
		if function == "" {
			function = "main"
		}
		return []string{"erl", "+S", "1:1", "+A", "1", "-noshell", "-pa", primaryPath, "-s", module, function, "-s", "init", "stop"}
	case "scala":
		mainClass := strings.TrimSpace(req.EntryPoint)
		if mainClass == "" {
			mainClass = "Main"
		}
		mainClass = strings.ReplaceAll(mainClass, "/", ".")
		return []string{"scala", "-nocompdaemon", "-classpath", primaryPath, mainClass}
	case "java":
		xmx := max(32, req.Limits.MemoryMB)
		return []string{"java", "-XX:ReservedCodeCacheSize=64m", "-XX:-UseCompressedClassPointers", fmt.Sprintf("-Xmx%dm", xmx), "-Xss16m", "-Dfile.encoding=UTF-8", "-XX:+UseSerialGC", "-DONLINE_JUDGE=1", "-jar", primaryPath}
	case "javascript":
		return []string{"node", "--stack-size=65536", primaryPath}
	case "julia":
		return []string{"julia", "--startup-file=no", "--history-file=no", "--color=no", primaryPath}
	case "ruby":
		return []string{"ruby", primaryPath}
	case "php":
		return []string{"php", "-d", "display_errors=stderr", primaryPath}
	case "lua":
		return []string{"lua5.4", primaryPath}
	case "perl":
		return []string{"perl", primaryPath}
	case "ocaml":
		return []string{"env", "OCAMLRUNPARAM=" + ocamlRunParam, primaryPath}
	case "elixir":
		return []string{"env", "ERL_AFLAGS=" + elixirERLAFlags, "elixir", primaryPath}
	case "sqlite":
		dbPath := filepath.Join(filepath.Dir(primaryPath), ".aonohako.sqlite3")
		return []string{"sh", "-c", "exec sqlite3 \"$0\" < \"$1\"", dbPath, primaryPath}
	case "uhmlang":
		return []string{"/usr/bin/umjunsik-lang-go", primaryPath}
	case "csharp", "fsharp":
		if strings.HasSuffix(strings.ToLower(primaryPath), ".dll") {
			return []string{"dotnet", primaryPath}
		}
		return []string{primaryPath}
	case "whitespace":
		return []string{"python3", "/usr/local/lib/aonohako/whitespace.py", primaryPath}
	case "brainfuck":
		return []string{"python3", "/usr/local/lib/aonohako/brainfuck.py", primaryPath}
	case "wasm":
		return []string{"wasmtime", "run", "--dir=.", primaryPath}
	case "text":
		return []string{"cat", primaryPath}
	default:
		return []string{primaryPath}
	}
}

func runCommandWithSandbox(parent context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks, outputLimitBytes int) execResult {
	limits := req.Limits
	timeMs := max(1, limits.TimeMs)
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeMs)*time.Millisecond)
	defer cancel()

	return executeSandboxCommand(ctx, ws, command, req, hooks, outputLimitBytes)
}

func executeSandboxCommand(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks, outputLimitBytes int) execResult {
	if len(command) == 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox command is empty"}
	}
	if os.Geteuid() != 0 && !platform.IsCloudRun() {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox requires root outside Cloud Run"}
	}
	timeLimitMs := max(1, req.Limits.TimeMs)
	memoryLimitKB := int64(0)
	if req.Limits.MemoryMB > 0 {
		memoryLimitKB = int64(req.Limits.MemoryMB) * 1024
	}
	workspaceLimitBytes := req.Limits.WorkspaceBytes
	if workspaceLimitBytes <= 0 {
		workspaceLimitBytes = defaultWorkspaceBytes
	}
	if workspaceLimitBytes > hardMaxWorkspaceBytes {
		workspaceLimitBytes = hardMaxWorkspaceBytes
	}
	addressSpaceLimitKB := int64(addressSpaceLimitBytes(req.Limits.MemoryMB) / 1024)

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

	switch profiles.NormalizeRunLang(req.Lang) {
	case "ocaml":
		innerEnv = append(innerEnv, "OCAMLRUNPARAM="+ocamlRunParam)
	case "elixir":
		innerEnv = append(innerEnv, "ERL_AFLAGS="+elixirERLAFlags)
	}

	finalCommand := append([]string(nil), command...)
	resolveRealPath := func(name string) (string, error) {
		path, err := exec.LookPath(name)
		if err != nil {
			return "", err
		}
		if real, err := filepath.EvalSymlinks(path); err == nil && real != "" {
			path = real
		}
		return path, nil
	}
	if !filepath.IsAbs(finalCommand[0]) {
		path, err := resolveRealPath(finalCommand[0])
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
			path, err := resolveRealPath(finalCommand[i])
			if err != nil {
				return execResult{Status: model.RunStatusInitFail, Reason: "resolve env command failed: " + err.Error()}
			}
			finalCommand[i] = path
			break
		}
	}

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

	reqPath := filepath.Join(ws.RootDir, ".tmp", "sandbox-request.json")
	helperReq := sandbox.ExecRequest{
		Command:       finalCommand,
		Dir:           ws.BoxDir,
		Env:           innerEnv,
		Limits:        req.Limits,
		ThreadLimit:   sandboxThreadLimit,
		EnableNetwork: req.EnableNetwork,
	}
	rawReq, err := json.Marshal(helperReq)
	if err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request failed: " + err.Error()}
	}
	if err := os.WriteFile(reqPath, rawReq, 0o644); err != nil {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox request write failed: " + err.Error()}
	}

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
	cmd.Env = append(append(baseEnv[:0:0], baseEnv...), sandbox.HelperModeEnv+"="+sandbox.HelperModeExec, sandbox.RequestPathEnv+"="+reqPath)

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

	imageDone := make(chan struct{})
	if imgPath := firstImagePath(req.SidecarOutputs); imgPath != "" {
		go func() {
			streamImageEvents(ctx, ws, imgPath, hooks.OnImage)
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
	watchdog := time.NewTicker(5 * time.Millisecond)
	defer watchdog.Stop()
	lastWorkspaceScan := time.Time{}
	maxCPUTimeMs := int64(0)
	maxRSSKB := int64(0)
	maxVmSizeKB := int64(0)

	result := execResult{Status: "OK"}
	for {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-waitCh
			if result.Status == "OK" {
				result.Status = model.RunStatusTLE
				result.Reason = "wall time limit exceeded"
			}
			goto done
		case err := <-waitCh:
			if err != nil && result.Status == "OK" {
				result.Status = model.RunStatusRE
			}
			goto done
		case <-watchdog.C:
			if !targetStarted {
				exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", cmd.Process.Pid))
				if err != nil {
					continue
				}
				if realExePath, err := filepath.EvalSymlinks(exePath); err == nil && realExePath != "" {
					exePath = realExePath
				}
				if exePath == resolvedHelperPath {
					continue
				}
				targetStarted = true
				cpuBaselineNs, _ = timing.ProcessCPUTimeNs(cmd.Process.Pid)
				lastWorkspaceScan = time.Now()
				continue
			}
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
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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
				if result.Status == "OK" && memoryLimitKB > 0 && maxRSSKB > memoryLimitKB {
					result.Status = model.RunStatusMLE
					result.Reason = "memory limit exceeded"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}

			if result.Status == "OK" && (lastWorkspaceScan.IsZero() || time.Since(lastWorkspaceScan) >= 25*time.Millisecond) {
				lastWorkspaceScan = time.Now()
				workspaceBytes := int64(0)
				_ = filepath.Walk(ws.RootDir, func(path string, info os.FileInfo, err error) error {
					if err != nil || info == nil {
						return nil
					}
					if info.Mode().IsRegular() {
						workspaceBytes += info.Size()
					}
					return nil
				})
				if workspaceBytes > workspaceLimitBytes {
					result.Status = model.RunStatusWLE
					result.Reason = "workspace quota exceeded"
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		}
	}
done:
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	<-doneOut
	<-doneErr
	<-imageDone

	result.WallTimeMs = timing.SinceMillis(wallStart)
	result.CPUTimeMs = maxCPUTimeMs
	result.Stdout = stdoutBuf.Bytes()
	result.Stderr = stderrBuf.Bytes()
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
					} else {
						result.Status = model.RunStatusRE
					}
				}
			}
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
	if result.Status != model.RunStatusTLE && result.Status != model.RunStatusInitFail && memoryLimitKB > 0 && maxVmSizeKB > 0 && maxVmSizeKB+addressSpaceSlackKB >= addressSpaceLimitKB {
		result.Status = model.RunStatusMLE
		result.Reason = "memory limit exceeded"
	}
	if result.ExitCode != nil && *result.ExitCode == 120 && bytes.Contains(result.Stderr, []byte("sandbox-init:")) {
		result.Status = model.RunStatusInitFail
		result.Reason = clipUTF8(result.Stderr, outputLimitBytes)
	}
	if result.Status == "OK" && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = model.RunStatusTLE
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
	if len(paths) == 0 {
		return ""
	}
	return paths[0].Path
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

	readNew := func() {
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
		reader := bufio.NewReader(output.file)
		chunk, _ := ioReadAll(reader)
		offset += int64(len(chunk))
		if len(chunk) == 0 {
			return
		}
		text := carry + string(chunk)
		lines := strings.Split(text, "\n")
		if !strings.HasSuffix(text, "\n") {
			carry = lines[len(lines)-1]
			lines = lines[:len(lines)-1]
		} else {
			carry = ""
		}
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
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
			ts := payload.TS
			if ts == 0 {
				ts = time.Now().UnixMilli()
			}
			emit(payload.Mime, payload.B64, ts)
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

func captureSidecarOutputs(ws Workspace, specs []model.OutputFile) []model.SidecarOutput {
	outputs := make([]model.SidecarOutput, 0, len(specs))
	var totalBytes int64
	for _, spec := range specs {
		output, err := openCapturedOutput(ws, spec.Path)
		if err != nil {
			continue
		}
		if output.info.Size() > maxCapturedFileBytes {
			output.cleanup()
			continue
		}
		totalBytes += output.info.Size()
		if totalBytes > maxCapturedSidecarTotalBytes {
			output.cleanup()
			continue
		}
		if _, err := output.file.Seek(0, 0); err != nil {
			output.cleanup()
			continue
		}
		data, err := ioReadAll(bufio.NewReader(output.file))
		output.cleanup()
		if err != nil {
			continue
		}
		outputs = append(outputs, model.SidecarOutput{Path: spec.Path, DataB64: util.EncodeB64(data)})
	}
	return outputs
}

func hasSPJ(req *model.RunRequest) bool {
	return req != nil && req.SPJ != nil && req.SPJ.Binary != nil && req.SPJ.Binary.Name != "" && req.SPJ.Binary.DataB64 != ""
}

func runSPJ(ctx context.Context, ws Workspace, req *model.RunRequest, userStdout string) (bool, *float64, error) {
	spjPath := filepath.Join(ws.RootDir, "spj-runner")
	data, err := base64.StdEncoding.DecodeString(req.SPJ.Binary.DataB64)
	if err != nil {
		return false, nil, err
	}
	if len(data) > maxBinaryFileBytes {
		return false, nil, fmt.Errorf("spj binary too large")
	}
	if err := os.WriteFile(spjPath, data, 0o500); err != nil {
		return false, nil, err
	}
	defer os.Remove(spjPath)

	inputPath, err := writeTempFile(filepath.Join(ws.RootDir, ".tmp"), "spj-input-*", req.Stdin)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(inputPath)

	solutionPath, err := writeTempFile(filepath.Join(ws.RootDir, ".tmp"), "spj-solution-*", req.ExpectedStdout)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(solutionPath)

	outputPath, err := writeTempFile(filepath.Join(ws.RootDir, ".tmp"), "spj-output-*", userStdout)
	if err != nil {
		return false, nil, err
	}
	defer os.Remove(outputPath)

	spjLang := profiles.NormalizeRunLang(req.SPJ.Lang)
	if spjLang == "" || spjLang == "binary" {
		spjLang = "binary"
	}
	spjReq := &model.RunRequest{Lang: spjLang, Limits: req.Limits, EnableNetwork: false}
	args := buildCommand(spjPath, spjLang, spjReq)
	args = append(args, inputPath, solutionPath, outputPath)
	res := runCommandWithSandbox(ctx, ws, args, &model.RunRequest{Limits: req.Limits, EnableNetwork: false, Stdin: userStdout}, Hooks{}, outputLimitBytes(req))
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

func addressSpaceLimitBytes(memMB int) uint64 {
	base := memMB + 64
	if base < 512 {
		base = 512
	}
	return uint64(base) * 1024 * 1024
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
