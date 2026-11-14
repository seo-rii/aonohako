package runvalidation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/model"
)

func TestWorkMeasurementTimeAcceptsOrdinaryCPPEnvelopes(t *testing.T) {
	for _, language := range []string{"C", "C11", "CPP", "CPP20", "CPP26", "cpp20"} {
		t.Run(language, func(t *testing.T) {
			req := validDuranteLimitsRequest()
			req.Lang = language
			req.WorkMeasurementTime = true
			before, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateWorkMeasurementLimits(req); err != nil {
				t.Fatalf("direct runner rejected a C/C++ time request: %v", err)
			}
			if err := Validate(req); err != nil {
				t.Fatalf("API rejected a C/C++ time request: %v", err)
			}
			after, err := json.Marshal(req)
			if err != nil || string(before) != string(after) {
				t.Fatalf("validation changed the request: %v", err)
			}
			var forwarded model.RunRequest
			if err := json.Unmarshal(before, &forwarded); err != nil || !reflect.DeepEqual(req, &forwarded) || !forwarded.WorkMeasurementTime {
				t.Fatalf("time opt-in or limits did not survive forwarding: %+v, %v", forwarded, err)
			}
			if err := ValidateRemoteRequestSize(req); err != nil {
				t.Fatalf("remote budget rejected a time request: %v", err)
			}
		})
	}
}

func TestWorkMeasurementTimeRejectsMissingEnvelopeAndUnsupportedModes(t *testing.T) {
	for name, edit := range map[string]func(*model.RunRequest){
		"measurement disabled": func(r *model.RunRequest) { r.WorkMeasurement = false },
		"no raw envelope":      func(r *model.RunRequest) { r.WorkMeasurementLimits = nil },
		"CPython":              func(r *model.RunRequest) { r.Lang = "PYTHON3" },
		"PyPy":                 func(r *model.RunRequest) { r.Lang = "PYPY3" },
		"JVM":                  func(r *model.RunRequest) { r.Lang = "JAVA21" },
		"generic binary":       func(r *model.RunRequest) { r.Lang = "binary" },
		"other native family":  func(r *model.RunRequest) { r.Lang = "RUST2021" },
		"interactor":           func(r *model.RunRequest) { r.Interactor = &model.InteractorSpec{} },
		"communication":        func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		"special judge":        func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} },
		"pipeline":             func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
		"programs":             func(r *model.RunRequest) { r.Programs = []model.RunProgram{{}} },
		"steps":                func(r *model.RunRequest) { r.Steps = []model.RunStep{{}} },
		"entry mode":           func(r *model.RunRequest) { r.EntryMode = "state" },
	} {
		t.Run(name, func(t *testing.T) {
			req := validDuranteLimitsRequest()
			req.WorkMeasurementTime = true
			edit(req)
			for _, validate := range []func(*model.RunRequest) error{ValidateWorkMeasurementLimits, Validate} {
				if err := validate(req); err == nil || !strings.Contains(err.Error(), "work_measurement") {
					t.Fatalf("time request accepted or rejected for an unrelated reason: %v", err)
				}
			}
		})
	}
}

func TestWorkMeasurementTimePreservesRequestsWithoutOptIn(t *testing.T) {
	for _, language := range []string{"CPP20", "PYTHON3", "PYPY3"} {
		for _, envelope := range []bool{false, true} {
			req := validDuranteLimitsRequest()
			req.Lang = language
			if !envelope {
				req.WorkMeasurementLimits = nil
			}
			if err := Validate(req); err != nil {
				t.Fatalf("existing %s request (envelope=%t) changed: %v", language, envelope, err)
			}
			raw, err := json.Marshal(req)
			if err != nil || strings.Contains(string(raw), "work_measurement_time") {
				t.Fatalf("absent time opt-in changed the wire request: %s, %v", raw, err)
			}
		}
	}
	req := validDuranteLimitsRequest()
	req.Lang, req.WorkMeasurement, req.WorkMeasurementLimits = "binary", false, nil
	if err := Validate(req); err != nil {
		t.Fatalf("unmetered binary request changed: %v", err)
	}
}
