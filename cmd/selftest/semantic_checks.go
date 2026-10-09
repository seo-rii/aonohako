package main

import (
	"fmt"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func languageSemanticCases() map[string][]languageSemanticCase {
	cases := nonABSemanticCases()
	cases["plain"] = []languageSemanticCase{{
		name:           "text artifact is printed verbatim",
		compileLang:    "TEXT",
		expectedStdout: "text artifact content\n",
		sources:        []model.Source{{Name: "Main.txt", DataB64: encodeScript("text artifact content\n")}},
	}}
	cases["golfscript"] = []languageSemanticCase{
		{
			name:           "compact arithmetic parses signed input",
			stdin:          "-17\t9\n",
			expectedStdout: "-8\n",
			sources:        []model.Source{{Name: "Main.gs", DataB64: encodeScript("~+")}},
		},
		{
			name:              "addition without evaluating stdin underflows the stack",
			stdin:             "1 2\n",
			expectedRunStatus: model.RunStatusRE,
			sources:           []model.Source{{Name: "Main.gs", DataB64: encodeScript("+")}},
		},
	}
	return cases
}

func verifyLanguageSemanticCase(baseURL, language, compileLanguage string, baseline compileExecuteCase, tc languageSemanticCase) error {
	if tc.compileLang != "" {
		compileLanguage = tc.compileLang
	}
	profile, ok := profiles.Resolve(compileLanguage)
	if !ok {
		return fmt.Errorf("%s/%s %s: unknown compile profile", language, compileLanguage, tc.name)
	}
	compiled, err := postCompileRequest(baseURL, model.CompileRequest{
		Lang:       compileLanguage,
		Sources:    tc.sources,
		EntryPoint: baseline.entryPoint,
	})
	if err != nil {
		return fmt.Errorf("%s/%s %s compile request: %w", language, compileLanguage, tc.name, err)
	}
	wantCompileStatus := tc.expectedCompileStatus
	if wantCompileStatus == "" {
		wantCompileStatus = model.CompileStatusOK
	}
	if compiled.Status != wantCompileStatus {
		return fmt.Errorf("%s/%s %s compile status=%s, want %s: reason=%s stdout=%q stderr=%q", language, compileLanguage, tc.name, compiled.Status, wantCompileStatus, compiled.Reason, compiled.Stdout, compiled.Stderr)
	}
	if wantCompileStatus != model.CompileStatusOK {
		if len(compiled.Artifacts) != 0 {
			return fmt.Errorf("%s/%s %s rejected compile returned %d artifacts", language, compileLanguage, tc.name, len(compiled.Artifacts))
		}
		return nil
	}
	if len(compiled.Artifacts) == 0 {
		return fmt.Errorf("%s/%s %s compile succeeded without artifacts", language, compileLanguage, tc.name)
	}
	binaries := make([]model.Binary, 0, len(compiled.Artifacts))
	for _, artifact := range compiled.Artifacts {
		binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
	}
	limits := baseline.limits
	if limits.TimeMs <= 0 {
		limits.TimeMs = 6000
	}
	if limits.MemoryMB <= 0 {
		limits.MemoryMB = 512
	}
	request := model.RunRequest{
		Lang:           profile.RunLang,
		Binaries:       binaries,
		EntryPoint:     baseline.entryPoint,
		Stdin:          tc.stdin,
		ExpectedStdout: tc.expectedStdout,
		Limits:         limits,
	}
	result, err := postExecuteRequest(baseURL, request)
	if err != nil {
		return fmt.Errorf("%s/%s %s execute request: %w", language, compileLanguage, tc.name, err)
	}
	wantRunStatus := tc.expectedRunStatus
	if wantRunStatus == "" {
		wantRunStatus = model.RunStatusAccepted
	}
	if result.Status != wantRunStatus {
		return fmt.Errorf("%s/%s %s execute status=%s, want %s: reason=%s stdout=%q stderr=%q", language, compileLanguage, tc.name, result.Status, wantRunStatus, result.Reason, result.Stdout, result.Stderr)
	}
	if wantRunStatus == model.RunStatusAccepted {
		request.ExpectedStdout += "aonohako-wrong-answer-control\n"
		control, err := postExecuteRequest(baseURL, request)
		if err != nil {
			return fmt.Errorf("%s/%s %s wrong-answer control request: %w", language, compileLanguage, tc.name, err)
		}
		if control.Status != model.RunStatusWA {
			return fmt.Errorf("%s/%s %s wrong-answer control returned status=%s, want %s", language, compileLanguage, tc.name, control.Status, model.RunStatusWA)
		}
	}
	return nil
}
