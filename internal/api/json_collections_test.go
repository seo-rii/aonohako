package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func collectionJSONForTest(prefix, suffix string, count int, object bool) string {
	var body strings.Builder
	body.WriteString(prefix)
	for i := 0; i < count; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		if object {
			fmt.Fprintf(&body, "%q:", fmt.Sprint(i))
		}
		body.WriteString(`{}`)
	}
	body.WriteString(suffix)
	return body.String()
}

func TestJSONCollectionAdmissionBounds(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, suffix string
		limit                int
		compile, object      bool
	}{
		{name: "sources", prefix: `{"sources":[`, suffix: `]}`, limit: maxCompileSourceFiles, compile: true},
		{name: "binaries", prefix: `{"binaries":[`, suffix: `]}`, limit: runvalidation.MaxBinaryFiles},
		{name: "programs", prefix: `{"programs":[`, suffix: `]}`, limit: runvalidation.MaxPrograms},
		{name: "steps", prefix: `{"steps":[`, suffix: `]}`, limit: runvalidation.MaxSteps},
		{name: "file_outputs", prefix: `{"file_outputs":[`, suffix: `]}`, limit: 1},
		{name: "sidecar_outputs", prefix: `{"sidecar_outputs":[`, suffix: `]}`, limit: runvalidation.MaxSidecarOutputs},
		{name: "programs.binaries", prefix: `{"programs":[{"binaries":[`, suffix: `]}]}`, limit: runvalidation.MaxBinaryFiles},
		{name: "steps.stdin_parts", prefix: `{"steps":[{"stdin_parts":[`, suffix: `]}]}`, limit: runvalidation.MaxStdinParts},
		{name: "interactor.binaries", prefix: `{"interactor":{"binaries":[`, suffix: `]}}`, limit: runvalidation.MaxBinaryFiles},
		{name: "spj.sidecar_outputs", prefix: `{"spj":{"sidecar_outputs":[`, suffix: `]}}`, limit: runvalidation.MaxSidecarOutputs},
		{name: "pipeline.resources", prefix: `{"pipeline":{"resources":{`, suffix: `}}}`, limit: runvalidation.MaxPipelineResources, object: true},
		{name: "pipeline.programs", prefix: `{"pipeline":{"programs":[`, suffix: `]}}`, limit: runvalidation.MaxPrograms},
		{name: "pipeline.programs.binaries", prefix: `{"pipeline":{"programs":[{"binaries":[`, suffix: `]}]}}`, limit: runvalidation.MaxBinaryFiles},
		{name: "pipeline.steps", prefix: `{"pipeline":{"steps":[`, suffix: `]}}`, limit: runvalidation.MaxPipelineSteps},
		{name: "pipeline.steps.stdin", prefix: `{"pipeline":{"steps":[{"stdin":[`, suffix: `]}]}}`, limit: runvalidation.MaxStdinParts},
		{name: "pipeline.steps.outputs", prefix: `{"pipeline":{"steps":[{"outputs":[`, suffix: `]}]}}`, limit: runvalidation.MaxPipelineOutputs},
		{name: "pipeline.final_judge.spj.sidecar_outputs", prefix: `{"pipeline":{"final_judge":{"spj":{"sidecar_outputs":[`, suffix: `]}}}}`, limit: runvalidation.MaxSidecarOutputs},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, count := range []int{tc.limit, tc.limit + 1} {
				body := collectionJSONForTest(tc.prefix, tc.suffix, count, tc.object)
				var dst any = &model.RunRequest{}
				if tc.compile {
					dst = &model.CompileRequest{}
				}
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
				err := decodeJSONBody(httptest.NewRecorder(), req, dst)
				if count == tc.limit {
					if err != nil {
						t.Fatalf("at limit: %v", err)
					}
					continue
				}
				var limitErr *jsonCollectionLimitError
				if !errors.As(err, &limitErr) || limitErr.field != tc.name || limitErr.limit != tc.limit {
					t.Fatalf("over limit: error = %v, want %s limit %d", err, tc.name, tc.limit)
				}
			}
		})
	}
}

func TestJSONCollectionSchemaInventory(t *testing.T) {
	seen := make(map[jsonCollectionField]bool)
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Map {
			visit(typ.Elem())
			return
		}
		if typ.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			if field.Type.Kind() == reflect.Slice || field.Type.Kind() == reflect.Map {
				key := jsonCollectionField{typ, field.Name}
				seen[key] = true
				if jsonCollectionLimits[key] <= 0 {
					t.Errorf("request collection %s.%s has no admission bound", typ, field.Name)
				}
			}
			visit(field.Type)
		}
	}
	visit(reflect.TypeFor[model.CompileRequest]())
	visit(reflect.TypeFor[model.RunRequest]())
	for key := range jsonCollectionLimits {
		if !seen[key] {
			t.Errorf("stale collection policy for %s.%s", key.typ, key.name)
		}
	}
	if _, err := buildJSONDecodeSchema(reflect.TypeFor[struct{ Unbounded []string }](), 0); err == nil {
		t.Fatal("schema builder accepted an unbounded new request collection")
	}
}

func TestCommunicationParticipantIDsAdmissionBounds(t *testing.T) {
	for _, tc := range []struct {
		body string
		want []string
	}{
		{`{"communication":{"participant_program_ids":["seat0","seat1"]}}`, []string{"seat0", "seat1"}},
		{`{"communication":{"participant_program_ids":["seat0","seat1","seat2"]}}`, nil},
	} {
		var req model.RunRequest
		err := decodeJSONBody(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(tc.body)), &req)
		if tc.want != nil {
			if err != nil || req.Communication == nil || !reflect.DeepEqual(req.Communication.ParticipantProgramIDs, tc.want) {
				t.Fatalf("valid seat IDs changed: request = %+v, error = %v", req.Communication, err)
			}
			continue
		}
		var limitErr *jsonCollectionLimitError
		if !errors.As(err, &limitErr) || limitErr.field != "communication.participant_program_ids" || limitErr.limit != 2 {
			t.Fatalf("oversized seat IDs did not hit the admission limit: %v", err)
		}
	}
}

func TestJSONCollectionKeysCannotBypassBounds(t *testing.T) {
	for _, key := range []string{`steps`, `STEPS`, `st\u0065ps`, `\u017Fteps`} {
		t.Run(key, func(t *testing.T) {
			body := `{"` + key + `":[null,null,null]}`
			var req model.RunRequest
			err := decodeJSONBody(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body)), &req)
			var limitErr *jsonCollectionLimitError
			if !errors.As(err, &limitErr) || limitErr.field != "steps" || len(req.Steps) != runvalidation.MaxSteps {
				t.Fatalf("error = %v, decoded steps = %d", err, len(req.Steps))
			}
		})
	}
}

func TestJSONCollectionDuplicateFieldsStayBounded(t *testing.T) {
	resources := collectionJSONForTest(`{"pipeline":{"resources":{`, `},"resources":{"extra":{}}}}`, runvalidation.MaxPipelineResources, true)
	for _, body := range []string{
		`{"steps":[{},{},{}],"steps":[]}`,
		`{"steps":[],"STEPS":[{},{},{}]}`,
		resources,
	} {
		var req model.RunRequest
		err := decodeJSONBody(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body)), &req)
		var limitErr *jsonCollectionLimitError
		if !errors.As(err, &limitErr) {
			t.Fatalf("duplicate field bypassed collection bound: %v", err)
		}
	}
}

func TestBoundedJSONPreservesStandardDecoding(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `{"steps":null,"pipeline":null,"programs":[]}`,
		`{"STEPS":[{"ID":"first","stdin_parts":null},{"id":"second","stdin_parts":[]}]}`,
		`{"st\u0065ps":[{"id":"first"}],"\u017Fteps":[{}]}`,
		`{"steps":[{"id":"first","stdin_parts":[{"type":"text","data":"before"}]}],"steps":[{"stdin_parts":[{"data":"after"}]}]}`,
		`{"steps":[{"id":"first"}],"steps":null,"steps":[{}]}`,
		`{"steps":[{}],"steps":[]}`,
		`{"steps":[{"id":"first"},{"id":"second"}],"steps":[{}],"steps":[{},{}]}`,
		`{"pipeline":{"resources":{"a":{"data_b64":"before"}}},"pipeline":{"resources":{"b":{},"a":{"data_url":"after"}}}}`,
		`{"pipeline":{"resources":{"a":{},"a":{},"a":{}}}}`,
		`{"pipeline":{"resources":{"a":{}}},"pipeline":{"resources":null}}`,
		`{"pipeline":{"resources":{"a":{}}},"pipeline":null}`,
		`{"capture_limits":{"stdout_bytes":0,"stderr_bytes":12},"limits":{"time_ms":123,"memory_mb":64},"emit_logs":false}`,
	} {
		t.Run(body, func(t *testing.T) {
			var got, want model.RunRequest
			standard := json.NewDecoder(strings.NewReader(body))
			standard.DisallowUnknownFields()
			if err := standard.Decode(&want); err != nil {
				t.Fatal(err)
			}
			if err := decodeJSONBody(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(body)), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("bounded decode differs from encoding/json\ngot: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestBoundedJSONPreservesErrorClasses(t *testing.T) {
	for _, tc := range []struct{ body, code string }{
		{`{"steps":[{"unexpected":true}]}`, "unknown_json_field"},
		{`{"pipeline":{"unexpected":[{"nested":[1,2,3]}]}}`, "unknown_json_field"},
		{`{"unexpected":true,"steps":[{},{},{}]}`, "unknown_json_field"},
		{`{"lang":42,"steps":[{},{},{}]}`, "invalid_json_type"},
		{`{"steps":{}}`, "invalid_json_type"},
		{`{"pipeline":[]}`, "invalid_json_type"},
		{`{"pipeline":{"resources":[]}}`, "invalid_json_type"},
		{`{"steps":[false]}`, "invalid_json_type"},
		{`{"steps":[{"limits":{"time_ms":"bad"}}]}`, "invalid_json_type"},
		{`[]`, "invalid_json_type"},
		{`42`, "invalid_json_type"},
		{`{"steps":[}`, "invalid_json"},
		{`{"steps":[{}]`, "invalid_json"},
		{`{"steps":[{}],}`, "invalid_json"},
		{`{"unexpected":{"bad":}}`, "invalid_json"},
		{`{"steps":{"bad":}}`, "invalid_json"},
		{`{"pipeline":{"resources":{"a":{}}}} []`, "unexpected_trailing_json"},
		{`{"steps":[]} {"steps":[{},{},{}]}`, "unexpected_trailing_json"},
		{`{"steps":[]} malformed`, "invalid_json"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			var req model.RunRequest
			err := decodeJSONBody(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/execute", strings.NewReader(tc.body)), &req)
			if err == nil {
				t.Fatal("invalid body decoded successfully")
			}
			rr := httptest.NewRecorder()
			writeRequestBodyError(rr, err)
			assertJSONBodyError(t, rr.Result(), http.StatusBadRequest, tc.code)
		})
	}
}

type countedJSONReader struct {
	io.Reader
	bytes int
}

func (r *countedJSONReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.bytes += n
	return n, err
}

func TestJSONCollectionOverflowStopsBeforeMaterializing(t *testing.T) {
	// The API-020 reproduction previously allocated over 6 MiB while decoding
	// this 30 KiB body into 10,000 RunStep structs before validating its count.
	body := collectionJSONForTest(`{"programs":[{}],"steps":[`, `]}`, 10000, false)
	reader := &countedJSONReader{Reader: strings.NewReader(body)}
	request := httptest.NewRequest(http.MethodPost, "/execute", reader)
	response := httptest.NewRecorder()
	var decoded model.RunRequest
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	err := decodeJSONBody(response, request, &decoded)
	runtime.ReadMemStats(&after)
	var limitErr *jsonCollectionLimitError
	if !errors.As(err, &limitErr) || limitErr.field != "steps" {
		t.Fatalf("error = %v, want early steps limit rejection", err)
	}
	if len(decoded.Steps) > runvalidation.MaxSteps || cap(decoded.Steps) > runvalidation.MaxSteps {
		t.Fatalf("oversized step slice was materialized: len=%d cap=%d", len(decoded.Steps), cap(decoded.Steps))
	}
	if reader.bytes > 4096 {
		t.Fatalf("overflow consumed %d/%d body bytes instead of rejecting promptly", reader.bytes, len(body))
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 1<<20 {
		t.Fatalf("overflow allocated %d bytes, want less than 1 MiB", allocated)
	}
	t.Logf("body_bytes=%d read_bytes=%d decoded_steps=%d decode_total_alloc_delta=%d", len(body), reader.bytes, len(decoded.Steps), after.TotalAlloc-before.TotalAlloc)
	runtime.KeepAlive(decoded)
}

func TestJSONCollectionErrorsReleaseAdmissionBeforeSSE(t *testing.T) {
	for _, endpoint := range []string{"/compile", "/execute"} {
		body, code := `{"steps":[{},{},{}]}`, "invalid_request"
		if endpoint == "/compile" {
			body = collectionJSONForTest(`{"sources":[`, `]}`, maxCompileSourceFiles+1, false)
			code = "too_many_sources"
		}
		for _, auth := range []string{"bearer", "platform-hmac"} {
			t.Run(endpoint+"/"+auth, func(t *testing.T) {
				server := jsonBodyServerForTest(t, auth)
				request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
				setJSONBodyAuthForTest(t, request, auth, strings.NewReader(body), 1)
				response := httptest.NewRecorder()
				server.Handler().ServeHTTP(response, request)
				assertJSONBodyError(t, response.Result(), http.StatusBadRequest, code)
				active, pending := server.queue.Snapshot()
				if active != 0 || pending != 0 || server.uploads.Load() != 0 || server.streams.Load() != 0 {
					t.Fatal("collection overflow retained admission or entered the run queue")
				}
			})
		}
	}
}

func FuzzBoundedJSONMatchesStandard(f *testing.F) {
	for _, seed := range []string{`{}`, `null`, `{"steps":[{"id":"a"}],"STEPS":[{}]}`, `{"steps":[{"id":"a"},{"id":"b"}],"steps":[{}],"steps":[{},{}]}`, `{"pipeline":{"resources":{"a":{},"a":null}}}`, `{"steps":[{},{},{}]}`, `{"steps":{"bad":[]}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		var got, want model.RunRequest
		bounded := json.NewDecoder(strings.NewReader(body))
		bounded.DisallowUnknownFields()
		err := decodeBoundedJSON(bounded, &got)
		var limitErr *jsonCollectionLimitError
		if errors.As(err, &limitErr) {
			return // Deliberately rejected before the standard decoder's allocation.
		}
		standard := json.NewDecoder(strings.NewReader(body))
		standard.DisallowUnknownFields()
		wantErr := standard.Decode(&want)
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("bounded error = %v, standard error = %v", err, wantErr)
		}
		if err == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("bounded request differs from encoding/json: %q", body)
		}
	})
}
