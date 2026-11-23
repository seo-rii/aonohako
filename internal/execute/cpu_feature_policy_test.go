package execute

import (
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"aonohako/internal/cpupolicy"
	"aonohako/internal/durante"
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
	put64(24, 0x401000) // e_entry
	put64(32, 64)       // e_phoff
	put16(52, 64)       // e_ehsize
	put16(54, 56)       // e_phentsize
	put16(56, 1)        // e_phnum
	put16(58, 64)       // e_shentsize
	put32(64, uint32(elf.PT_LOAD))
	put32(68, uint32(elf.PF_R|elf.PF_X))
	put64(72, 0)        // p_offset
	put64(80, 0x400000) // p_vaddr
	put64(88, 0x400000) // p_paddr
	put64(96, 0x1001)   // p_filesz
	put64(104, 0x1001)  // p_memsz
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
		{name: "PIE", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_DYN))
			binary.LittleEndian.PutUint64(data[24:], 0x1000)
			binary.LittleEndian.PutUint64(data[80:], 0)
			return data
		}},
		{name: "dynamic ELF interpreter", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[56:], 2)
			binary.LittleEndian.PutUint32(data[120:], uint32(elf.PT_INTERP))
			return data
		}},
		{name: "LOAD above 4 GiB", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[80:], 1<<32)
			binary.LittleEndian.PutUint64(data[24:], (1<<32)+0x1000)
			return data
		}},
		{name: "wrong machine", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[18:], uint16(elf.EM_AARCH64))
			return data
		}, want: "ELF64 x86-64"},
		{name: "relocatable object", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint16(data[16:], uint16(elf.ET_REL))
			return data
		}, want: "ELF64 x86-64"},
		{name: "malformed header", edit: func(data []byte) []byte { return data[:8] }, want: "ELF64 x86-64"},
		{name: "invalid magic", edit: func(data []byte) []byte { data[0] = 0; return data }, want: "ELF64 x86-64"},
		{name: "LOAD size overflows", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[104:], ^uint64(0))
			return data
		}, want: "invalid load segment"},
		{name: "file segment larger than memory segment", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[96:], 0x1002)
			return data
		}, want: "invalid load segment"},
		{name: "entry outside LOAD", edit: func(data []byte) []byte {
			binary.LittleEndian.PutUint64(data[24:], 0x401001)
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
	if err := prepareCPUFeatureExecution(Workspace{}, nil, nil, nil); err != nil {
		t.Fatalf("omitted policy preparation = %v", err)
	}
}

func TestPrepareCPUFeatureExecutionRejectsUnpreparedOrUnsupportedTarget(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("CPU feature backend requires Linux x86-64")
	}
	ws := Workspace{BoxDir: t.TempDir()}
	program := writeCPUFeatureELF(t, ws.BoxDir, cpuFeatureELFFixture())
	for _, tc := range []struct {
		name     string
		command  []string
		features []cpupolicy.Feature
		want     string
	}{
		{"unprepared session", []string{program, "argument"}, []cpupolicy.Feature{cpupolicy.AVX}, "prepared Durante session"},
		{"outside workspace", []string{writeCPUFeatureELF(t, t.TempDir(), cpuFeatureELFFixture())}, []cpupolicy.Feature{cpupolicy.AVX}, "inside the submission workspace"},
		{"relative executable", []string{"Main"}, []cpupolicy.Feature{cpupolicy.AVX}, "native executable"},
		{"missing executable", nil, []cpupolicy.Feature{cpupolicy.AVX}, "native executable"},
		{"unknown feature", []string{program}, []cpupolicy.Feature{"avx2"}, "unknown disallowed CPU feature"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := prepareCPUFeatureExecution(ws, tc.command, tc.features, nil); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("prepareCPUFeatureExecution() = %v, want %q", err, tc.want)
			}
		})
	}
}

// Exercise the collector's local startup receipt as well as the execution
// decision. A public or decoded report alone must never supply that receipt.
func cpuFeatureCollectedReport(t *testing.T, policy, terminal string) *durante.Report {
	t.Helper()
	base := map[string]any{
		"schema": 1, "client_version": 1, "authority": durante.Authority, "metric": durante.Metric,
		"pid": 10, "segment": "10-1", "seq": 1, "event": "start", "sdk_version": "11.3.0",
		"origin": "initial", "parent_pid": 0, "parent_segment": "", "rep_bytes_enabled": true,
	}
	if policy != "" {
		base["cpu_policy_version"], base["cpu_policy"] = 1, policy
	}
	first, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	data := append(first, '\n')
	if terminal != "" {
		delete(base, "cpu_policy_version")
		delete(base, "cpu_policy")
		base["seq"] = 2
		if terminal == "complete" {
			base["event"], base["instructions"], base["valid"] = "end", 100, true
			base["rep_bytes"], base["rep_entries"], base["unsupported_rep_entries"] = 0, 0, 0
			base["signals_observed"], base["accounting_error"] = 0, false
			base["threads_started"], base["threads_finished"], base["threads_complete"] = 1, 1, true
			base["app_exit_seen"], base["app_exit_code"], base["quality"] = true, 126, "uninterrupted"
		} else {
			base["event"], base["feature"] = "cpu_feature_violation", terminal
		}
		last, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, last...), '\n')
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "process-10.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	report := durante.CollectDirectory(dir, 10)
	return &report
}

func TestCPUFeatureExecutionRequiresMatchingNativeStartup(t *testing.T) {
	for _, tc := range []struct {
		name, policy, terminal, status, reason string
		features                               []cpupolicy.Feature
		initialStatus                          string
	}{
		{name: "normal exit 126", policy: "avx", terminal: "complete"},
		{name: "canonical request order", policy: "avx,simd", terminal: "complete", features: []cpupolicy.Feature{cpupolicy.SIMD, cpupolicy.AVX}},
		{name: "instruction violation", policy: "avx", terminal: "avx", status: model.RunStatusRE, reason: "blocked an instruction: avx"},
		{name: "signal after startup", policy: "avx", initialStatus: model.RunStatusRE},
		{name: "missing capability", terminal: "complete", status: model.RunStatusInitFail},
		{name: "wrong capability", policy: "simd", terminal: "complete", status: model.RunStatusInitFail},
		{name: "unexpected extra restriction", policy: "avx,simd", terminal: "complete", status: model.RunStatusInitFail},
		{name: "violation under wrong capability", policy: "avx,simd", terminal: "avx", status: model.RunStatusInitFail},
		{name: "incomplete successful execution", policy: "avx", status: model.RunStatusInitFail},
		{name: "fault without startup", initialStatus: model.RunStatusRE, status: model.RunStatusInitFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			features := tc.features
			if features == nil {
				features = []cpupolicy.Feature{cpupolicy.AVX}
			}
			exitCode := 126
			original := execResult{Status: "OK", ExitCode: &exitCode, Reason: "ordinary exit 126", VerdictSource: "exit_code", Stderr: []byte("disallowed CPU feature: avx\n")}
			if tc.initialStatus != "" {
				original.Status, original.ExitCode, original.VerdictSource = tc.initialStatus, nil, "signal"
			}
			result := original
			applyCPUFeatureResult(features, &result, cpuFeatureCollectedReport(t, tc.policy, tc.terminal))
			if tc.status == "" {
				if !reflect.DeepEqual(result, original) {
					t.Fatalf("ordinary execution changed: %+v", result)
				}
			} else if result.Status != tc.status || result.VerdictSource != "cpu_feature_policy" || !strings.Contains(result.Reason, tc.reason) {
				t.Fatalf("policy result = %+v, want status %q, reason %q", result, tc.status, tc.reason)
			}
		})
	}
	report := cpuFeatureCollectedReport(t, "avx", "complete")
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	var decoded durante.Report
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	result := execResult{Status: "OK"}
	applyCPUFeatureResult([]cpupolicy.Feature{cpupolicy.AVX}, &result, &decoded)
	if result.Status != model.RunStatusInitFail {
		t.Fatalf("remote metadata forged local startup: %+v", result)
	}
}

func TestCPUFeatureExecutionPreservesResourceLimitPriority(t *testing.T) {
	report := cpuFeatureCollectedReport(t, "avx", "avx")
	for _, status := range []string{model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusInitFail} {
		t.Run(status, func(t *testing.T) {
			original := execResult{Status: status, Reason: "preexisting sandbox failure", VerdictSource: "resource_limit"}
			for _, evidence := range []*durante.Report{report, nil} {
				result := original
				applyCPUFeatureResult([]cpupolicy.Feature{cpupolicy.AVX}, &result, evidence)
				if !reflect.DeepEqual(result, original) {
					t.Fatalf("policy overwrote existing verdict: %+v", result)
				}
			}
		})
	}
}

func TestCPUFeatureInteractiveMeasurementReusesCollectedReport(t *testing.T) {
	report := cpuFeatureCollectedReport(t, "avx", "complete")
	exitCode := 0
	result := execResult{Status: "OK", ExitCode: &exitCode, WorkMeasurement: report}
	// An unprepared session cannot collect anything; the existing report must
	// be reused, including its original local startup receipt.
	process := collectPeerWorkMeasurement(&durante.Session{}, result, false, "contestant", "contestant", nil)
	if process.Report != report || !process.Report.Complete || !report.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) {
		t.Fatalf("interactive policy report was collected again: %+v", process)
	}
	result.Status, result.VerdictSource = model.RunStatusRE, "cpu_feature_policy"
	process = collectPeerWorkMeasurement(&durante.Session{}, result, false, "contestant", "contestant", nil)
	if process.Report == report || !process.Report.ValidUnavailableResult() || process.Report.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) || process.TerminationSource != "cpu_feature_policy" {
		t.Fatalf("interactive failure retained usable evidence or lost attribution: %+v", process)
	}
	if !report.Complete || !report.HasCPUFeaturePolicy([]cpupolicy.Feature{cpupolicy.AVX}) {
		t.Fatal("stripping public diagnostic totals changed the original local report")
	}
}
