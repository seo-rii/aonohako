package runvalidation

import (
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestEntryExecutionContract(t *testing.T) {
	base := func() *model.RunRequest {
		return &model.RunRequest{
			Lang: "entry", EntryMode: "state", Binaries: []model.Binary{{Name: "Main.entry.json", DataB64: "e30="}},
			ExpectedStdout: `{"version":1,"objects":{"s":{"x":30}}}`, Limits: model.Limits{TimeMs: 1000, MemoryMB: 256},
		}
	}
	if err := Validate(base()); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*model.RunRequest){
		func(r *model.RunRequest) { r.EntryMode = "" }, func(r *model.RunRequest) { r.EntryMode = "other" },
		func(r *model.RunRequest) { r.Lang = "javascript" }, func(r *model.RunRequest) { r.EnableNetwork = true },
		func(r *model.RunRequest) { r.ExpectedStdout = `{"version":1}` },
		func(r *model.RunRequest) { r.Binaries[0].Name = "Main.js" }, func(r *model.RunRequest) { r.Binaries[0].Mode = "exec" },
		func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} }, func(r *model.RunRequest) { r.IgnoreTLE = true },
		func(r *model.RunRequest) { r.Stdin = strings.Repeat("x", 131073) },
	} {
		req := base()
		mutate(req)
		if err := ValidateEntry(req); err == nil {
			t.Fatalf("accepted invalid Entry request %+v", req)
		}
	}
	stdio := base()
	stdio.EntryMode = "stdio"
	stdio.ExpectedStdout = "hello\n"
	if err := Validate(stdio); err != nil {
		t.Fatal(err)
	}
}
