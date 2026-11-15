package execute

import (
	"path/filepath"
	"reflect"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
)

func TestPicatExecutionUsesFixedCommandAndSelectsMainSource(t *testing.T) {
	for _, lang := range []string{"PICAT", "picat", " Picat "} {
		if got := profiles.NormalizeRunLang(lang); got != "picat" {
			t.Fatalf("NormalizeRunLang(%q) = %q", lang, got)
		}
	}
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	req := &model.RunRequest{Lang: "PICAT", Binaries: []model.Binary{
		{Name: "data.txt", DataB64: util.EncodeB64([]byte("input"))},
		{Name: "helper.pi", DataB64: util.EncodeB64([]byte("helper = 42."))},
		{Name: "Main.pi", DataB64: util.EncodeB64([]byte("main => println(42)."))},
		{Name: "nested/Main.pi", DataB64: util.EncodeB64([]byte("main => println(0)."))},
	}}
	primary, lang, err := materializeFiles(ws, req)
	if err != nil || primary != filepath.Join(ws.BoxDir, "Main.pi") || lang != "picat" {
		t.Fatalf("materializeFiles = %q, %q, %v", primary, lang, err)
	}
	want := []string{"env", "PICATPATH=", "picat", primary}
	if got := buildCommand(primary, lang, req); !reflect.DeepEqual(got, want) {
		t.Fatalf("command = %q, want %q", got, want)
	}
}
