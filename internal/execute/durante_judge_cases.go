package execute

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runvalidation"
)

// NativeJudgeHeader tells the worker that its upstream already chose native
// judging. It leaves explicit diagnostic requests and native shadowing intact.
const NativeJudgeHeader = "X-Aonohako-Native-Judge"

// Preserve the ordinary request unless the explicit scoped path admits a copy
// using this deployment's raw watchdogs and effective runtime options.
func automaticJudgeRequest(req *model.RunRequest, cfg durante.Config, defaults config.RuntimeTuningConfig, named map[string]config.RuntimeTuningConfig) *model.RunRequest {
	if !cfg.AutoJudge || cfg.JudgeCases == nil || !cfg.Judge.VerdictsAllowed || !ordinaryMeasurementRequest(req) ||
		req.SkipAutoJudge || req.IgnoreTLE || req.ProblemID == "" {
		return req
	}
	tuning, err := resolveRuntimeTuning(req, defaults, named)
	if err != nil {
		return req
	}
	candidate := *req
	candidate.WorkMeasurement, candidate.WorkMeasurementTime, candidate.WorkMeasurementJudge = true, true, true
	candidate.JudgeCaseRequired = true
	admitted, err := resolveJudgeWorkLimits(&candidate, cfg, tuning)
	if err != nil {
		return req
	}
	return admitted
}

func validateJudgeCaseMode(req *model.RunRequest, cfg durante.Config) error {
	if req.JudgeCaseRequired && (cfg.JudgeCases == nil || !req.WorkMeasurementJudge || req.WorkMeasurementShadow) {
		return fmt.Errorf("work_measurement_judge_case_required requires a scoped deciding judge on this deployment")
	}
	if cfg.JudgeCases != nil && req.WorkMeasurementPeerTime != nil && req.WorkMeasurementPeerTime.Version == 2 {
		return fmt.Errorf("judge case policy does not authorize deciding peer clocks")
	}
	return nil
}

func validateJudgeCase(req *model.RunRequest, cases *durante.JudgeCases, clock *durante.WorkClockRequest) error {
	if err := runvalidation.ValidateWorkMeasurementLimits(req); err != nil {
		return err
	}
	profile, ok := profiles.Resolve(req.Lang)
	if !ok || profile.SourceLang != "CPP20" || req.EnableNetwork || len(req.FileOutputs) != 0 ||
		len(req.SidecarOutputs) != 0 || req.StdinURL != "" || req.ExpectedStdoutURL != "" || len(req.Binaries) != 1 ||
		req.Binaries[0].DataURL != "" || req.PythonLibraryMode != "" {
		return fmt.Errorf("judge case policy requires a single CPP20 executable with inline stdin and stdout judging")
	}
	if clock == nil || clock.TimePolicy == nil {
		return fmt.Errorf("judge case policy requires a deciding weighted online clock")
	}
	binding := clock.ModelBinding()
	digest := sha256.Sum256([]byte(req.Stdin))
	if !cases.Allows(req.ProblemID, profile.SourceLang, hex.EncodeToString(digest[:]), req.Limits.TimeMs, binding) {
		return fmt.Errorf("no judge case matches the problem, language, stdin, nominal time and effective clock model")
	}
	if req.JudgeClockBinding != "" && req.JudgeClockBinding != binding {
		return fmt.Errorf("work_measurement_judge_clock_binding does not match the effective judge model")
	}
	if !staticJudgeExecutable(req.Binaries[0].DataB64) {
		return fmt.Errorf("judge case policy requires an inline statically linked x86-64 ELF executable")
	}
	return nil
}

// Inspect only the file header and its bounded program table. Sections, symbols
// and other contestant-controlled metadata are irrelevant to the kernel loader.
func staticJudgeExecutable(encoded string) bool {
	if len(encoded) > base64.StdEncoding.EncodedLen(runvalidation.MaxBinaryFileBytes) {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > runvalidation.MaxBinaryFileBytes {
		return false
	}
	var header elf.Header64
	order := binary.LittleEndian
	headerSize, err := binary.Decode(data, order, &header)
	if err != nil || string(header.Ident[:4]) != elf.ELFMAG || header.Ident[elf.EI_CLASS] != byte(elf.ELFCLASS64) ||
		header.Ident[elf.EI_DATA] != byte(elf.ELFDATA2LSB) || header.Ident[elf.EI_VERSION] != byte(elf.EV_CURRENT) ||
		header.Version != uint32(elf.EV_CURRENT) || header.Machine != uint16(elf.EM_X86_64) || header.Type != uint16(elf.ET_EXEC) ||
		int(header.Ehsize) != headerSize || header.Phoff < uint64(headerSize) || header.Phoff > uint64(len(data)) {
		return false
	}
	programSize := binary.Size(elf.Prog64{})
	// Linux limits the entire ELF program header table to 64 KiB.
	if int(header.Phentsize) != programSize || header.Phnum == 0 || int(header.Phnum)*programSize > 65536 {
		return false
	}
	table := data[header.Phoff:]
	hasEntry := false
	for range header.Phnum {
		var program elf.Prog64
		size, err := binary.Decode(table, order, &program)
		if err != nil {
			return false
		}
		table = table[size:]
		switch elf.ProgType(program.Type) {
		case elf.PT_INTERP, elf.PT_DYNAMIC:
			return false
		case elf.PT_LOAD:
			if program.Filesz > program.Memsz || program.Off > uint64(len(data)) || program.Filesz > uint64(len(data))-program.Off {
				return false
			}
			if elf.ProgFlag(program.Flags)&elf.PF_X != 0 && header.Entry >= program.Vaddr && header.Entry-program.Vaddr < program.Filesz {
				hasEntry = true
			}
		}
	}
	return hasEntry
}
