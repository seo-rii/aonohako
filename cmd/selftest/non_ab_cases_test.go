package main

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/profiles"
)

func TestNonABSemanticCasesCoverDocumentedExceptions(t *testing.T) {
	semantics := nonABSemanticCases()
	baseline := compileExecuteCases()
	for language, tc := range baseline {
		if tc.nonABReason == "" {
			continue
		}
		if len(semantics[language]) == 0 {
			t.Errorf("A+B exception %q has no semantic conformance case", language)
		}
	}
	for language := range semantics {
		tc, ok := baseline[language]
		if !ok || tc.nonABReason == "" {
			t.Errorf("semantic exception %q has no documented A+B exception", language)
		}
	}
}

func TestNonABSemanticCasesHaveValidRequests(t *testing.T) {
	baseline := compileExecuteCases()
	for language, cases := range nonABSemanticCases() {
		t.Run(language, func(t *testing.T) {
			tc := baseline[language]
			names := map[string]bool{}
			for _, semantic := range cases {
				if strings.TrimSpace(semantic.name) == "" || names[semantic.name] {
					t.Errorf("semantic case has missing or repeated name %q", semantic.name)
				}
				names[semantic.name] = true
				compileLang := semantic.compileLang
				if compileLang == "" {
					compileLang = tc.compileLang
				}
				if _, ok := profiles.Resolve(compileLang); !ok {
					t.Errorf("semantic case %q uses unknown compile profile %q", semantic.name, compileLang)
				}
				if len(semantic.sources) == 0 || reflect.DeepEqual(semantic.sources, tc.sources) {
					t.Errorf("semantic case %q needs a source distinct from its startup smoke", semantic.name)
				}
				paths := map[string]bool{}
				for _, source := range semantic.sources {
					if strings.TrimSpace(source.Name) == "" || paths[source.Name] {
						t.Errorf("semantic case %q has missing or repeated source path %q", semantic.name, source.Name)
					}
					paths[source.Name] = true
					data, err := base64.StdEncoding.DecodeString(source.DataB64)
					if err != nil || len(data) == 0 {
						t.Errorf("semantic case %q source %q is empty or invalid base64", semantic.name, source.Name)
					}
				}
				switch semantic.expectedCompileStatus {
				case "", model.CompileStatusOK:
					switch semantic.expectedRunStatus {
					case "", model.RunStatusAccepted, model.RunStatusRE:
					default:
						t.Errorf("semantic case %q has unsupported run expectation %q", semantic.name, semantic.expectedRunStatus)
					}
				case model.CompileStatusCompileError:
					if semantic.expectedRunStatus != "" || semantic.expectedStdout != "" {
						t.Errorf("compile rejection %q also expects execution", semantic.name)
					}
				default:
					t.Errorf("semantic case %q has unsupported compile expectation %q", semantic.name, semantic.expectedCompileStatus)
				}
			}
		})
	}
}

func TestProofSemanticCasesRequireVerifierRejection(t *testing.T) {
	semantics := nonABSemanticCases()
	for _, language := range []string{"coq", "rocq", "lean4", "agda", "dafny", "why3", "isabelle", "fstar", "alloy", "acl2", "kframework"} {
		rejects := false
		for _, tc := range semantics[language] {
			rejects = rejects || tc.expectedCompileStatus == model.CompileStatusCompileError
		}
		if !rejects {
			t.Errorf("verification profile %q never rejects an invalid proof or definition", language)
		}
	}
}
