package runtimepacks

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWrenCatalogPinsCLIAndUsesSystemLibuv(t *testing.T) {
	catalog, err := LoadCatalog(filepath.Join("..", "..", "runtime-images.yml"))
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := catalog.Languages["wren"]
	if !ok || !slices.Contains(catalog.Profiles["type-a"].Languages, "wren") {
		t.Fatal("Wren missing from type-a catalog")
	}
	if !slices.Contains(spec.Install.Apt, "libuv1-dev") || !slices.Contains(spec.Install.SandboxTools, "wren_cli") {
		t.Fatalf("Wren install = %+v", spec.Install)
	}
	install := strings.Join(spec.Install.Script, "\n")
	for _, marker := range []string{"WREN_CLI_COMMIT=18553636618a4d33f10af9b5ab92da6431784a8c", "WREN_CLI_SHA256=4fdbb52bd5a3a5ecbedf20352378618755d2740e184aa441bc109cdd64c8f894", "sha256sum -c -", "deps/wren/src/vm/*.c", "-luv", "/usr/local/share/licenses/wren-cli/LICENSE", "/usr/local/share/licenses/wren-cli/Wren.LICENSE"} {
		if !strings.Contains(install, marker) {
			t.Fatalf("Wren installer missing %q", marker)
		}
	}
	if strings.Contains(install, "deps/libuv/src/") {
		t.Fatal("Wren must use the distribution's maintained libuv")
	}
	smoke := strings.Join(spec.Smoke.Command, "\n")
	for _, marker := range []string{"Stdin.readLine()", "addition.wren", "17 25", "7 13", "Broken.wren", "test ! -s broken.stdout"} {
		if !strings.Contains(smoke, marker) {
			t.Fatalf("Wren smoke missing %q", marker)
		}
	}
}
