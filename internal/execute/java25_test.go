package execute

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func TestJava25CommandUsesDedicatedRuntimeAndJavaResourceLimits(t *testing.T) {
	for _, memoryMB := range []int{64, 512, 2048} {
		req := &model.RunRequest{Limits: model.Limits{MemoryMB: memoryMB}}
		tuning := config.DefaultRuntimeTuningConfig()
		tuning.JavaStackSizeKB = 8192
		legacy := buildCommandWithRuntimeTuning("/tmp/Main.jar", "java", req, tuning)
		for i, arg := range legacy {
			if arg == "-XX:-UseCompressedClassPointers" {
				legacy[i] = "-XX:CompressedClassSpaceSize=64m"
			}
		}
		args := buildCommandWithRuntimeTuning("/tmp/Main.jar", "java25", req, tuning)
		if len(args) == 0 || args[0] != filepath.Join(profiles.Java25Home, "bin", "java") {
			t.Fatalf("Java 25 command = %v", args)
		}
		if !reflect.DeepEqual(args[1:], legacy[1:]) {
			t.Fatalf("Java 25 resource flags differ from Java: %v versus %v", args, legacy)
		}
		for _, flag := range []string{"-Xss8192k", "-XX:+UseSerialGC", "-XX:CompressedClassSpaceSize=64m", "-DONLINE_JUDGE=1", "-jar"} {
			if !containsArg(args, flag) {
				t.Errorf("Java 25 command missing %s: %v", flag, args)
			}
		}
		if containsArg(args, "-XX:-UseCompressedClassPointers") {
			t.Fatalf("Java 25 disables compressed class pointers and emits CDS startup warnings: %v", args)
		}
	}
}

func TestJava25MaterializesClassesIntoSubmissionJar(t *testing.T) {
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	primary, lang, err := materializeFiles(ws, &model.RunRequest{
		Lang:       "JAVA25",
		EntryPoint: "pkg/Main",
		Binaries: []model.Binary{
			{Name: "pkg/Main.class", DataB64: b64("main class")},
			{Name: "pkg/Helper.class", DataB64: b64("helper class")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lang != "java25" || filepath.Dir(primary) != ws.BoxDir {
		t.Fatalf("materializeFiles = (%q, %q), want Java 25 JAR under %s", primary, lang, ws.BoxDir)
	}
	jar, err := zip.OpenReader(primary)
	if err != nil {
		t.Fatal(err)
	}
	defer jar.Close()
	want := map[string]string{
		"META-INF/MANIFEST.MF": "Manifest-Version: 1.0\r\nMain-Class: pkg.Main\r\n\r\n",
		"pkg/Main.class":       "main class",
		"pkg/Helper.class":     "helper class",
	}
	if len(jar.File) != len(want) {
		t.Fatalf("JAR contains %d entries, want %d", len(jar.File), len(want))
	}
	for _, file := range jar.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		closeErr := r.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read JAR entry %s: %v, close: %v", file.Name, err, closeErr)
		}
		if expected, ok := want[file.Name]; !ok || string(body) != expected {
			t.Fatalf("unexpected JAR entry %s = %q", file.Name, body)
		}
	}
}

func TestJava25MaterializesSubmittedJarAndRejectsUnsafeMainClass(t *testing.T) {
	for _, entryPoint := range []string{"Main", "Main\r\nClass-Path: /tmp/evil", "../Main", "Main;echo"} {
		t.Run(entryPoint, func(t *testing.T) {
			ws, err := prepareWorkspaceDirs(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			primary, lang, err := materializeFiles(ws, &model.RunRequest{
				Lang:       "java25",
				EntryPoint: entryPoint,
				Binaries:   []model.Binary{{Name: "Main.jar", DataB64: b64("submitted jar")}},
			})
			if entryPoint != "Main" {
				if err == nil || !strings.Contains(err.Error(), "entry_point") {
					t.Fatalf("unsafe entry point %q produced error %v", entryPoint, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if lang != "java25" || primary != filepath.Join(ws.BoxDir, "Main.jar") {
				t.Fatalf("materialized JAR = (%q, %q)", primary, lang)
			}
			body, err := os.ReadFile(primary)
			if err != nil || string(body) != "submitted jar" {
				t.Fatalf("submitted JAR bytes = %q, error %v", body, err)
			}
		})
	}
}

func TestJava25RetainsTrustedJVMAndPhysicalMemoryPolicy(t *testing.T) {
	for _, lang := range []string{"java25", "JAVA25"} {
		if !isJVMRunLang(lang) || !isTrustedJVMRuntime(lang, "java") {
			t.Errorf("Java 25 does not select trusted JVM policy: %s", lang)
		}
		if isTrustedJVMRuntime(lang, "") || isTrustedJVMRuntime(lang, "java25") {
			t.Errorf("Java 25 grants JVM policy to an unrecognized executable: %s", lang)
		}
		if addressSpaceProximityCanClassifyMLE("", lang) {
			t.Errorf("Java 25 uses virtual address reservations to classify memory limits: %s", lang)
		}
	}
}
