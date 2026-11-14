package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCPUFeatureQEMURejectsUnverifiedSource(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash unavailable")
	}
	root := t.TempDir()
	archive := filepath.Join(root, "unverified.tar.xz")
	if err := os.WriteFile(archive, []byte("unverified source"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "out")
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "build_cpu_policy_qemu.sh"), output)
	cmd.Env = append(os.Environ(), "TMPDIR="+root, "AONOHAKO_QEMU_SOURCE_ARCHIVE="+archive, "AONOHAKO_QEMU_BUILD_JOBS=1")
	if data, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("unverified QEMU archive was accepted: %s", data)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("failed verification published an output directory: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(root, "aonohako-qemu.*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("failed verification left build directories: %v, %v", leftovers, err)
	}
}

func TestCPUFeatureRuntimePackaging(t *testing.T) {
	buildScript, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build_cpu_policy_qemu.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"--enable-plugins", "--enable-capstone"} {
		if !strings.Contains(string(buildScript), required) {
			t.Errorf("CPU policy QEMU build omits required backend feature %q", required)
		}
	}
	for _, name := range []string{"Dockerfile", filepath.Join("docker", "runtime.Dockerfile")} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		for _, required := range []string{
			"FROM ${RUNTIME_BASE} AS cpu-policy-builder",
			"scripts/build_cpu_policy_qemu.sh",
			"scripts/build_cpu_policy_plugin.sh",
			"scripts/test_cpu_policy_runtime.py",
			"runtime/cpu-policy.c runtime/qemu-plugin-compat.h",
			"COPY runtime/qemu-10.0.15-guest-bounds.patch /src/runtime/",
			"/src/scripts/build_cpu_policy_plugin.sh /out/cpu-policy/cpu-policy.so",
			"python3 /src/scripts/test_cpu_policy_runtime.py /out/cpu-policy",
			"libc6-dev",
			"COPY --from=cpu-policy-builder /out/cpu-policy/ /usr/local/lib/aonohako/cpu-policy/",
			"libglib2.0-0t64",
			"libcapstone-dev",
			"libcapstone5",
		} {
			if !strings.Contains(body, required) {
				t.Errorf("%s omits CPU feature runtime dependency %q", name, required)
			}
		}
		if name == "Dockerfile" {
			_, python, ok := strings.Cut(body, "FROM ${PYTHON_IMAGE} AS execute-python\n")
			if !ok {
				t.Fatal("execute-python stage missing")
			}
			python, _, _ = strings.Cut(python, "\nFROM ")
			if !strings.Contains(python, "COPY --from=cpu-policy-builder") || !strings.Contains(python, "libglib2.0-0t64") {
				t.Error("execute-python bypasses the common base and must include the CPU feature runtime explicitly")
			}
		}
	}
}
