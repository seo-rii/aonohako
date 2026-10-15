package runvalidation

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func populatedRemoteRequest(s string) *model.RunRequest {
	var req model.RunRequest
	populateRemoteRequestValue(reflect.ValueOf(&req).Elem(), s)
	return &req
}

func populatedRemoteCompileRequest(s string) *model.CompileRequest {
	var req model.CompileRequest
	populateRemoteRequestValue(reflect.ValueOf(&req).Elem(), s)
	return &req
}

func populateRemoteRequestValue(v reflect.Value, s string) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			populateRemoteRequestValue(v.Field(i), s)
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		populateRemoteRequestValue(v.Elem(), s)
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 2, 2))
		populateRemoteRequestValue(v.Index(0), s)
		populateRemoteRequestValue(v.Index(1), s)
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		for _, key := range []string{s + "first", s + "second"} {
			value := reflect.New(v.Type().Elem()).Elem()
			populateRemoteRequestValue(value, s)
			v.SetMapIndex(reflect.ValueOf(key), value)
		}
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(-42)
	default:
		panic("unhandled request field " + v.Type().String())
	}
}

func checkRemoteJSONSize(t *testing.T, req any) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int{-1, 0, 1} {
		counter := jsonSizeCounter{limit: len(raw) + delta}
		err := counter.value(reflect.ValueOf(req))
		if delta < 0 {
			if !errors.Is(err, errJSONSizeLimit) {
				t.Fatalf("one byte below encoded size %d: error = %v", len(raw), err)
			}
		} else if err != nil || counter.size != len(raw) {
			t.Fatalf("JSON size = %d, error = %v, want %d", counter.size, err, len(raw))
		}
	}
}

func TestRemoteJSONSizeMatchesMarshal(t *testing.T) {
	for _, s := range []string{"", "plain", "\x00\x01\b\f\n\r\t\"\\<>&", "한글\u2028\u2029", "\xff\xfe\xc0\x80"} {
		t.Run(strings.ReplaceAll(s, "\x00", "NUL"), func(t *testing.T) {
			checkRemoteJSONSize(t, populatedRemoteRequest(s))
			checkRemoteJSONSize(t, populatedRemoteCompileRequest(s))
		})
	}
	checkRemoteJSONSize(t, nil)
	checkRemoteJSONSize(t, &model.RunRequest{})
	checkRemoteJSONSize(t, &model.CompileRequest{})
	checkRemoteJSONSize(t, (*model.CompileRequest)(nil))
	zero, disabled := 0, false
	checkRemoteJSONSize(t, &model.RunRequest{
		Binaries:      []model.Binary{},
		EmitLogs:      &disabled,
		CaptureLimits: &model.CaptureLimits{StdoutBytes: &zero},
	})
}

func TestValidateRemoteCompileRequestSizeAtTransportBoundary(t *testing.T) {
	req := &model.CompileRequest{
		Lang: "PYTHON3", Version: "v",
		Sources: []model.Source{{Name: "Main.py"}, {Name: "helper.py"}, {Name: "data.py"}},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(raw) - len(req.Version)
	text := strings.Repeat("A", MaxJSONRequestBytes)
	// Each base64 source represents less than 16 MiB; together they remain
	// below the decoded 48 MiB field budget while approaching the wire limit.
	const perSource = (MaxJSONRequestBytes - 1024) / 3
	for i := range req.Sources {
		req.Sources[i].DataB64 = text[:perSource]
	}
	for _, delta := range []int{-1, 0, 1} {
		req.Version = text[:MaxJSONRequestBytes-overhead-3*perSource+delta]
		err := ValidateRemoteCompileRequestSize(req)
		if delta <= 0 {
			if err != nil {
				t.Fatalf("compile wire size limit%+d: %v", delta, err)
			}
		} else {
			var sizeErr *RemoteRequestSizeError
			if !errors.As(err, &sizeErr) || sizeErr.LimitBytes != MaxJSONRequestBytes {
				t.Fatalf("compile wire size limit+1: error = %v", err)
			}
		}
	}
}

func TestValidateRemoteRequestSizeAtTransportBoundary(t *testing.T) {
	req := &model.RunRequest{ExpectedStdout: "x"}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	overhead := len(raw) - len(req.ExpectedStdout)
	// Share the same input backing memory across all three boundary cases.
	text := strings.Repeat("x", MaxJSONRequestBytes)
	for _, delta := range []int{-1, 0, 1} {
		req.ExpectedStdout = text[:MaxJSONRequestBytes-overhead+delta]
		err := ValidateRemoteRequestSize(req)
		if delta <= 0 {
			if err != nil {
				t.Fatalf("wire size limit%+d: %v", delta, err)
			}
		} else {
			var sizeErr *RemoteRequestSizeError
			if !errors.As(err, &sizeErr) || sizeErr.LimitBytes != MaxJSONRequestBytes {
				t.Fatalf("wire size limit+1: error = %v", err)
			}
		}
	}
}

func TestRemoteRequestSizeCountsBase64AndEscapeInflation(t *testing.T) {
	data := strings.Repeat("b", 4096)
	req := &model.RunRequest{
		Binaries:       []model.Binary{{Name: "binary", DataB64: base64.StdEncoding.EncodeToString([]byte(data))}},
		ExpectedStdout: strings.Repeat("<\x00", 2048),
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	decodedBytes := len(data) + len(req.ExpectedStdout)
	if len(raw) <= decodedBytes {
		t.Fatal("fixture did not inflate on encoding")
	}
	counter := jsonSizeCounter{limit: decodedBytes}
	if err := counter.value(reflect.ValueOf(req)); !errors.Is(err, errJSONSizeLimit) {
		t.Fatalf("encoded inflation was not counted: %v", err)
	}
	checkRemoteJSONSize(t, req)
}

func TestRemoteRequestSizeDoesNotAllocatePayloadCopies(t *testing.T) {
	payload := strings.Repeat("x", 1<<20)
	runReq := &model.RunRequest{ExpectedStdout: payload}
	compileReq := &model.CompileRequest{Sources: []model.Source{{Name: "Main.py", DataB64: payload}}}
	for _, tc := range []struct {
		name     string
		validate func() error
	}{
		{name: "execute", validate: func() error { return ValidateRemoteRequestSize(runReq) }},
		{name: "compile", validate: func() error { return ValidateRemoteCompileRequestSize(compileReq) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if err := tc.validate(); err != nil {
						b.Fatal(err)
					}
				}
			})
			if bytes := result.AllocedBytesPerOp(); bytes > 16<<10 {
				t.Fatalf("size validation allocated %d bytes/op for a 1 MiB payload", bytes)
			}
		})
	}
}

func FuzzRemoteRequestJSONSize(f *testing.F) {
	for _, seed := range []string{"", "text", "\x00\n<>&\"\\", "한글\u2028\u2029", "\xff\xfe"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 4096 {
			t.Skip()
		}
		checkRemoteJSONSize(t, populatedRemoteRequest(s))
		checkRemoteJSONSize(t, populatedRemoteCompileRequest(s))
	})
}
