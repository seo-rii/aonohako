package runtimepacks

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPicatTrustedCompileHelperAndPinnedRuntime(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	spec := catalog.Languages["picat"]
	if !slices.Contains(spec.Install.SandboxTools, "picat") {
		t.Fatal("Picat missing from sandbox tools")
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, required := range []string{"PICAT_VERSION=39_12", "PICAT_SHA256=f053b11cccabfb69a3ccd779f2dc1c24cdf889805799bc8c779e6c56da703e4a", "sha256sum -c -", "/usr/local/share/licenses/picat/LICENSE", "Picat version 3.9#12"} {
		if !strings.Contains(install, required) {
			t.Fatalf("installer missing %q", required)
		}
	}
	helper, err := os.ReadFile(filepath.Join("..", "..", "scripts", "picat_compile.pi"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(helper), "main([File]) =>\n    compile(File).") {
		t.Fatal("Picat compile helper must compile argv source without loading or calling main")
	}
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "docker", "runtime.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(dockerfile), "COPY --chmod=0444 scripts/picat_compile.pi /usr/local/lib/aonohako/picat_compile.pi") {
		t.Fatal("Picat compile helper must be installed immutable outside submitted workspace")
	}
}
