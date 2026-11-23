package reference

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
	"aonohako/internal/pythonpolicy"
	"aonohako/internal/rustpolicy"
)

// Only the artifact envelope is exercised here. These bytes are never executed.
func runtimeTestArtifacts(profile CorpusProfile, source string) model.CompileResponse {
	if profile.Family == "native" {
		return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{collectionTestArtifact(true)}}
	}
	data := make([]byte, 17)
	data[2], data[3], data[16] = '\r', '\n', 1
	binary.LittleEndian.PutUint32(data[4:8], 3)
	return model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{
		{Name: "Main.py", DataB64: base64.StdEncoding.EncodeToString([]byte(source))},
		{Name: "Main.pyc", DataB64: base64.StdEncoding.EncodeToString(data)},
	}}
}

func TestRuntimeCollectionReusesCompiledArtifactAndLanguage(t *testing.T) {
	for _, language := range []string{"PYTHON3", "PYPY3", "RUST2024"} {
		t.Run(language, func(t *testing.T) {
			profile, _ := ResolveCorpusProfile(language)
			corpus, _ := CorpusForLanguage(language, true)
			corpus = corpus[:1]
			dataset := collectionTestDataset()
			dataset.SourceLanguage, dataset.CorpusID = language, RuntimeCorpusID
			dataset.NativeLimits.MemoryMB, dataset.MeteredLimits.MemoryMB = 512, 512
			compiles, executions, snapshots := 0, 0, 0
			var binaries []model.Binary
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/compile" {
					compiles++
					var request model.CompileRequest
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
						t.Error(err)
					}
					if request.Lang != language || len(request.Sources) != 1 || request.Sources[0].Name != profile.SourceName || request.Sources[0].DataB64 != base64.StdEncoding.EncodeToString([]byte(corpus[0].Source)) {
						t.Errorf("wrong runtime compile request: %+v", request)
					}
					if language == "RUST2024" && request.RustCrateMode != rustpolicy.CrateModeStdlib {
						t.Error("Rust collection did not explicitly select the standard library")
					}
					collectionTestWriteResult(t, w, runtimeTestArtifacts(profile, corpus[0].Source))
					return
				}
				executions++
				var request model.RunRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Lang != language || len(request.Binaries) != 1 || request.Binaries[0].Name != profile.ArtifactName || request.WorkMeasurement != (executions == 2) || request.WorkMeasurementTime || request.ExpectedStdout != corpus[0].Cases[0].ExpectedStdout {
					t.Errorf("wrong runtime execute request: %+v", request)
				}
				if binaries == nil {
					binaries = request.Binaries
				} else if !reflect.DeepEqual(request.Binaries, binaries) {
					t.Error("artifact changed across the native/metered/native triplet")
				}
				if language != "RUST2024" && request.PythonLibraryMode != pythonpolicy.LibraryModeStdlib {
					t.Error("Python collection did not explicitly select the standard library")
				}
				if request.WorkMeasurement {
					response := collectionTestMetered(dataset)
					response.WorkMeasurement.Family = profile.Family
					collectionTestWriteResult(t, w, response)
				} else {
					collectionTestWriteResult(t, w, collectionTestNative())
				}
			}))
			defer server.Close()
			options := CollectOptions{Language: language, MemoryMB: 512, Smoke: true, Repetitions: 1,
				OnSample: func(snapshot Dataset) error {
					snapshots++
					if snapshot.SourceLanguage != language || snapshot.CorpusID != RuntimeCorpusID || snapshot.Samples[0].Metered.WorkMeasurement.Family != profile.Family {
						t.Error("snapshot lost runtime provenance")
					}
					return nil
				},
			}
			if err := collectCorpus(context.Background(), server.Client(), server.URL, corpus, options, &dataset); err != nil {
				t.Fatal(err)
			}
			if compiles != 1 || executions != 3 || snapshots != 1 || len(dataset.Samples) != 1 || dataset.Samples[0].BinarySHA256 != binaries[0].SHA256 {
				t.Fatalf("incomplete triplet or artifact hash: %+v", dataset)
			}
		})
	}
}

func TestRuntimeArtifactValidationPreservesSourceAndFormat(t *testing.T) {
	profile, _ := ResolveCorpusProfile("PYTHON3")
	source := "print(42)\n"
	for name, edit := range map[string]func(*model.CompileResponse){
		"missing bytecode": func(r *model.CompileResponse) { r.Artifacts = r.Artifacts[:1] },
		"changed source": func(r *model.CompileResponse) {
			r.Artifacts[0].DataB64 = base64.StdEncoding.EncodeToString([]byte("print(0)\n"))
		},
		"wrong name": func(r *model.CompileResponse) { r.Artifacts[1].Name = "Other.pyc" },
		"duplicate":  func(r *model.CompileResponse) { r.Artifacts[1].Name = "Main.py" },
		"wrong mode": func(r *model.CompileResponse) { r.Artifacts[1].Mode = "exec" },
		"short bytecode": func(r *model.CompileResponse) {
			r.Artifacts[1].DataB64 = base64.StdEncoding.EncodeToString(make([]byte, 16))
		},
		"unchecked header": func(r *model.CompileResponse) {
			data, _ := base64.StdEncoding.DecodeString(r.Artifacts[1].DataB64)
			binary.LittleEndian.PutUint32(data[4:8], 0)
			r.Artifacts[1].DataB64 = base64.StdEncoding.EncodeToString(data)
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := runtimeTestArtifacts(profile, source)
			edit(&response)
			if _, _, err := collectionProfileArtifact(profile, response, source); err == nil {
				t.Fatal("incompatible compiled artifact accepted")
			}
		})
	}
	rust, _ := ResolveCorpusProfile("RUST2024")
	response := runtimeTestArtifacts(rust, "")
	if _, _, err := collectionProfileArtifact(rust, response, ""); err != nil {
		t.Fatal("default Rust dynamic artifact rejected", err)
	}
	if _, _, err := collectionArtifact(response); err == nil {
		t.Fatal("legacy CPP20 stopped requiring its static artifact")
	}
}

func TestRuntimeCollectionLimitsRequireExplicitOperatorCapacity(t *testing.T) {
	options := CollectOptions{Language: "PYTHON3", MemoryMB: 512, Smoke: true, Repetitions: 1}
	cfg := collectionTestConfig()
	cfg.Execution.WorkMeasurement.Families = []string{"cpython"}
	limits := collectionTestDataset().MeteredLimits
	limits.MemoryMB = 512
	if err := validateCollectionConfig(cfg, options, limits); err == nil {
		t.Fatal("runtime corpus silently raised the operator's 256 MiB limit")
	}
	cfg.Execution.WorkMeasurement.ResourcePolicy.MaxLimits.MemoryMB = 512
	if err := validateCollectionConfig(cfg, options, limits); err != nil {
		t.Fatal("explicitly provisioned runtime corpus rejected", err)
	}
	options.MemoryMB = 1024
	if err := validateCollectionConfig(cfg, options, limits); err == nil {
		t.Fatal("memory option differed from recorded limits")
	}
	options.MemoryMB, options.Language = 512, "JAVA21"
	if err := validateCollectionConfig(cfg, options, limits); err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatal("unimplemented runtime admitted", err)
	}
}

func TestRuntimeArtifactLimitsMatchRustAPIAndPreserveOtherProfiles(t *testing.T) {
	rust, _ := ResolveCorpusProfile("RUST2024")
	artifact := collectionTestArtifact(true)
	minimal, _ := base64.StdEncoding.DecodeString(artifact.DataB64)
	data := make([]byte, compilewire.ArtifactBytes)
	copy(data, minimal)
	artifact.DataB64 = base64.StdEncoding.EncodeToString(data)
	response := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{artifact}}
	binary, digest, err := collectionProfileArtifact(rust, response, "")
	if err != nil || digest != collectionSHA(data) || binary.DataB64 != artifact.DataB64 {
		t.Fatal("API-sized Rust artifact was rejected or altered", err)
	}
	// The same API-sized bytes must also pass the bounded HTTP request path.
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.Copy(io.Discard, r.Body)
		collectionTestWriteResult(t, w, collectionTestNative())
	}))
	defer server.Close()
	request := model.RunRequest{Lang: "RUST2024", Binaries: []model.Binary{binary}}
	var result model.RunResponse
	if err := postCollectionSSE(context.Background(), server.Client(), server.URL+"/execute", request, &result, collectionExecuteBytes, time.Second*5); err != nil {
		t.Fatal("API-sized Rust artifact could not pass the HTTP request budget", err)
	}
	request.Lang = "CPP20"
	if err := postCollectionSSE(context.Background(), server.Client(), server.URL+"/execute", request, &result, collectionExecuteBytes, time.Second*5); err == nil || requests != 1 {
		t.Fatal("legacy request budget was raised", err, requests)
	}
	// A one-byte overflow can still have the same base64 encoded length.
	response.Artifacts[0].DataB64 = base64.StdEncoding.EncodeToString(append(data, 0))
	if _, _, err := collectionProfileArtifact(rust, response, ""); err == nil {
		t.Fatal("Rust collector accepted an artifact above the API limit")
	}
	tooLarge := base64.StdEncoding.EncodeToString(make([]byte, collectionArtifactBytes+1))
	for _, language := range []string{"CPP20", "PYTHON3", "PYPY3"} {
		profile, _ := ResolveCorpusProfile(language)
		response := model.CompileResponse{Status: model.CompileStatusOK, Artifacts: []model.Artifact{{Name: "Main", Mode: "exec", DataB64: tooLarge}}}
		if language != "CPP20" {
			response = runtimeTestArtifacts(profile, "print(42)\n")
			response.Artifacts[1].DataB64 = tooLarge
		}
		if _, _, err := collectionProfileArtifact(profile, response, "print(42)\n"); err == nil {
			t.Fatal("existing profile artifact cap was raised", language)
		}
	}
}
