package execute

import (
	"context"
	"debug/elf"
	"encoding/base64"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

// These fixtures contain explicit x86-64 instructions so the root sandbox
// regression suite does not depend on a host compiler or its target defaults.
func cpuFeatureRunnableELF(code []byte) []byte {
	data := append(cpuFeatureELFFixture()[:0x1000], code...)
	binary.LittleEndian.PutUint64(data[96:], uint64(len(code)))
	binary.LittleEndian.PutUint64(data[104:], uint64(len(code)))
	return data
}

func cpuFeatureWriteAndExit(prefix []byte, message string, fd byte, exitCode byte) []byte {
	code := append([]byte(nil), prefix...)
	// write(fd, message, len(message)); exit(exitCode).
	code = append(code, 0xb8, 1, 0, 0, 0, 0xbf, fd, 0, 0, 0, 0x48, 0xbe)
	addressOffset := len(code)
	code = append(code, make([]byte, 8)...)
	code = append(code, 0xba, byte(len(message)), 0, 0, 0, 0x0f, 0x05,
		0xb8, 60, 0, 0, 0, 0xbf, exitCode, 0, 0, 0, 0x0f, 0x05)
	binary.LittleEndian.PutUint64(code[addressOffset:], 0x400000+uint64(len(code)))
	return cpuFeatureRunnableELF(append(code, []byte(message)...))
}

func TestRunCPUFeaturePolicyInRootSandbox(t *testing.T) {
	unavailable := func(reason string) {
		t.Helper()
		if os.Getenv("AONOHAKO_ENFORCE_SANDBOX_TESTS") != "" {
			t.Fatal(reason)
		}
		t.Skip(reason)
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		unavailable("CPU feature sandbox integration requires Linux x86-64 and root")
	}
	for _, name := range []string{"qemu-x86_64", "cpu-policy.so"} {
		if _, err := os.Stat(filepath.Join(cpuPolicyRuntimeDir, name)); err != nil {
			unavailable("CPU feature enforcement backend is unavailable: " + err.Error())
		}
	}
	requireSandboxSupport(t)

	const output = "scalar ok\n"
	scalar := cpuFeatureWriteAndExit([]byte{
		0x66, 0x0f, 0xef, 0xc0, // pxor xmm0,xmm0: permitted register clearing.
		0xf2, 0x0f, 0x58, 0xc0, // addsd xmm0,xmm0: permitted scalar arithmetic.
	}, output, 1, 0)
	packed := cpuFeatureWriteAndExit([]byte{0x66, 0x0f, 0xfe, 0xc0}, output, 1, 0) // paddd xmm0,xmm0.
	avx := cpuFeatureWriteAndExit([]byte{0xc5, 0xf8, 0x57, 0xc0}, output, 1, 0)    // vxorps xmm0,xmm0,xmm0.
	dynamic := append([]byte(nil), scalar...)
	binary.LittleEndian.PutUint16(dynamic[56:], 2)
	binary.LittleEndian.PutUint32(dynamic[120:], uint32(elf.PT_INTERP))

	for _, tc := range []struct {
		name         string
		program      []byte
		features     []cpupolicy.Feature
		wantStatus   string
		wantOutput   string
		wantReason   string
		policySource bool
		wantExit     int
	}{
		{name: "scalar-avx", program: scalar, features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusAccepted, wantOutput: output},
		{name: "scalar-simd", program: scalar, features: []cpupolicy.Feature{cpupolicy.SIMD}, wantStatus: model.RunStatusAccepted, wantOutput: output},
		{name: "avx-blocked", program: avx, features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusRE, wantReason: "blocked an instruction", policySource: true, wantExit: 126},
		{name: "simd-blocked", program: packed, features: []cpupolicy.Feature{cpupolicy.SIMD}, wantStatus: model.RunStatusRE, wantReason: "blocked an instruction", policySource: true, wantExit: 126},
		{name: "avx-allows-baseline-packed", program: packed, features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusAccepted, wantOutput: output},
		{name: "omitted-policy-native", program: packed, wantStatus: model.RunStatusAccepted, wantOutput: output},
		{name: "ordinary-exit-126", program: cpuFeatureWriteAndExit(nil, "", 1, 126), features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusRE, wantExit: 126},
		{name: "guest-cannot-write-report", program: cpuFeatureWriteAndExit(nil, "R", 6, 0), features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusRE, wantReason: "blocked a syscall", policySource: true, wantExit: 126},
		{name: "dynamic-rejected-before-start", program: dynamic, features: []cpupolicy.Feature{cpupolicy.AVX}, wantStatus: model.RunStatusInitFail, wantReason: "dynamically linked", policySource: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout strings.Builder
			response := New().Run(context.Background(), &model.RunRequest{
				Lang:                  "binary",
				Binaries:              []model.Binary{{Name: "Main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(tc.program)}},
				DisallowedCPUFeatures: tc.features,
				ExpectedStdout:        tc.wantOutput,
				Limits:                model.Limits{TimeMs: 5000, MemoryMB: 256},
			}, Hooks{OnLog: func(stream, message string) {
				if stream == "stdout" {
					stdout.WriteString(message)
				}
			}})
			if response.Status != tc.wantStatus || stdout.String() != tc.wantOutput || !strings.Contains(response.Reason, tc.wantReason) {
				t.Fatalf("CPU feature sandbox result = %+v; stdout = %q", response, stdout.String())
			}
			if (response.VerdictSource == "cpu_feature_policy") != tc.policySource {
				t.Fatalf("unexpected policy attribution: %+v", response)
			}
			if tc.wantStatus == model.RunStatusInitFail {
				if response.ExitCode != nil || response.CPUTimeNs != 0 || response.MemoryKB != 0 {
					t.Fatalf("unsupported ELF reached target execution: %+v", response)
				}
			} else if response.ExitCode == nil || *response.ExitCode != tc.wantExit {
				t.Fatalf("unexpected exit code: %+v", response)
			}
		})
	}

	t.Run("cpu-limit", func(t *testing.T) {
		response := New().Run(context.Background(), &model.RunRequest{
			Lang:                  "binary",
			Binaries:              []model.Binary{{Name: "Main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(cpuFeatureRunnableELF([]byte{0xeb, 0xfe}))}}, // jmp .
			DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX},
			Limits:                model.Limits{TimeMs: 100, WallTimeMs: 3000, MemoryMB: 256},
		}, Hooks{})
		if response.Status != model.RunStatusTLE || !strings.HasPrefix(response.VerdictSource, "cpu_") || response.CPUTimeNs == 0 {
			t.Fatalf("emulated CPU limit was not retained: %+v", response)
		}
	})

	t.Run("resident-memory-limit", func(t *testing.T) {
		// Touch one byte per page in a 256 MiB BSS, then loop. The guest's
		// reserved 4 GiB virtual window must not hide resident-memory growth.
		program := cpuFeatureRunnableELF([]byte{
			0xb8, 0, 0x10, 0x40, 0, // mov eax,0x401000
			0xb9, 0, 0, 1, 0, // mov ecx,65536
			0xc6, 0, 1, // mov byte ptr [rax],1
			0x48, 0x05, 0, 0x10, 0, 0, // add rax,4096
			0xff, 0xc9, 0x75, 0xf3, // dec ecx; jnz page loop
			0xeb, 0xfe, // jmp .
		})
		binary.LittleEndian.PutUint32(program[68:], uint32(elf.PF_R|elf.PF_W|elf.PF_X))
		binary.LittleEndian.PutUint64(program[104:], 257*1024*1024)
		response := New().Run(context.Background(), &model.RunRequest{
			Lang:                  "binary",
			Binaries:              []model.Binary{{Name: "Main", Mode: "exec", DataB64: base64.StdEncoding.EncodeToString(program)}},
			DisallowedCPUFeatures: []cpupolicy.Feature{cpupolicy.AVX},
			Limits:                model.Limits{TimeMs: 5000, MemoryMB: 128},
		}, Hooks{})
		if response.Status != model.RunStatusMLE || response.VerdictSource != "memory_rss" || response.MemoryKB < 128*1024 {
			t.Fatalf("emulated resident-memory limit was not retained: %+v", response)
		}
	})
}
