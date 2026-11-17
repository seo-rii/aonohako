package execute

import (
	"path/filepath"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func TestScala3RuntimeUsesOnlyTrustedRuntimeLibraries(t *testing.T) {
	req := &model.RunRequest{EntryPoint: "pkg/Main", Limits: model.Limits{MemoryMB: 1024}}
	args := buildCommand("/work/classes", "scala3", req)
	if len(args) == 0 || args[0] != "java" || args[len(args)-1] != "pkg.Main" {
		t.Fatalf("command = %v", args)
	}
	wantClasspath := "/work/classes:" + profiles.Scala3RuntimeClasspath
	found := false
	for i, arg := range args {
		if arg == "-cp" && i+1 < len(args) {
			found = true
			if args[i+1] != wantClasspath {
				t.Fatalf("classpath = %q, want %q", args[i+1], wantClasspath)
			}
		}
	}
	if !found {
		t.Fatal("missing classpath")
	}
	for _, arg := range args {
		if strings.Contains(arg, "compiler") || strings.Contains(arg, "/usr/share/java") {
			t.Fatalf("runtime includes compiler/legacy libraries: %v", args)
		}
	}
	if !isJVMRunLang("SCALA3") || !isTrustedJVMRuntime("scala3", "java") || isTrustedJVMRuntime("scala3", "scala3") {
		t.Fatal("Scala 3 JVM sandbox classification incorrect")
	}
}

func TestScala3MaterializesClassDirectoryAndValidatesEntryPoint(t *testing.T) {
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"scala3", "SCALA3"} {
		ws, err := prepareWorkspaceDirs(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		primary, normalized, err := materializeFiles(ws, &model.RunRequest{Lang: lang, EntryPoint: "pkg.Main", Binaries: []model.Binary{{Name: "pkg/Main.class", DataB64: b64("class")}}})
		if err != nil || primary != ws.BoxDir || normalized != "scala3" {
			t.Fatalf("materializeFiles = %q %q %v", primary, normalized, err)
		}
		if filepath.Base(primary) != "box" {
			t.Fatal(primary)
		}
	}
	for _, entry := range []string{"../Main", "Main;exit", "-Xmx100g"} {
		if _, _, err := materializeFiles(ws, &model.RunRequest{Lang: "scala3", EntryPoint: entry, Binaries: []model.Binary{{Name: "Main.class", DataB64: b64("class")}}}); err == nil {
			t.Fatalf("accepted invalid JVM entry %q", entry)
		}
	}
	if _, _, err := materializeFiles(ws, &model.RunRequest{Lang: "scala3", Binaries: []model.Binary{{Name: "Main.scala", DataB64: b64("source")}}}); err == nil {
		t.Fatal("accepted uncompiled Scala source")
	}
}
