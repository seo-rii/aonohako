package execute

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/model"
)

// A single executable LOAD containing ret, with no sections or dynamic loader.
// Keeping the fixture explicit avoids requiring a host compiler or libc.
func cpuFeatureELFFixture() []byte {
	data := make([]byte, 0x1001)
	copy(data, []byte{0x7f, 'E', 'L', 'F', byte(elf.ELFCLASS64), byte(elf.ELFDATA2LSB), byte(elf.EV_CURRENT)})
	put16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(data[offset:], value) }
	put32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(data[offset:], value) }
	put64 := func(offset int, value uint64) { binary.LittleEndian.PutUint64(data[offset:], value) }
	put16(16, uint16(elf.ET_EXEC))
	put16(18, uint16(elf.EM_X86_64))
	put32(20, uint32(elf.EV_CURRENT))
	put64(24, 0x400000) // e_entry
	put64(32, 64)       // e_phoff
	put16(52, 64)       // e_ehsize
	put16(54, 56)       // e_phentsize
	put16(56, 1)        // e_phnum
	put16(58, 64)       // e_shentsize
	put32(64, uint32(elf.PT_LOAD))
	put32(68, uint32(elf.PF_R|elf.PF_X))
	put64(72, 0x1000)   // p_offset
	put64(80, 0x400000) // p_vaddr
	put64(88, 0x400000) // p_paddr
	put64(96, 1)        // p_filesz
	put64(104, 1)       // p_memsz
	put64(112, 0x1000)  // p_align
	data[0x1000] = 0xc3
	return data
}

func writeCPUFeatureELF(t *testing.T, dir string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, "Main")
	if err := os.WriteFile(path, data, 0o555); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateCPUFeatureExecutable(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]byte) []byte
		want string
	}{
		{name: "static executable"},
		{name: "static PIE", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_DYN))
			binary.LittleEndian.PutUint64(data[24:], 0x1000)
			binary.LittleEndian.PutUint64(data[80:], 0x1000)
			return data
		}},
		{name: "ELF interpreter", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[56:], 2)
			binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_INTERP))
			return data
		}, want: "dynamically linked"},
		{name: "wrong machine", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_AARCH64))
			return data
		}, want: "static ELF64 x86-64"},
		{name: "relocatable object", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_REL))
			return data
		}, want: "static ELF64 x86-64"},
		{name: "malformed header", edit: func(data []byte) []byte { return data[:8] }, want: "static ELF64 x86-64"},
		{name: "invalid magic", edit: func(data []byte) []byte { data[0] = 0; return data }, want: "static ELF64 x86-64"},
		{name: "LOAD starts outside guest window", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[80:], 1<<32)
			return data
		}, want: "4 GiB"},
		{name: "LOAD crosses guest boundary", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[80:], (1<<32)-1)
			binary.LittleEndian.PutUint64(data[104:], 2)
			return data
		}, want: "4 GiB"},
		{name: "LOAD size overflows", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[104:], ^uint64(0))
			return data
		}, want: "4 GiB"},
		{name: "file segment larger than memory segment", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[96:], 2)
			return data
		}, want: "4 GiB"},
		{name: "entry outside LOAD", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[24:], 0x400001)
			return data
		}, want: "loadable entry point"},
		{name: "entry inside non-executable LOAD", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint32(data[68:], uint32(elf.PF_R))
			return data
		}, want: "loadable entry point"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := cpuFeatureELFFixture()
			if tc.edit != nil {
				data = tc.edit(data)
			}
			err := validateCPUFeatureExecutable(writeCPUFeatureELF(t, t.TempDir(), data))
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("validateCPUFeatureExecutable() = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPrepareCPUFeatureExecutionWithoutPolicyNeedsNoBackend(t *testing.T) {
	execution, err := prepareCPUFeatureExecution(Workspace{}, nil, nil, 0, "/missing/backend")
	if err != nil || execution != nil {
		t.Fatalf("omitted policy preparation = %v, %v", execution, err)
	}
}

func TestPrepareCPUFeatureExecutionProtectsReportAndBindsBackend(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("CPU feature backend requires Linux x86-64")
	}
	root := t.TempDir()
	ws := Workspace{RootDir: root, BoxDir: filepath.Join(root, "box")}
	if err := os.Mkdir(ws.BoxDir, 0o755); err != nil {
		t.Fatal(err)
	}
	program := writeCPUFeatureELF(t, ws.BoxDir, cpuFeatureELFFixture())
	backend := t.TempDir()
	emulator, plugin := filepath.Join(backend, "qemu-x86_64"), filepath.Join(backend, "cpu-policy.so")
	for _, path := range []string{emulator, plugin} {
		if err := os.WriteFile(path, []byte("trusted fixture"), 0o555); err != nil {
			t.Fatal(err)
		}
	}
	execution, err := prepareCPUFeatureExecution(ws, []string{program}, []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}, 9, backend)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(execution.close)
	if execution.command[0] != emulator || execution.command[len(execution.command)-1] != program {
		t.Fatalf("execution is not bound to trusted emulator and submitted program: %v", execution.command)
	}
	for flag, want := range map[string]string{
		"-cpu": "qemu64", "-B": "0x10000000000", "-R": "4G", "-tb-size": "16",
		"-plugin": plugin + ",simd=on,avx=on,report-fd=9",
	} {
		index := slices.Index(execution.command, flag)
		if index < 0 || index+1 >= len(execution.command) || execution.command[index+1] != want {
			t.Fatalf("command %v does not bind %s to %q", execution.command, flag, want)
		}
	}
	reportPath := execution.report.Name()
	info, err := os.Lstat(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(reportPath) != ws.RootDir || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 0 {
		t.Fatalf("report is not empty and private outside box: path=%s mode=%v size=%d", reportPath, info.Mode(), info.Size())
	}
	execution.close()
	if _, err := os.Stat(reportPath); !os.IsNotExist(err) {
		t.Fatalf("report survived cleanup: %v", err)
	}
	if err := os.Chmod(plugin, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareCPUFeatureExecution(ws, []string{program}, []cpupolicy.Feature{cpupolicy.AVX}, 9, backend); err == nil {
		t.Fatal("writable backend plugin was accepted")
	}
	if _, err := prepareCPUFeatureExecution(ws, []string{program}, []cpupolicy.Feature{cpupolicy.AVX}, 9, filepath.Join(backend, "missing")); err == nil {
		t.Fatal("missing backend was accepted")
	}
	if _, err := prepareCPUFeatureExecution(ws, []string{program, "argument"}, []cpupolicy.Feature{cpupolicy.AVX}, 9, backend); err == nil {
		t.Fatal("unexpected native command arguments were accepted")
	}
}

func TestCPUFeatureExecutionUsesTrustedReport(t *testing.T) {
	for _, tc := range []struct {
		name, report, status, reason string
		remove, closeFD              bool
	}{
		{name: "ready preserves ordinary exit 126", report: "R"},
		{name: "ready with inherited descriptor already closed", report: "R", closeFD: true},
		{name: "instruction violation", report: "RV", status: model.RunStatusRE, reason: "blocked an instruction"},
		{name: "syscall violation", report: "RS", status: model.RunStatusRE, reason: "blocked a syscall"},
		{name: "missing", remove: true, status: model.RunStatusInitFail},
		{name: "empty", status: model.RunStatusInitFail},
		{name: "violation without ready", report: "V", status: model.RunStatusInitFail},
		{name: "malformed", report: "RX", status: model.RunStatusInitFail},
		{name: "oversized", report: "RVV", status: model.RunStatusInitFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := os.CreateTemp(t.TempDir(), "report-*")
			if err != nil {
				t.Fatal(err)
			}
			execution := &cpuFeatureExecution{report: report}
			t.Cleanup(execution.close)
			if _, err := report.WriteString(tc.report); err != nil {
				t.Fatal(err)
			}
			if tc.closeFD {
				if err := report.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.remove {
				if err := os.Remove(report.Name()); err != nil {
					t.Fatal(err)
				}
			}
			exitCode := 126
			original := execResult{Status: model.RunStatusRE, ExitCode: &exitCode, Reason: "ordinary exit 126", VerdictSource: "exit_code", Stderr: []byte("cpu-policy: disallowed simd instruction\n")}
			result := original
			execution.applyResult(&result)
			if tc.status == "" {
				if !reflect.DeepEqual(result, original) {
					t.Fatalf("stderr or exit code forged a policy violation: %+v", result)
				}
			} else if result.Status != tc.status || result.VerdictSource != "cpu_feature_policy" || !strings.Contains(result.Reason, tc.reason) {
				t.Fatalf("trusted report result = %+v, want status %q, reason %q", result, tc.status, tc.reason)
			}
		})
	}
}

func TestCPUFeatureExecutionPreservesResourceLimitPriority(t *testing.T) {
	for _, status := range []string{model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE} {
		t.Run(status, func(t *testing.T) {
			report, err := os.CreateTemp(t.TempDir(), "report-*")
			if err != nil {
				t.Fatal(err)
			}
			execution := &cpuFeatureExecution{report: report}
			t.Cleanup(execution.close)
			if _, err := report.WriteString("RV"); err != nil {
				t.Fatal(err)
			}
			original := execResult{Status: status, Reason: "resource limit exceeded", VerdictSource: "resource_limit"}
			result := original
			execution.applyResult(&result)
			if !reflect.DeepEqual(result, original) {
				t.Fatalf("policy overwrote resource verdict: %+v", result)
			}
			if err := os.Remove(report.Name()); err != nil {
				t.Fatal(err)
			}
			execution.applyResult(&result)
			if !reflect.DeepEqual(result, original) {
				t.Fatalf("missing report overwrote resource verdict: %+v", result)
			}
		})
	}
}
