package cpupolicy

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	for _, features := range [][]Feature{nil, {}, {SIMD}, {AVX}, {SIMD, AVX}, {AVX, SIMD}} {
		if err := Validate(features); err != nil {
			t.Errorf("Validate(%v): %v", features, err)
		}
	}
	for _, features := range [][]Feature{{""}, {"AVX"}, {" avx"}, {"avx2"}, {"sse"}, {AVX, AVX}, {SIMD, SIMD}, {AVX, SIMD, AVX}} {
		if err := Validate(features); err == nil {
			t.Errorf("Validate(%v) unexpectedly succeeded", features)
		}
	}
}

func TestFlags(t *testing.T) {
	if flags := Flags(nil); flags != nil {
		t.Fatalf("default flags = %v, want no change", flags)
	}
	avx := Flags([]Feature{AVX})
	for _, flag := range []string{"-march=x86-64", "-mno-avx", "-mno-avx2", "-mno-avx512f", "-mno-fma", "-mno-fma4", "-mno-f16c", "-mno-xop"} {
		if !slices.Contains(avx, flag) {
			t.Errorf("AVX flags missing %s: %v", flag, avx)
		}
	}
	if slices.Contains(avx, "-fno-tree-loop-vectorize") || slices.Contains(avx, "-fno-tree-slp-vectorize") {
		t.Fatalf("AVX-only policy disables SSE vectorization: %v", avx)
	}
	simd := Flags([]Feature{SIMD})
	for _, flag := range append(avx, "-fno-tree-loop-vectorize", "-fno-tree-slp-vectorize") {
		if !slices.Contains(simd, flag) {
			t.Errorf("SIMD flags missing %s: %v", flag, simd)
		}
	}
	if !reflect.DeepEqual(simd, Flags([]Feature{SIMD, AVX})) || !reflect.DeepEqual(simd, Flags([]Feature{AVX, SIMD})) {
		t.Fatal("feature order or redundant AVX changes SIMD flags")
	}
	avx[0] = "changed"
	if Flags([]Feature{AVX})[0] == "changed" {
		t.Fatal("Flags returns shared mutable state")
	}
}

// Generated with GCC 12.2 and objdump -d -w --no-show-raw-insn -M att from
// double scalar(double a, double b) { return a + b; }
// void copy(long *a, const long *b) { a[0] = b[0]; }
const scalarFixture = `
scalar.o:     file format elf64-x86-64


Disassembly of section .text:

0000000000000000 <scalar>:
   0:	addsd  %xmm1,%xmm0
   4:	ret
   5:	data16 cs nopw 0x0(%rax,%rax,1)

0000000000000010 <copy>:
  10:	mov    (%rsi),%rax
  13:	mov    %rax,(%rdi)
  16:	ret
`

// Generated with GCC 12.2's assembler and the same objdump arguments.
const vectorFixture = `
simd.o:     file format elf64-x86-64


Disassembly of section .text:

0000000000000000 <vectors>:
   0:	vpaddd %ymm0,%ymm1,%ymm2
   4:	addps  %xmm0,%xmm1
   7:	ret
`

func disassembly(body string) string {
	return "submission.o:     file format elf64-x86-64\n\nDisassembly of section .text:\n\n0000000000000000 <main>:\n   0:\t" + body + "\n"
}

func TestCheckDisassemblyRealFixtures(t *testing.T) {
	for _, features := range [][]Feature{{AVX}, {SIMD}, {AVX, SIMD}} {
		if err := CheckDisassembly(scalarFixture, features); err != nil {
			t.Errorf("scalar fixture rejected for %v: %v", features, err)
		}
		var violation *ViolationError
		if err := CheckDisassembly(vectorFixture, features); !errors.As(err, &violation) {
			t.Errorf("vector fixture for %v: got %v, want policy violation", features, err)
		}
	}
	if err := CheckDisassembly(vectorFixture, nil); err != nil {
		t.Fatalf("disabled policy rejects vector fixture: %v", err)
	}
}

func TestCheckDisassemblyAllowsScalarABIAndDataMoves(t *testing.T) {
	for _, body := range []string{
		"movaps %xmm0,%xmm1", "movapd %xmm0,%xmm1", "movups %xmm0,(%rdi)",
		"movdqu (%rsi),%xmm0", "movdqa %xmm0,%xmm1", "movss %xmm0,(%rdi)",
		"movsd (%rsi),%xmm0", "movd %eax,%xmm0", "movq %xmm0,%rax",
		"addsd %xmm1,%xmm0", "mulss %xmm1,%xmm0", "sqrtsd %xmm1,%xmm0",
		"comisd %xmm1,%xmm0", "ucomiss %xmm1,%xmm0", "cmpeqss %xmm1,%xmm0",
		"cvtsi2sdq %rax,%xmm0", "cvttsd2si %xmm0,%eax", "cvtss2sd %xmm0,%xmm0",
		"xorps %xmm0,%xmm0", "xorpd %xmm3, %xmm3", "pxor %xmm15,%xmm15",
		"repz ret", "data16 cs nopw 0x0(%rax,%rax,1)", "endbr64",
		"verr %ax", "verw %ax", "andn %eax,%ebx,%ecx", "rorx $0x5,%eax,%edx",
		"vmcall", "vmrun %rax", "vmread %rax,%rbx", "vmxon (%rax)", "vmmcall",
	} {
		t.Run(body, func(t *testing.T) {
			if err := CheckDisassembly(disassembly(body), []Feature{SIMD}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCheckDisassemblyRejectsPackedSIMD(t *testing.T) {
	for _, body := range []string{
		"addps %xmm1,%xmm0", "addpd %xmm1,%xmm0", "paddd %xmm1,%xmm0",
		"pshufd $0x0,%xmm0,%xmm1", "punpcklqdq %xmm1,%xmm0",
		"pmaddwd %mm0,%mm1", "movq %mm0,%rax", "emms", "femms",
		"andpd 0x0(%rip),%xmm0", "xorps %xmm1,%xmm0", "pxor (%rdi),%xmm0",
		"movmskpd %xmm0,%eax", "tilezero %tmm0", "tilerelease",
		"vpaddd %ymm0,%ymm1,%ymm2", "vmovdqa64 (%rdi),%zmm0", "vzeroupper",
		"kmovw %eax,%k0", "kortestw %k0,%k1", "data16 vpxor %xmm0,%xmm0,%xmm0",
	} {
		t.Run(body, func(t *testing.T) {
			var violation *ViolationError
			err := CheckDisassembly(disassembly(body), []Feature{SIMD})
			if !errors.As(err, &violation) || violation.Feature != SIMD || violation.Instruction != body {
				t.Fatalf("got %v, want SIMD violation for %q", err, body)
			}
		})
	}
}

func TestCheckDisassemblyAVXLeavesLegacySIMDAllowed(t *testing.T) {
	for _, body := range []string{"addps %xmm0,%xmm1", "paddd %mm0,%mm1", "emms", "xorps %xmm0,%xmm1"} {
		if err := CheckDisassembly(disassembly(body), []Feature{AVX}); err != nil {
			t.Errorf("AVX policy rejected legacy instruction %q: %v", body, err)
		}
	}
	for _, body := range []string{"vaddsd %xmm0,%xmm1,%xmm2", "vzeroupper", "kmovw %eax,%k0", "vmovaps %zmm0,%zmm1"} {
		var violation *ViolationError
		err := CheckDisassembly(disassembly(body), []Feature{AVX, SIMD})
		if !errors.As(err, &violation) || violation.Feature != AVX {
			t.Errorf("got %v, want AVX violation for %q", err, body)
		}
	}
}

func TestCheckDisassemblyDoesNotClassifySymbolsAndComments(t *testing.T) {
	fixture := disassembly("call 5 <vaddps_%ymm0>") +
		"   5:\taddsd %xmm1,%xmm0 # addps %ymm0,%zmm0\n" +
		"0000000000000009 <vpaddd_%k0>:\n   9:\tret\n"
	if err := CheckDisassembly(fixture, []Feature{SIMD}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDisassemblyAllowsDataOnlyObject(t *testing.T) {
	if err := CheckDisassembly("data.o:     file format elf64-x86-64\n", []Feature{SIMD}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckDisassemblyFailsClosed(t *testing.T) {
	for name, fixture := range map[string]string{
		"empty":                  "",
		"whitespace":             " \n\t",
		"no header":              "Disassembly of section .text:\n   0:\tret\n",
		"wrong architecture":     strings.ReplaceAll(scalarFixture, "elf64-x86-64", "elf64-littleaarch64"),
		"32 bit object":          strings.ReplaceAll(scalarFixture, "elf64-x86-64", "elf32-i386"),
		"multiple objects":       scalarFixture + scalarFixture,
		"no section":             "main.o: file format elf64-x86-64\n   0:\tret\n",
		"bad instruction":        disassembly("(bad)"),
		"prefix bad instruction": disassembly("data16 (bad)"),
		"undecoded byte":         disassembly(".byte 0xc5"),
		"raw bytes":              disassembly("c5 fc 58 c1 vaddps %ymm1,%ymm0,%ymm0"),
		"missing mnemonic":       disassembly("rex.W"),
		"missing instruction":    disassembly(""),
		"skipped bytes":          scalarFixture + "\t...\n",
		"warning":                scalarFixture + "objdump: warning: truncated section\n",
	} {
		t.Run(name, func(t *testing.T) {
			err := CheckDisassembly(fixture, []Feature{SIMD})
			var violation *ViolationError
			if err == nil || errors.As(err, &violation) {
				t.Fatalf("got %v, want inspection failure", err)
			}
		})
	}
	if err := CheckDisassembly(scalarFixture, []Feature{"unknown"}); err == nil {
		t.Fatal("unknown feature accepted")
	}
}
