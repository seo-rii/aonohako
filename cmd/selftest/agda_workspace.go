package main

import (
	"fmt"
	"strings"

	"aonohako/internal/model"
)

// Exercise library discovery through the real compile endpoint, with the same
// traverse-only work root that smoke_runtime.sh and Cloud Run configure.
func runAgdaWorkspaceRegression(baseURL, rawLanguages string) error {
	enabled := false
	for _, language := range strings.Split(rawLanguages, ",") {
		if strings.TrimSpace(language) == "agda" {
			enabled = true
			break
		}
	}
	if !enabled {
		return nil
	}

	for _, tc := range []struct {
		name    string
		sources []model.Source
		status  string
	}{
		{
			name: "nested-import",
			sources: []model.Source{
				{Name: "Main.agda", DataB64: encodeScript("module Main where\nopen import Library.Helper\nok : Unit\nok = tt\n")},
				{Name: "Library/Helper.agda", DataB64: encodeScript("module Library.Helper where\ndata Unit : Set where\n  tt : Unit\n")},
			},
			status: model.CompileStatusOK,
		},
		{
			name: "submitted-library-root",
			sources: []model.Source{
				{Name: "project.agda-lib", DataB64: encodeScript("include: src\nflags: --safe\n")},
				{Name: "src/Main.agda", DataB64: encodeScript("module Main where\ndata Unit : Set where\n  tt : Unit\nok : Unit\nok = tt\n")},
			},
			status: model.CompileStatusOK,
		},
		{
			name: "false-proof",
			sources: []model.Source{
				{Name: "Main.agda", DataB64: encodeScript("module Main where\ndata Bool : Set where\n  true false : Bool\ndata IsTrue : Bool -> Set where\n  witness : IsTrue true\nbad : IsTrue false\nbad = witness\n")},
			},
			status: model.CompileStatusCompileError,
		},
	} {
		resp, err := postCompileRequest(baseURL, model.CompileRequest{Lang: "AGDA", Sources: tc.sources})
		if err != nil {
			return fmt.Errorf("agda/%s compile request failed: %w", tc.name, err)
		}
		if resp.Status != tc.status || (tc.status != model.CompileStatusOK && len(resp.Artifacts) != 0) {
			return fmt.Errorf("agda/%s compile returned status=%s reason=%s artifacts=%d stdout=%q stderr=%q", tc.name, resp.Status, resp.Reason, len(resp.Artifacts), resp.Stdout, resp.Stderr)
		}
	}
	return nil
}
