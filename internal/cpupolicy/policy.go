// Package cpupolicy defines the shared opt-in compile and execution CPU feature
// options and inspects C/C++ objects before linking. Runtime instruction checks
// are implemented separately by the QEMU plugin in runtime/cpu-policy.c; object
// inspection alone does not constrain linked libraries or dynamically reached
// code.
package cpupolicy

import (
	"fmt"
	"regexp"
	"strings"
)

type Feature string

const (
	SIMD Feature = "simd"
	AVX  Feature = "avx"

	MaxFeatures = 2
)

// Validate requires the canonical wire spellings and rejects duplicates rather
// than silently changing the caller's policy.
func Validate(features []Feature) error {
	if len(features) > MaxFeatures {
		return fmt.Errorf("too many disallowed CPU features: max %d", MaxFeatures)
	}
	seen := make(map[Feature]bool, len(features))
	for _, feature := range features {
		switch feature {
		case SIMD, AVX:
		default:
			return fmt.Errorf("unknown disallowed CPU feature %q; supported values are simd and avx", feature)
		}
		if seen[feature] {
			return fmt.Errorf("duplicate disallowed CPU feature %q", feature)
		}
		seen[feature] = true
	}
	return nil
}

// Flags sets a portable x86-64 baseline. Object inspection is still required:
// source-level target attributes, pragmas and assembly can override these flags.
// SIMD keeps SSE/SSE2 enabled for the scalar floating-point ABI and ordinary
// data copies, while disabling automatic loop and SLP vectorization.
func Flags(features []Feature) []string {
	if len(features) == 0 {
		return nil
	}
	flags := []string{
		"-march=x86-64", "-mtune=generic",
		"-mno-avx", "-mno-avx2", "-mno-avx512f",
		"-mno-fma", "-mno-fma4", "-mno-f16c", "-mno-xop",
	}
	for _, feature := range features {
		if feature == SIMD {
			flags = append(flags, "-fno-tree-loop-vectorize", "-fno-tree-slp-vectorize")
			break
		}
	}
	return flags
}

// ViolationError means inspection found an instruction prohibited by the
// requested policy. Other errors mean inspection could not establish a result.
type ViolationError struct {
	Feature     Feature
	Instruction string
}

func (e *ViolationError) Error() string {
	return fmt.Sprintf("disallowed CPU feature %q: %s", e.Feature, e.Instruction)
}

var (
	objectHeader = regexp.MustCompile(`^.+:\s+file format (\S+)$`)
	symbolLabel  = regexp.MustCompile(`^[0-9a-fA-F]+ <.*>:$`)
	instruction  = regexp.MustCompile(`^\s*([0-9a-fA-F]+):\s+(\S.*)$`)
	opcode       = regexp.MustCompile(`^[a-z][a-z0-9_.]*$`)
	vectorReg    = regexp.MustCompile(`%(xmm|ymm|zmm|mm|k|tmm)[0-9]+\b`)
	xmmReg       = regexp.MustCompile(`^%xmm[0-9]+$`)
	scalarCmp    = regexp.MustCompile(`^cmp(eq|lt|le|unord|neq|nlt|nle|ord)?s[sd]$`)
)

// CheckDisassembly reads the output of GNU objdump with
// -d -w -z --no-show-raw-insn -M att. Callers must reject truncated output before
// calling this function and run objdump against each submitted object before
// linking those same objects with trusted runtime libraries.
//
// Empty output, other architectures, undecoded bytes and unexpected output
// fail closed. A recognized object with no executable code is valid. SIMD is a
// conservative code policy: scalar SSE and ordinary data moves are allowed,
// but packed mask operations may also occur in scalar source (for example,
// floating-point sign changes) and are rejected.
func CheckDisassembly(text string, features []Feature) error {
	if err := Validate(features); err != nil {
		return err
	}
	var noSIMD, noAVX bool
	for _, feature := range features {
		noSIMD = noSIMD || feature == SIMD
		noAVX = noAVX || feature == AVX
	}
	var header, section bool
	for lineIndex, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		invalid := func() error {
			return fmt.Errorf("CPU feature inspection: unrecognized disassembly at line %d", lineIndex+1)
		}
		if match := objectHeader.FindStringSubmatch(line); match != nil {
			if header || match[1] != "elf64-x86-64" {
				return fmt.Errorf("CPU feature inspection requires one elf64-x86-64 object")
			}
			header = true
			continue
		}
		if !header {
			return invalid()
		}
		if strings.HasPrefix(line, "Disassembly of section ") && strings.HasSuffix(line, ":") {
			section = true
			continue
		}
		if !section {
			return invalid()
		}
		if symbolLabel.MatchString(line) {
			continue
		}
		match := instruction.FindStringSubmatch(raw)
		if match == nil {
			return invalid()
		}
		body := match[2]
		// Comments and symbol annotations are not instruction operands. In
		// particular, symbol names may contain strings resembling registers.
		if i := strings.IndexByte(body, '#'); i >= 0 {
			body = body[:i]
		}
		if i := strings.IndexByte(body, '<'); i >= 0 {
			body = body[:i]
		}
		body = strings.TrimSpace(body)
		fields := strings.Fields(body)
		for len(fields) > 0 && instructionPrefix(fields[0]) {
			fields = fields[1:]
		}
		if len(fields) == 0 || !opcode.MatchString(fields[0]) || rawByte(fields[0]) {
			return invalid()
		}
		mnemonic := fields[0]
		operands := strings.Join(fields[1:], " ")
		registers := vectorReg.FindAllStringSubmatch(operands, -1)
		avx := strings.HasPrefix(mnemonic, "v")
		switch mnemonic {
		// Segment checks and virtualization instructions share the v prefix,
		// but do not use AVX. Runtime privilege checks remain independent.
		case "verr", "verw", "vmcall", "vmclear", "vmfunc", "vmlaunch", "vmptrld", "vmptrst",
			"vmread", "vmresume", "vmwrite", "vmxoff", "vmxon", "vmrun", "vmmcall", "vmload", "vmsave":
			avx = false
		}
		for _, register := range registers {
			avx = avx || register[1] == "ymm" || register[1] == "zmm" || register[1] == "k"
		}
		if avx && (noAVX || noSIMD) {
			feature := SIMD
			if noAVX {
				feature = AVX
			}
			return &ViolationError{Feature: feature, Instruction: body}
		}
		if !noSIMD {
			continue
		}
		for _, register := range registers {
			if register[1] != "xmm" || !scalarXMMInstruction(mnemonic, operands) {
				return &ViolationError{Feature: SIMD, Instruction: body}
			}
		}
		switch mnemonic {
		case "emms", "femms", "ldtilecfg", "sttilecfg", "tilerelease":
			return &ViolationError{Feature: SIMD, Instruction: body}
		}
	}
	if !header {
		return fmt.Errorf("CPU feature inspection: missing elf64-x86-64 object header")
	}
	return nil
}

func instructionPrefix(s string) bool {
	switch s {
	case "lock", "rep", "repz", "repe", "repnz", "repne", "bnd", "notrack",
		"data16", "addr16", "addr32", "cs", "ds", "es", "fs", "gs", "ss":
		return true
	}
	return s == "rex" || strings.HasPrefix(s, "rex.")
}

func rawByte(s string) bool {
	if len(s) != 2 {
		return false
	}
	for _, c := range s {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

func scalarXMMInstruction(mnemonic, operands string) bool {
	switch mnemonic {
	case "movss", "movsd", "movd", "movq", "movaps", "movapd", "movups", "movupd", "movdqa", "movdqu",
		"addss", "addsd", "subss", "subsd", "mulss", "mulsd", "divss", "divsd",
		"minss", "minsd", "maxss", "maxsd", "sqrtss", "sqrtsd", "rsqrtss", "rcpss", "roundss", "roundsd",
		"comiss", "comisd", "ucomiss", "ucomisd", "cvtss2sd", "cvtsd2ss",
		"cvtsi2ss", "cvtsi2ssl", "cvtsi2ssq", "cvtsi2sd", "cvtsi2sdl", "cvtsi2sdq",
		"cvtss2si", "cvtss2sil", "cvtss2siq", "cvtsd2si", "cvtsd2sil", "cvtsd2siq",
		"cvttss2si", "cvttss2sil", "cvttss2siq", "cvttsd2si", "cvttsd2sil", "cvttsd2siq":
		return true
	case "pxor", "xorps", "xorpd":
		parts := strings.Split(operands, ",")
		if len(parts) != 2 {
			return false
		}
		left, right := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		return left == right && xmmReg.MatchString(left)
	}
	return scalarCmp.MatchString(mnemonic)
}
