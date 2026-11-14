package execute

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

const cpuPolicyRuntimeDir = "/usr/local/lib/aonohako/cpu-policy"
const cpuPolicyGuestBytes = uint64(1) << 32

type cpuFeatureExecution struct {
	command []string
	report  *os.File
}

// Only the supervisor can open this file by name. The plugin inherits its fd;
// its guest syscall policy denies access to every descriptor except stdio.
func (execution *cpuFeatureExecution) close() {
	if execution != nil {
		_ = execution.report.Close()
		_ = os.Remove(execution.report.Name())
	}
}

func prepareCPUFeatureExecution(ws Workspace, command []string, features []cpupolicy.Feature, reportFD int, runtimeDir string) (*cpuFeatureExecution, error) {
	if err := cpupolicy.Validate(features); err != nil {
		return nil, err
	}
	if len(features) == 0 {
		return nil, nil
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, fmt.Errorf("CPU feature enforcement requires a Linux x86-64 worker")
	}
	if len(command) != 1 || !filepath.IsAbs(command[0]) || reportFD < 3 {
		return nil, fmt.Errorf("CPU feature enforcement requires one native executable")
	}
	rel, err := filepath.Rel(ws.BoxDir, command[0])
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("CPU feature executable must be inside the submission workspace")
	}
	if err := validateCPUFeatureExecutable(command[0]); err != nil {
		return nil, err
	}
	emulator := filepath.Join(runtimeDir, "qemu-x86_64")
	plugin := filepath.Join(runtimeDir, "cpu-policy.so")
	for _, path := range []string{emulator, plugin} {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || path == emulator && info.Mode().Perm()&0o111 == 0 {
			return nil, fmt.Errorf("CPU feature enforcement backend is unavailable: %s", path)
		}
	}
	report, err := os.CreateTemp(ws.RootDir, ".cpu-policy-*")
	if err != nil {
		return nil, fmt.Errorf("create CPU feature enforcement report: %w", err)
	}
	pluginOption := plugin
	for _, feature := range features {
		pluginOption += "," + string(feature) + "=on"
	}
	pluginOption += ",report-fd=" + strconv.Itoa(reportFD)
	return &cpuFeatureExecution{
		command: []string{emulator, "-cpu", "qemu64", "-B", "0x10000000000", "-R", "4G", "-tb-size", "16", "-plugin", pluginOption, command[0]},
		report:  report,
	}, nil
}

func validateCPUFeatureExecutable(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("CPU feature enforcement requires a static ELF64 x86-64 executable: %w", err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 || file.Data != elf.ELFDATA2LSB || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return fmt.Errorf("CPU feature enforcement requires a static ELF64 x86-64 executable")
	}
	executableEntry := false
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			return fmt.Errorf("CPU feature enforcement does not support dynamically linked executables")
		}
		if program.Type != elf.PT_LOAD {
			continue
		}
		if program.Filesz > program.Memsz || program.Vaddr >= cpuPolicyGuestBytes || program.Memsz > cpuPolicyGuestBytes-program.Vaddr {
			return fmt.Errorf("CPU feature executable exceeds the 4 GiB guest address space")
		}
		if program.Flags&elf.PF_X != 0 && file.Entry >= program.Vaddr && file.Entry-program.Vaddr < program.Memsz {
			executableEntry = true
		}
	}
	if !executableEntry {
		return fmt.Errorf("CPU feature executable has no loadable entry point")
	}
	return nil
}

func (execution *cpuFeatureExecution) applyResult(result *execResult) {
	if execution == nil || result.Status == model.RunStatusTLE || result.Status == model.RunStatusMLE || result.Status == model.RunStatusWLE {
		return
	}
	// Some interactive paths close inherited files immediately after Start.
	// Reopen the supervisor-owned pathname rather than trust stderr/exit codes.
	file, err := os.Open(execution.report.Name())
	var report []byte
	if err == nil {
		report, err = io.ReadAll(io.LimitReader(file, 3))
		_ = file.Close()
	}
	if err == nil {
		switch string(report) {
		case "R":
			return
		case "RV":
			result.Status = model.RunStatusRE
			result.Reason = "CPU feature policy blocked an instruction"
			result.VerdictSource = "cpu_feature_policy"
			return
		case "RS":
			result.Status = model.RunStatusRE
			result.Reason = "CPU feature policy blocked a syscall"
			result.VerdictSource = "cpu_feature_policy"
			return
		}
	}
	result.Status = model.RunStatusInitFail
	result.Reason = "CPU feature enforcement did not initialize a valid trusted report"
	result.VerdictSource = "cpu_feature_policy"
}
