package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func TestEntrySelftestRequestsPassAdmission(t *testing.T) {
	type expectedRun struct {
		mode   string
		status string
	}
	baseline := compileExecuteCases()["entry"]
	var fullSuiteRuns []expectedRun
	for range baseline.judgeIO {
		fullSuiteRuns = append(fullSuiteRuns, expectedRun{"stdio", model.RunStatusAccepted})
	}
	fullSuiteRuns = append(fullSuiteRuns,
		expectedRun{"stdio", model.RunStatusWA},
		expectedRun{"state", model.RunStatusAccepted},
		expectedRun{"state", model.RunStatusWA},
	)
	for _, tc := range []struct {
		name string
		runs []expectedRun
		run  func(string) error
	}{
		{
			name: "compile-execute and wrong-answer controls",
			runs: fullSuiteRuns,
			run: func(baseURL string) error {
				return verifyCompileExecuteLanguages(baseURL, "entry")
			},
		},
		{
			name: "semantic success and wrong-answer control",
			runs: []expectedRun{{"stdio", model.RunStatusAccepted}, {"stdio", model.RunStatusWA}},
			run: func(baseURL string) error {
				return verifyLanguageSemanticCase(baseURL, "entry", "ENTRY", baseline, languageSemanticCase{
					name: "stdio addition", sources: baseline.sources,
					stdin: "20\n22\n", expectedStdout: "42\n",
				})
			},
		},
		{
			name: "semantic runtime failure control",
			runs: []expectedRun{{"stdio", model.RunStatusTLE}},
			run: func(baseURL string) error {
				return verifyLanguageSemanticCase(baseURL, "entry", "ENTRY", baseline, languageSemanticCase{
					name: "execution limit", sources: baseline.sources,
					expectedRunStatus: model.RunStatusTLE,
				})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var runCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var result any
				switch r.URL.Path {
				case "/compile":
					var request model.CompileRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					compiled := model.CompileResponse{Status: model.CompileStatusOK}
					for _, source := range request.Sources {
						compiled.Artifacts = append(compiled.Artifacts, model.Artifact{Name: source.Name, DataB64: source.DataB64})
					}
					result = compiled
				case "/execute":
					var request model.RunRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					if err := runvalidation.Validate(&request); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					index := int(runCount.Add(1)) - 1
					if index >= len(tc.runs) {
						http.Error(w, "unexpected execution request", http.StatusBadRequest)
						return
					}
					want := tc.runs[index]
					if request.Lang != "entry" || request.EntryMode != want.mode || request.Limits != baseline.limits {
						t.Errorf("request %d: lang=%q entry_mode=%q limits=%+v, want entry/%s and %+v", index+1, request.Lang, request.EntryMode, request.Limits, want.mode, baseline.limits)
					}
					result = model.RunResponse{Status: want.status}
				default:
					http.NotFound(w, r)
					return
				}
				payload, err := json.Marshal(result)
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "event: result\ndata: %s\n\n", payload)
			}))
			defer server.Close()
			if err := tc.run(server.URL); err != nil {
				t.Fatal(err)
			}
			if got := int(runCount.Load()); got != len(tc.runs) {
				t.Fatalf("executed %d controls, want %d", got, len(tc.runs))
			}
		})
	}
}
