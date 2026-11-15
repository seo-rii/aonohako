package execute

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

func TestWrenCommandRunsSelectedSourceWithoutREPL(t *testing.T) {
	for _, lang := range []string{"WREN", "wren", " Wren "} {
		profile, ok := profiles.Resolve(lang)
		if !ok || profile.Extension != "wren" || profile.CompileKind != "wren" || profile.RunLang != "wren" || profile.DefaultTarget != "Main.wren" {
			t.Fatalf("Resolve(%q) = %+v, %v", lang, profile, ok)
		}
		want := []string{"wren_cli", "/work/Main.wren"}
		if got := buildCommand("/work/Main.wren", profiles.NormalizeRunLang(lang), &model.RunRequest{}); !reflect.DeepEqual(got, want) {
			t.Fatalf("Wren command = %q, want %q", got, want)
		}
	}
}

func TestWrenSandboxExecutesInputAndFibers(t *testing.T) {
	if _, err := exec.LookPath("wren_cli"); err != nil {
		t.Skip("Wren CLI unavailable")
	}
	requireSandboxSupport(t)
	for _, tc := range []struct{ name, program, stdin, want string }{
		{"output", "System.print(42)\n", "", "42\n"},
		{"stdin", "import \"io\" for Stdin\nSystem.print(Stdin.readLine())\n", "hello\n", "hello\n"},
		{"fiber", "var f = Fiber.new {\nFiber.yield(20)\nreturn 22\n}\nSystem.print(f.call() + f.call())\n", "", "42\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := New().Run(context.Background(), &model.RunRequest{Lang: "WREN", Stdin: tc.stdin, ExpectedStdout: tc.want, Binaries: []model.Binary{{Name: "Main.wren", DataB64: util.EncodeB64([]byte(tc.program))}}, Limits: model.Limits{TimeMs: 5000, MemoryMB: 256}}, Hooks{})
			if resp.Status != model.RunStatusAccepted {
				t.Fatalf("Wren response = %+v", resp)
			}
		})
	}
}

func TestWrenMaterializationSelectsMainAndHonorsEntryPoint(t *testing.T) {
	for _, entry := range []string{"", "helper.wren"} {
		ws, err := prepareWorkspaceDirs(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		req := &model.RunRequest{Lang: "WREN", EntryPoint: entry, Binaries: []model.Binary{
			{Name: "data.txt", DataB64: util.EncodeB64([]byte("not a program"))},
			{Name: "helper.wren", DataB64: util.EncodeB64([]byte("class Helper {}"))},
			{Name: "Main.wren", DataB64: util.EncodeB64([]byte("System.print(42)"))},
		}}
		got, lang, err := materializeFiles(ws, req)
		if err != nil {
			t.Fatal(err)
		}
		want := "Main.wren"
		if entry != "" {
			want = entry
		}
		if got != filepath.Join(ws.BoxDir, want) || lang != "wren" {
			t.Fatalf("materializeFiles = %q, %q, want %q", got, lang, want)
		}
	}
}
