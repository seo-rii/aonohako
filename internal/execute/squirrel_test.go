package execute

import (
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"reflect"
	"testing"
)

func TestSquirrelRuntimeUsesFixedArguments(t *testing.T) {
	want := []string{"aonohako-squirrel", "/work/box/Main.cnut"}
	got := buildCommand("/work/box/Main.cnut", "squirrel", &model.RunRequest{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command=%v want=%v", got, want)
	}
	for _, key := range []string{"SQUIRREL", "squirrel"} {
		if got := profiles.NormalizeRunLang(key); got != "squirrel" {
			t.Fatalf("NormalizeRunLang(%q)=%q", key, got)
		}
	}
}
