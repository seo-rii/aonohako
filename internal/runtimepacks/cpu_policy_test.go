package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCPUFeatureRuntimePackagesFollowDistributionDependencies(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("POSIX shell unavailable")
	}
	// Native apt dependency graphs differ across supported RUNTIME_BASE images.
	// Keep unrelated dependencies so the helper must select runtime libraries,
	// rather than install the development packages or assume a distro version.
	const trixieGLib = `libglib2.0-dev
  Depends: girepository-tools
  Depends: libgio-2.0-dev
  Depends: libglib2.0-bin
  Depends: libglib2.0-dev-bin
libgio-2.0-dev
  Depends: libglib2.0-0t64
  Depends: libpcre2-dev
  Depends: zlib1g-dev
`
	const bookwormGLib = `libglib2.0-dev
  Depends: libglib2.0-0
  Depends: libglib2.0-bin
  Depends: libglib2.0-dev-bin
  Depends: libpcre2-dev
  Depends: zlib1g-dev
`
	const jammyGLib = `libglib2.0-dev
  Depends: libglib2.0-0
  Depends: libglib2.0-bin
  Depends: libglib2.0-dev-bin
  Depends: libpcre3-dev
  Depends: zlib1g-dev
`
	const trixieCapstone = "libcapstone-dev\n  Depends: libcapstone5\n"
	const jammyCapstone = "libcapstone-dev\n  Depends: libcapstone4\n"
	const nobleGLib = "libglib2.0-dev\n  Depends: libglib2.0-0t64\n  Depends: libglib2.0-dev-bin\n  Depends: zlib1g-dev\n"
	for _, tc := range []struct {
		name, glib, capstone, want string
		metadataFailure            bool
	}{
		{name: "Debian trixie", glib: trixieGLib, capstone: trixieCapstone, want: "libglib2.0-0t64 libcapstone5 zlib1g libatomic1"},
		{name: "Debian bookworm", glib: bookwormGLib, capstone: jammyCapstone, want: "libglib2.0-0 libcapstone4 zlib1g libatomic1"},
		{name: "Ubuntu jammy override", glib: jammyGLib, capstone: jammyCapstone, want: "libglib2.0-0 libcapstone4 zlib1g libatomic1"},
		{name: "Ubuntu noble override", glib: nobleGLib, capstone: jammyCapstone, want: "libglib2.0-0t64 libcapstone4 zlib1g libatomic1"},
		{name: "missing GLib runtime", glib: "libglib2.0-dev\n  Depends: zlib1g-dev\n", capstone: trixieCapstone},
		{name: "missing Capstone runtime", glib: trixieGLib, capstone: "libcapstone-dev\n  Depends: libc6-dev\n"},
		{name: "ambiguous Capstone runtime", glib: trixieGLib, capstone: "libcapstone-dev\n  Depends: libcapstone4\n  Depends: libcapstone5\n"},
		{name: "alternative Capstone runtimes", glib: trixieGLib, capstone: "libcapstone-dev\n |Depends: libcapstone4\n  Depends: libcapstone5\n"},
		{name: "ambiguous GLib runtime", glib: "libglib2.0-dev\n  Depends: libglib2.0-0\n  Depends: libglib2.0-0t64\n", capstone: trixieCapstone},
		{name: "failed apt metadata despite partial output", glib: trixieGLib, capstone: trixieCapstone, metadataFailure: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, body := range map[string]string{
				"libglib2.0-dev": tc.glib, "libcapstone-dev": tc.capstone,
				"apt-cache": `#!/bin/sh
if [ "$1" != depends ]; then exit 2; fi
recursive=0
for package do
  if [ "$package" = --recurse ]; then recursive=1; fi
done
case "$package" in libglib2.0-dev|libcapstone-dev) ;; *) exit 2 ;; esac
if [ "$recursive" = 1 ]; then
  cat "$AONOHAKO_APT_FIXTURES/$package"
else
  awk 'NR > 1 && /^[^[:space:]]/ { exit } { print }' "$AONOHAKO_APT_FIXTURES/$package"
fi
exit "${AONOHAKO_APT_CACHE_STATUS:-0}"
`,
			} {
				if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			status := "0"
			if tc.metadataFailure {
				status = "100"
			}
			cmd := exec.Command(sh, filepath.Join("..", "..", "scripts", "cpu_policy_runtime_packages.sh"))
			cmd.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "AONOHAKO_APT_FIXTURES="+root, "AONOHAKO_APT_CACHE_STATUS="+status)
			out, err := cmd.Output()
			if tc.want == "" {
				if err == nil {
					t.Fatalf("unusable runtime dependencies were accepted: %s", out)
				}
				return
			}
			if err != nil {
				t.Fatalf("runtime package selection failed: %v", err)
			}
			got, want := strings.Fields(string(out)), strings.Fields(tc.want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("runtime packages = %v, want %v", got, want)
			}
		})
	}
}

func TestCPUFeatureRuntimeImagesRejectMissingSharedLibraries(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler unavailable")
	}
	if _, err := exec.LookPath("ldd"); err != nil {
		t.Skip("ldd unavailable")
	}
	root := t.TempDir()
	for name, body := range map[string]string{
		"dependency.c": "int dependency(void) { return 0; }\n",
		"qemu.c":       "int dependency(void); int main(void) { return dependency(); }\n",
		"plugin.c":     "int dependency(void); int plugin_entry(void) { return dependency(); }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Tiny actual ELF dependencies exercise both loader failure and ldd's
	// successful exit when it reports a missing dependency in its output.
	for _, args := range [][]string{
		{"-shared", "-fPIC", "dependency.c", "-o", "libaonohako_qemu_fixture.so"},
		{"-shared", "-fPIC", "dependency.c", "-o", "libaonohako_plugin_fixture.so"},
		{"qemu.c", "-L.", "-laonohako_qemu_fixture", "-Wl,-rpath,$ORIGIN", "-o", "qemu-x86_64"},
		{"-shared", "-fPIC", "plugin.c", "-L.", "-laonohako_plugin_fixture", "-Wl,-rpath,$ORIGIN", "-o", "cpu-policy.so"},
	} {
		cmd := exec.Command(cc, args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compile loader fixture: %v\n%s", err, out)
		}
	}
	const backend = "/usr/local/lib/aonohako/cpu-policy/"
	for _, tc := range []struct {
		file  string
		count int
	}{{"Dockerfile", 2}, {filepath.Join("docker", "runtime.Dockerfile"), 1}} {
		data, err := os.ReadFile(filepath.Join("..", "..", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		var checks []string
		lines := strings.Split(string(data), "\n")
		for i := 0; i < len(lines); i++ {
			if !strings.HasPrefix(lines[i], "RUN "+backend+"qemu-x86_64 --version") {
				continue
			}
			command := strings.TrimPrefix(lines[i], "RUN ")
			for strings.HasSuffix(strings.TrimSpace(command), "\\") && i+1 < len(lines) {
				i++
				command = strings.TrimSuffix(strings.TrimSpace(command), "\\") + "\n" + lines[i]
			}
			checks = append(checks, strings.ReplaceAll(command, backend, `"$AONOHAKO_CPU_POLICY_FIXTURE"/`))
		}
		if len(checks) != tc.count {
			t.Fatalf("%s has %d backend loader checks, want %d", tc.file, len(checks), tc.count)
		}
		for _, command := range checks {
			for _, missing := range []string{"", "libaonohako_qemu_fixture.so", "libaonohako_plugin_fixture.so"} {
				if missing != "" {
					if err := os.Rename(filepath.Join(root, missing), filepath.Join(root, missing+".hidden")); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command("sh", "-c", command)
				cmd.Env = append(os.Environ(), "AONOHAKO_CPU_POLICY_FIXTURE="+root, "LD_LIBRARY_PATH="+root)
				out, runErr := cmd.CombinedOutput()
				if missing != "" {
					if err := os.Rename(filepath.Join(root, missing+".hidden"), filepath.Join(root, missing)); err != nil {
						t.Fatal(err)
					}
				}
				if missing == "" && runErr != nil || missing != "" && (runErr == nil || !strings.Contains(string(out), missing)) {
					t.Fatalf("%s loader check with missing=%q: %v\n%s", tc.file, missing, runErr, out)
				}
			}
		}
	}
}

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
			"libcapstone-dev",
			"scripts/cpu_policy_runtime_packages.sh",
			"runtime_packages=\"$(sh /tmp/cpu-policy-runtime-packages.sh)\" &&",
			"${runtime_packages}",
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
			if !strings.Contains(body, "FROM ${PYTHON_IMAGE} AS cpu-policy-python-builder\n") ||
				!strings.Contains(python, "COPY --from=cpu-policy-python-builder") ||
				!strings.Contains(python, "${runtime_packages}") {
				t.Error("execute-python must build and install CPU policy libraries for its own base image")
			}
		}
	}
}
