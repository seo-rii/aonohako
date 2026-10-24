package compile

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestRunUnlambdaValidatesSingleExpression(t *testing.T) {
	valid := "# greet\n`r```````````.H.e.l.l.o. .w.o.r.l.di\n"
	resp := New().Run(context.Background(), &model.CompileRequest{
		Lang:    "UNLAMBDA",
		Sources: []model.Source{{Name: "Main.unl", DataB64: b64String(valid)}},
	})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main.unl" {
		t.Fatalf("valid Unlambda response = %+v", resp)
	}
	resp = New().Run(context.Background(), &model.CompileRequest{
		Lang:    "UNLAMBDA",
		Sources: []model.Source{{Name: "Main.unl", DataB64: b64String("``.#i`S.\n# trailing comment\n")}},
	})
	if resp.Status != model.CompileStatusOK {
		t.Fatalf("literal comment and newline operands response = %+v", resp)
	}

	tests := []struct {
		name   string
		source string
		reason string
	}{
		{name: "empty", source: "# only a comment\n", reason: "before the expression is complete"},
		{name: "missing operand", source: "`i", reason: "before the expression is complete"},
		{name: "trailing", source: "`ii i", reason: "trailing input at byte 4"},
		{name: "dangling dot", source: "`i.", reason: "ends after"},
		{name: "unknown", source: "`ia", reason: "unknown character 'a'"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := New().Run(context.Background(), &model.CompileRequest{
				Lang:    "UNLAMBDA",
				Sources: []model.Source{{Name: "Main.unl", DataB64: b64String(tc.source)}},
			})
			if resp.Status != model.CompileStatusCompileError || !strings.Contains(resp.Reason, tc.reason) {
				t.Fatalf("response=%+v, want Compile Error containing %q", resp, tc.reason)
			}
		})
	}
}

func TestRunPietRequiresDecodableImageSignature(t *testing.T) {
	for _, src := range []model.Source{
		{Name: "Main.png", DataB64: b64String(string(pngSignature) + "rest")},
		{Name: "Main.ppm", DataB64: b64String("P3\n1 1\n255\n0 0 0\n")},
		{Name: "Main.PPM", DataB64: b64String("P6\n1 1\n255\n\x00\x00\x00")},
	} {
		resp := New().Run(context.Background(), &model.CompileRequest{Lang: "PIET", Sources: []model.Source{src}})
		if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 {
			t.Fatalf("Piet %s response = %+v", src.Name, resp)
		}
	}
	for _, src := range []model.Source{
		{Name: "Main.png", DataB64: b64String("GIF89a")},
		{Name: "Main.ppm", DataB64: b64String("P5\n1 1\n255\n\x00")},
	} {
		resp := New().Run(context.Background(), &model.CompileRequest{Lang: "PIET", Sources: []model.Source{src}})
		if resp.Status != model.CompileStatusCompileError || !strings.Contains(resp.Reason, "is not a") {
			t.Fatalf("Piet %s response = %+v, want signature Compile Error", src.Name, resp)
		}
	}
	resp := New().Run(context.Background(), &model.CompileRequest{Lang: "PIET", Sources: []model.Source{{Name: "Main.gif", DataB64: b64String("GIF89a")}}})
	if resp.Status != model.CompileStatusInvalid || resp.Reason != "no piet sources" {
		t.Fatalf("Piet GIF response = %+v, want no piet sources", resp)
	}
}

func TestIntercalCompilerBuildsTargetNamedCopyOfRootSource(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "solution.i"), []byte("DO GIVE UP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	normalized := filepath.Join(workDir, "Main.i")
	runner := &recordingCommandRunner{
		result: CommandResult{Status: model.CompileStatusOK},
		hook: func(_, _ string, args, _ []string) {
			source, err := os.ReadFile(args[len(args)-1])
			if err != nil || string(source) != "DO GIVE UP\n" {
				t.Errorf("normalized source = %q, %v", source, err)
			}
			if err := os.WriteFile(strings.TrimSuffix(args[len(args)-1], ".i"), []byte("binary"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
	}
	resp := intercalCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "solution.i"}}},
		Runner:  runner,
	})
	if resp.Status != model.CompileStatusOK || len(resp.Artifacts) != 1 || resp.Artifacts[0].Name != "Main" || resp.Artifacts[0].Mode != "exec" {
		t.Fatalf("INTERCAL response = %+v", resp)
	}
	if len(runner.commands) != 1 || runner.commands[0].bin != "ick" || !reflect.DeepEqual(runner.commands[0].args, []string{"-b", normalized}) || !reflect.DeepEqual(runner.commands[0].env, []string{"CC=gcc -std=gnu17"}) {
		t.Fatalf("INTERCAL command = %+v", runner.commands)
	}
	if _, err := os.Stat(normalized); !os.IsNotExist(err) {
		t.Fatalf("normalized INTERCAL source must be removed after compile, stat err = %v", err)
	}
}

func TestIntercalCompilerReportsPolitenessFailure(t *testing.T) {
	workDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workDir, "Main.i"), []byte("PLEASE GIVE UP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingCommandRunner{result: CommandResult{
		Status: model.CompileStatusCompileError,
		Stdout: "ICL099I\tPROGRAMMER IS OVERLY POLITE",
		Reason: "compiler exited with status 99",
	}}
	resp := intercalCompiler{}.Compile(context.Background(), CompileJob{
		WorkDir: workDir,
		Target:  "Main",
		Request: &model.CompileRequest{Sources: []model.Source{{Name: "Main.i"}}},
		Runner:  runner,
	})
	if resp.Status != model.CompileStatusCompileError || !strings.Contains(resp.Stdout, "OVERLY POLITE") || len(resp.Artifacts) != 0 {
		t.Fatalf("INTERCAL politeness response = %+v", resp)
	}
	if _, err := os.Stat(filepath.Join(workDir, "Main.i")); err != nil {
		t.Fatalf("submitted Main.i must be compiled in place: %v", err)
	}
}
