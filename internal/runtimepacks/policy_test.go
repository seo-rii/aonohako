package runtimepacks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryPolicyIgnoresDependencyCacheButChecksSource(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash unavailable")
	}
	for _, withRipgrep := range []bool{false, true} {
		name := "grep fallback"
		if withRipgrep {
			name = "ripgrep"
		}
		t.Run(name, func(t *testing.T) {
			root, binDir := t.TempDir(), t.TempDir()
			tools := []string{"dirname", "grep"}
			if withRipgrep {
				tools = append(tools, "rg")
			}
			for _, tool := range tools {
				path, err := exec.LookPath(tool)
				if err != nil {
					t.Skipf("%s unavailable", tool)
				}
				if err := os.Symlink(path, filepath.Join(binDir, tool)); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, script := range []string{"check_repo_policy.sh", "check_dockerfile_bases.sh"} {
				body, err := os.ReadFile(filepath.Join("..", "..", "scripts", script))
				if err != nil {
					t.Fatal(err)
				}
				write(filepath.Join("scripts", script), string(body))
			}
			write("Dockerfile", "FROM scratch\n")
			write(filepath.Join("docker", "runtime.Dockerfile"), "FROM scratch\n")
			// Do not depend on a checkout's local or global ignore configuration.
			forbidden := "gcl" + "oud"
			payload := "// " + forbidden + " app deploy\n"
			write(filepath.Join(".cache", "go-mod", "example.org", "module", "doc.go"), payload)
			run := func() ([]byte, error) {
				cmd := exec.Command(bash, filepath.Join("scripts", "check_repo_policy.sh"))
				cmd.Dir = root
				cmd.Env = append(os.Environ(), "PATH="+binDir, "RIPGREP_CONFIG_PATH=")
				return cmd.CombinedOutput()
			}
			if out, err := run(); err != nil || !strings.Contains(string(out), "repository policy check passed") {
				t.Fatalf("dependency cache must be ignored: %v\n%s", err, out)
			}
			write(filepath.Join("src", "main.go"), payload)
			out, err := run()
			if err == nil || !strings.Contains(string(out), "repository policy violation") || !strings.Contains(string(out), filepath.Join("src", "main.go")) {
				t.Fatalf("real source must still be rejected: %v\n%s", err, out)
			}
			if strings.Contains(string(out), filepath.Join(".cache", "go-mod")) {
				t.Fatalf("source rejection unexpectedly scanned dependency cache: %s", out)
			}
		})
	}
}
