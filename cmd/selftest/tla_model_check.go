package main

import (
	"fmt"
	"strings"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

// Use a transition to a second state: an invariant that only holds initially
// must fail after TLC starts exploring the model inside the judge sandbox.
func verifyTLAModelChecking(baseURL, compileLanguage string) error {
	profile, ok := profiles.Resolve(compileLanguage)
	if !ok || profile.RunLang != "tla" {
		return fmt.Errorf("TLA model-check regression has invalid profile %q", compileLanguage)
	}
	const specification = `---- MODULE Main ----
VARIABLE x
Init == x = 0
Next == x' = IF x = 0 THEN 1 ELSE x
Spec == Init /\ [][Next]_x
Safe == x \in {0, 1}
InitialOnly == x = 0
====
`
	for _, tc := range []struct {
		invariant string
		status    string
	}{
		{invariant: "Safe", status: model.RunStatusAccepted},
		{invariant: "InitialOnly", status: model.RunStatusRE},
	} {
		compileResp, err := postCompileRequest(baseURL, model.CompileRequest{
			Lang: compileLanguage,
			Sources: []model.Source{
				{Name: "Main.tla", DataB64: encodeScript(specification)},
				{Name: "Main.cfg", DataB64: encodeScript("SPECIFICATION Spec\nINVARIANT " + tc.invariant + "\n")},
			},
		})
		if err != nil {
			return fmt.Errorf("%s invariant %s compile request failed: %w", compileLanguage, tc.invariant, err)
		}
		if compileResp.Status != model.CompileStatusOK || len(compileResp.Artifacts) == 0 {
			return fmt.Errorf("%s invariant %s compile failed: status=%s artifacts=%d reason=%s stdout=%q stderr=%q", compileLanguage, tc.invariant, compileResp.Status, len(compileResp.Artifacts), compileResp.Reason, compileResp.Stdout, compileResp.Stderr)
		}
		binaries := make([]model.Binary, 0, len(compileResp.Artifacts))
		for _, artifact := range compileResp.Artifacts {
			binaries = append(binaries, model.Binary{Name: artifact.Name, DataB64: artifact.DataB64, Mode: artifact.Mode})
		}
		runResp, err := postExecuteRequest(baseURL, model.RunRequest{
			Lang:     profile.RunLang,
			Binaries: binaries,
			Limits:   model.Limits{TimeMs: 15000, MemoryMB: 1536, OutputBytes: 65536},
		})
		if err != nil {
			return fmt.Errorf("%s invariant %s execute request failed: %w", compileLanguage, tc.invariant, err)
		}
		if runResp.Status != tc.status {
			return fmt.Errorf("%s invariant %s execute failed: status=%s want=%s reason=%s stdout=%q stderr=%q", compileLanguage, tc.invariant, runResp.Status, tc.status, runResp.Reason, runResp.Stdout, runResp.Stderr)
		}
		if tc.status == model.RunStatusRE && !strings.Contains(runResp.Stderr, "Invariant InitialOnly is violated") {
			return fmt.Errorf("%s false invariant failed without a TLC counterexample: reason=%s stderr=%q", compileLanguage, runResp.Reason, runResp.Stderr)
		}
	}
	return nil
}
