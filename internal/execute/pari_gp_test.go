package execute

import (
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/util"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPARIGPMaterializationSelectsGPSource(t *testing.T) {
	ws, err := prepareWorkspaceDirs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request := &model.RunRequest{Lang: "PARI_GP", Binaries: []model.Binary{
		{Name: "data.js", DataB64: util.EncodeB64([]byte("data"))},
		{Name: "A.gp", DataB64: util.EncodeB64([]byte("helper(x)=x+1;"))},
		{Name: "Main.gp", DataB64: util.EncodeB64([]byte("print(42);"))},
	}}
	got, lang, err := materializeFiles(ws, request)
	if err != nil || got != filepath.Join(ws.BoxDir, "Main.gp") || lang != "pari-gp" {
		t.Fatalf("materialized=%q %q error=%v", got, lang, err)
	}
}

func TestPARIGPRuntimeUsesFixedArguments(t *testing.T) {
	want := []string{"aonohako-pari-gp", "-f", "-q", "/work/box/Main.gp"}
	got := buildCommand("/work/box/Main.gp", "pari-gp", &model.RunRequest{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command=%v want=%v", got, want)
	}
	for _, key := range []string{"PARI_GP", "pari-gp"} {
		if got := profiles.NormalizeRunLang(key); got != "pari-gp" {
			t.Fatalf("NormalizeRunLang(%q)=%q", key, got)
		}
	}
}
