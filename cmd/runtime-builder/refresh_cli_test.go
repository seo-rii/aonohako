package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the compiled CLI and both Docker invocations without contacting a
// registry. The shell stub records argv; it does not simulate a Docker build.
func TestRuntimeRefreshReachesBothDockerInvocations(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "runtime-builder")
	build := exec.Command("go", "build", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build runtime-builder: %v\n%s", err, out)
	}
	catalog := filepath.Join(dir, "runtime-images.yml")
	body := "languages:\n  plain:\n    smoke:\n      command: [\"true\"]\nprofiles:\n  type-a:\n    base_image: debian:trixie-slim@sha256:" + strings.Repeat("a", 64) + "\n    languages: [plain]\n"
	if err := os.WriteFile(catalog, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\nprintf '%s\\0' \"$@\" >> \"$DOCKER_CAPTURE\"\nprintf '\\0' >> \"$DOCKER_CAPTURE\"\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		env      string
		flag     string
		refresh  bool
		separate bool
	}{
		{"ordinary", "", "", false, true},
		{"env-true", "true", "", true, true},
		{"env-one", "1", "", true, true},
		{"flag-true", "false", "-refresh", true, true},
		{"explicit-disable", "true", "-refresh=false", false, true},
		{"single-final", "true", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "argv")
			args := []string{"-catalog", catalog, "-only", "type-a", "-push",
				"-cache-from", "type=registry,ref=example.invalid/cache:type-a",
				"-cache-to", "type=registry,ref=example.invalid/cache:type-a,mode=min"}
			if tc.separate {
				args = append(args, "-cache-target", "runtime-toolchain")
			}
			if tc.flag != "" {
				args = append(args, tc.flag)
			}
			cmd := exec.Command(binary, args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"DOCKER_CAPTURE="+capture, "AONOHAKO_RUNTIME_REFRESH="+tc.env,
				"AONOHAKO_PYTHON_PACKAGES_CONTEXT=", "AONOHAKO_RUNTIME_BINARIES_CONTEXT=", "AONOHAKO_DOCKER_CACHE_TARGET=")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("runtime-builder: %v\n%s", err, out)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			commands := strings.Split(strings.TrimSuffix(string(data), "\x00\x00"), "\x00\x00")
			wantCount := 1
			if tc.separate {
				wantCount = 2
			}
			if len(commands) != wantCount {
				t.Fatalf("got %d Docker calls, want %d", len(commands), wantCount)
			}
			for i, command := range commands {
				argv := strings.Split(command, "\x00")
				joined := "\x00" + command + "\x00"
				filter := "\x00--no-cache-filter\x00runtime-foundation,runtime-toolchain\x00"
				if strings.Contains(joined, filter) != tc.refresh || strings.Contains(joined, "\x00--pull\x00") != tc.refresh {
					t.Errorf("Docker call %d has incorrect refresh flags: %q", i, argv)
				}
				if !strings.Contains(joined, "\x00--cache-from\x00type=registry,ref=example.invalid/cache:type-a\x00") {
					t.Errorf("Docker call %d lost the ordinary cache import: %q", i, argv)
				}
				if i == wantCount-1 && !strings.Contains(joined, "\x00--push\x00") {
					t.Errorf("last Docker call is not the final image push: %q", argv)
				}
			}
		})
	}
	cmd := exec.Command(binary, "-catalog", catalog)
	capture := filepath.Join(dir, "must-not-exist")
	cmd.Env = append(os.Environ(), "AONOHAKO_RUNTIME_REFRESH=tru", "DOCKER_CAPTURE="+capture,
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "AONOHAKO_RUNTIME_REFRESH must be a boolean") {
		t.Fatalf("invalid refresh environment must fail explicitly: %v\n%s", err, out)
	}
	if _, err := os.Stat(capture); !os.IsNotExist(err) {
		t.Fatal("invalid refresh reached Docker")
	}
}
