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
	"aonohako/internal/profiles"
	"aonohako/internal/sandbox"
	"aonohako/internal/security"
	"aonohako/internal/timing"
	"aonohako/internal/util"
)

const (
	maxReturnBytes               = 3000
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

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	startWall := timing.MonotonicNow()
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
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

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, hooks)

	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		return model.RunResponse{Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs, CPUTimeMs: 0, Reason: res.Reason}
	}

	fullOut := res.Stdout
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err == nil {
			fullOut = captured
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
			ok, sc, spjErr := runSPJ(ctx, ws, req, string(fullOut))
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
			outputOK = compareOutputs([]byte(req.ExpectedStdout), fullOut)
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
		outResp = clipUTF8(fullOut, maxReturnBytes)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp = clipUTF8(fullErr, maxReturnBytes)
	}

	if hooks.OnLog != nil {
		if len(fullOut) > 0 {
			hooks.OnLog("stdout", clipUTF8(fullOut, 4096))
		}
		if len(fullErr) > 0 {
			hooks.OnLog("stderr", clipUTF8(fullErr, 4096))
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
	case "binary", "javascript", "ruby", "php", "lua", "perl", "uhmlang", "csharp", "text", "ocaml", "elixir":
		return primaryPath, lang, nil
	case "python", "pypy":
		if pyPath == "" {
			pyPath = primaryPath
		}
		return pyPath, lang, nil
	case "java":
		if jarPath != "" {
			return jarPath, lang, nil
		}
		jar, err := buildSubmissionJar(ws.BoxDir, req.EntryPoint, classFiles)
		if err != nil {
			return "", "", err
		}
		return jar, lang, nil
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
	case "java":
		xmx := max(32, req.Limits.MemoryMB)
		return []string{"java", "-XX:ReservedCodeCacheSize=64m", "-XX:-UseCompressedClassPointers", fmt.Sprintf("-Xmx%dm", xmx), "-Xss16m", "-Dfile.encoding=UTF-8", "-XX:+UseSerialGC", "-DONLINE_JUDGE=1", "-jar", primaryPath}
	case "javascript":
		return []string{"node", "--stack-size=65536", primaryPath}
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
	case "uhmlang":
		return []string{"/usr/bin/umjunsik-lang-go", primaryPath}
	case "csharp":
		if strings.HasSuffix(strings.ToLower(primaryPath), ".dll") {
			return []string{"dotnet", primaryPath}
		}
		return []string{primaryPath}
	case "text":
		return []string{"cat", primaryPath}
	default:
		return []string{primaryPath}
	}
}

func runCommandWithSandbox(parent context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks) execResult {
	limits := req.Limits
	timeMs := max(1, limits.TimeMs)
	ctx, cancel := context.WithTimeout(parent, time.Duration(timeMs)*time.Millisecond)
	defer cancel()

	return executeSandboxCommand(ctx, ws, command, req, hooks)
}

func executeSandboxCommand(ctx context.Context, ws Workspace, command []string, req *model.RunRequest, hooks Hooks) execResult {
	if len(command) == 0 {
		return execResult{Status: model.RunStatusInitFail, Reason: "sandbox command is empty"}
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
		chownTargets := append([]string{ws.BoxDir}, security.WorkspaceScopedDirs(ws.RootDir)...)
		for _, dir := range chownTargets {
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

	policies := make([]sandbox.PathPolicy, 0, 32)
	seenPolicies := make(map[string]struct{}, 32)
	addPolicy := func(path, access string) {
		if path == "" {
			return
		}
		if _, err := os.Lstat(path); err != nil {
			return
		}
		key := access + "\x00" + path
		if _, ok := seenPolicies[key]; ok {
			return
		}
		seenPolicies[key] = struct{}{}
		policies = append(policies, sandbox.PathPolicy{Path: path, Access: access})
	}
	for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt"} {
		addPolicy(path, "runtime")
	}
	for _, path := range []string{"/etc/ssl", "/etc/pki", "/etc/ca-certificates", "/etc/java-17-openjdk", "/etc/dotnet", "/etc/fonts", "/etc/xml"} {
		addPolicy(path, "readonly")
	}
	addPolicy(ws.BoxDir, "box")
	for _, dir := range security.WorkspaceScopedDirs(ws.RootDir) {
		addPolicy(dir, "scratch")
	}
	addPolicy("/dev/null", "devrw")
	addPolicy("/dev/zero", "devread")
	addPolicy("/dev/random", "devread")
	addPolicy("/dev/urandom", "devread")

	reqPath := filepath.Join(ws.RootDir, ".tmp", "sandbox-request.json")
	helperReq := sandbox.ExecRequest{
		Command:       finalCommand,
		Dir:           ws.BoxDir,
		Env:           innerEnv,
		Limits:        req.Limits,
		EnableNetwork: req.EnableNetwork,
		Paths:         policies,
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

	cloneFlags := uintptr(syscall.CLONE_NEWUSER | syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS)
	if !req.EnableNetwork {
		cloneFlags |= syscall.CLONE_NEWNET
	}
	hostUID := os.Geteuid()
	hostGID := os.Getegid()
	if hostUID == 0 {
		hostUID = 65532
		hostGID = 65532
	}

	cmd := exec.CommandContext(ctx, helperPath)
	cmd.Dir = ws.BoxDir
	cmd.Stdin = strings.NewReader(req.Stdin)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Pdeathsig:  syscall.SIGKILL,
		Cloneflags: cloneFlags,
		Credential: &syscall.Credential{Uid: 65532, Gid: 65532},
		UidMappings: []syscall.SysProcIDMap{{
			ContainerID: 65532,
			HostID:      hostUID,
			Size:        1,
		}},
		GidMappings: []syscall.SysProcIDMap{{
			ContainerID: 65532,
			HostID:      hostGID,
			Size:        1,
		}},
		GidMappingsEnableSetgroups: false,
	}
	cmd.Env = append(append(baseEnv[:0:0], baseEnv...), sandbox.HelperModeEnv+"="+sandbox.HelperModeExec, sandbox.RequestPathEnv+"="+reqPath)

	var stdoutBuf, stderrBuf bytes.Buffer
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
	cpuSampler := timing.StartProcessCPUSampler(cmd.Process.Pid)
	cpuBaselineNs, _ := timing.ProcessCPUTimeNs(cmd.Process.Pid)

	result := execResult{Status: "OK"}
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waitCh
		result.Status = model.RunStatusTLE
	case err := <-waitCh:
		if err != nil {
			result.Status = model.RunStatusRE
		}
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)

	<-doneOut
	<-doneErr
	<-imageDone

	result.WallTimeMs = timing.SinceMillis(wallStart)
	cpuEndNs := cpuSampler.Stop()
	if cpuBaselineNs > 0 && cpuEndNs > cpuBaselineNs {
		result.CPUTimeMs = timing.MilliFromNanoseconds(cpuEndNs - cpuBaselineNs)
	} else {
		result.CPUTimeMs = timing.MilliFromNanoseconds(cpuEndNs)
	}
	result.Stdout = stdoutBuf.Bytes()
	result.Stderr = stderrBuf.Bytes()

	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
			if ws.Exited() {
				c := ws.ExitStatus()
				result.ExitCode = &c
			}
			if ws.Signaled() {
				if result.Status == "OK" {
					result.Status = model.RunStatusRE
				}
				if ws.Signal() == syscall.SIGKILL || ws.Signal() == syscall.SIGXCPU {
					result.Status = model.RunStatusTLE
				}
			}
		}
		if sysu, ok := ps.SysUsage().(*syscall.Rusage); ok {
			result.MemoryKB = sysu.Maxrss
		}
		if usageCPU := timing.MilliFromDuration(ps.UserTime() + ps.SystemTime()); usageCPU > result.CPUTimeMs {
			result.CPUTimeMs = usageCPU
		}
	}
	if result.ExitCode != nil && *result.ExitCode == 120 && bytes.Contains(result.Stderr, []byte("sandbox-init:")) {
		result.Status = model.RunStatusInitFail
		result.Reason = clipUTF8(result.Stderr, maxReturnBytes)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Status = model.RunStatusTLE
	}
	return result
}

func ioCopy(dst *bytes.Buffer, src any) (int64, error) {
	switch r := src.(type) {
	case *os.File:
		return dst.ReadFrom(r)
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
		full, err := existingWorkspacePath(ws, clean)
		if err != nil {
			return
		}
		st, err := os.Stat(full)
		if err != nil || st.Size() <= offset {
			return
		}
		f, err := os.Open(full)
		if err != nil {
			return
		}
		defer f.Close()
		_, _ = f.Seek(offset, 0)
		reader := bufio.NewReader(f)
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
	res := runCommandWithSandbox(ctx, ws, args, &model.RunRequest{Limits: req.Limits, EnableNetwork: false, Stdin: userStdout}, Hooks{})
	if res.Status == model.RunStatusTLE || res.Status == model.RunStatusMLE || res.Status == model.RunStatusInitFail {
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
	if base < 256 {
		base = 256
	}
	return uint64(base) * 1024 * 1024
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
