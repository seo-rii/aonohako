package execute

import (
	"context"
	"debug/elf"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func (s *Service) prepareContestantDurante(ctx context.Context, ws Workspace, req *model.RunRequest) (*durante.Session, error) {
	if req.WorkMeasurement {
		return durante.Prepare(ctx, ws.RootDir, s.workMeasurement, req.Lang, int(defaultSandboxUID), int(defaultSandboxGID))
	}
	if len(req.DisallowedCPUFeatures) != 0 {
		return durante.PrepareCPUFeaturePolicy(ctx, ws.RootDir, s.workMeasurement, int(defaultSandboxUID), int(defaultSandboxGID))
	}
	return nil, nil
}

func prepareCPUFeatureExecution(ws Workspace, command []string, features []cpupolicy.Feature, meter *durante.Session) error {
	if err := cpupolicy.Validate(features); err != nil {
		return err
	}
	if len(features) == 0 {
		return nil
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return fmt.Errorf("CPU feature enforcement requires a Linux x86-64 worker")
	}
	if len(command) == 0 || !filepath.IsAbs(command[0]) {
		return fmt.Errorf("CPU feature enforcement requires a native executable")
	}
	rel, err := filepath.Rel(ws.BoxDir, command[0])
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("CPU feature executable must be inside the submission workspace")
	}
	if err := validateCPUFeatureExecutable(command[0]); err != nil {
		return err
	}
	return meter.SetDisallowedCPUFeatures(features)
}

func validateCPUFeatureExecutable(path string) error {
	file, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("CPU feature enforcement requires an ELF64 x86-64 executable: %w", err)
	}
	defer file.Close()
	if file.Class != elf.ELFCLASS64 || file.Machine != elf.EM_X86_64 || file.Data != elf.ELFDATA2LSB || (file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN) {
		return fmt.Errorf("CPU feature enforcement requires an ELF64 x86-64 executable")
	}
	executableEntry := false
	for _, program := range file.Progs {
		if program.Type != elf.PT_LOAD {
			continue
		}
		if program.Filesz > program.Memsz || program.Memsz > math.MaxUint64-program.Vaddr {
			return fmt.Errorf("CPU feature executable has an invalid load segment")
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

// The native client and its lifecycle reports remain application-mutable. A
// matching report confirms the selected diagnostic policy, not tamper-proof
// enforcement. Only a lifecycle violation event selects this attribution;
// ordinary exit 126 and contestant stderr never do so.
func applyCPUFeatureResult(features []cpupolicy.Feature, result *execResult, report *durante.Report) {
	if len(features) == 0 || result.Status == model.RunStatusTLE || result.Status == model.RunStatusMLE || result.Status == model.RunStatusWLE || result.Status == model.RunStatusInitFail {
		return
	}
	if report != nil && report.ValidDiagnosticContract() && report.HasCPUFeaturePolicy(features) {
		for _, feature := range features {
			if report.Reason == "disallowed CPU feature: "+string(feature) {
				result.Status = model.RunStatusRE
				result.Reason = "CPU feature policy blocked an instruction: " + string(feature)
				result.VerdictSource = "cpu_feature_policy"
				return
			}
		}
		canonical := slices.Clone(features)
		slices.Sort(canonical)
		if report.Complete && slices.Equal(report.DisallowedCPUFeatures, canonical) {
			return
		}
		// A native fault after a validated startup is a contestant execution
		// failure even when the process could not finish its report.
		if result.Status == model.RunStatusRE {
			return
		}
	}
	result.Status = model.RunStatusInitFail
	result.Reason = "CPU feature enforcement did not produce a matching native policy report"
	result.VerdictSource = "cpu_feature_policy"
}

func collectCPUFeatureResult(features []cpupolicy.Feature, result *execResult, meter *durante.Session) *durante.Report {
	if len(features) == 0 || meter == nil {
		return nil
	}
	report := meter.Collect(result.targetPID)
	applyCPUFeatureResult(features, result, report)
	return report
}
