package runtimepacks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPietInstallAllowsFollowingSchemeGitClone(t *testing.T) {
	for _, tool := range []string{"bash", "git", "make", "install"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("installer regression needs %s", tool)
		}
	}
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var version, clone string
	for _, command := range catalog.Languages["piet"].Install.Script {
		if value, ok := strings.CutPrefix(command, "export NPIET_VERSION="); ok {
			version = value
		}
	}
	for _, command := range catalog.Languages["scheme"].Install.Script {
		if strings.HasPrefix(command, "git clone ") {
			clone = command
		}
	}
	if version == "" || clone == "" {
		t.Fatal("Piet version or following Scheme clone is missing")
	}
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	tmpDir := filepath.Join(root, "tmp")
	binDir := filepath.Join(root, "bin")
	buildDir := filepath.Join(tmpDir, "npiet-"+version)
	for _, path := range []string{workDir, binDir, buildDir} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{
		"configure": "#!/bin/sh\nexit 0\n",
		"Makefile":  "npiet:\n\t@test -x ./npiet\n",
		"npiet":     "#!/bin/sh\nprintf '%s\\n' 'npiet fixture (with PNG support)'\n",
	} {
		if err := os.WriteFile(filepath.Join(buildDir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	schemeSource := filepath.Join(root, "scheme-source")
	for _, args := range [][]string{
		{"init", "--quiet", schemeSource},
		{"-C", schemeSource, "-c", "user.name=Installer test", "-c", "user.email=installer@example.invalid", "-c", "commit.gpgsign=false", "commit", "--quiet", "--allow-empty", "-m", "fixture"},
		{"-C", schemeSource, "update-server-info"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("initialize local clone source: %v\n%s", err, output)
		}
	}
	// HTTP invokes Git's remote helper, which needs a valid inherited cwd.
	// Local path and file:// clones can bypass that helper and hide the bug.
	server := httptest.NewServer(http.FileServer(http.Dir(filepath.Join(schemeSource, ".git"))))
	defer server.Close()
	schemeDest := filepath.Join(root, "chibi-scheme")
	// The image builder concatenates language installers in one shell. Keep
	// real build/install/cleanup commands and the following installer's Git
	// clone; only fixture downloads and extraction are replaced.
	script := "curl() { :; }\nsha256sum() { cat >/dev/null; }\ntar() { :; }\n"
	script += strings.NewReplacer("/tmp", tmpDir, "/usr/local/bin", binDir).Replace(strings.Join(catalog.Languages["piet"].Install.Script, "\n"))
	script += "\n" + strings.NewReplacer(
		"https://github.com/ashinn/chibi-scheme", `"${AONOHAKO_SCHEME_SOURCE}"`,
		"/opt/chibi-scheme", `"${AONOHAKO_SCHEME_DEST}"`,
	).Replace(clone)
	script += "\ntest -d \"${AONOHAKO_SCHEME_DEST}/.git\"\n"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", script)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "PATH="+binDir+":"+os.Getenv("PATH"),
		"AONOHAKO_SCHEME_SOURCE="+server.URL, "AONOHAKO_SCHEME_DEST="+schemeDest)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("combined Piet and Scheme installation failed: %v\n%s", err, output)
	}
}
