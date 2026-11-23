package runvalidation

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func validDuranteLimitsRequest() *model.RunRequest {
	return &model.RunRequest{
		Lang: "CPP20", WorkMeasurement: true,
		Binaries:              []model.Binary{{Name: "main", DataB64: "eA==", Mode: "exec"}},
		Limits:                model.Limits{TimeMs: 1000, WallTimeMs: 2000, MemoryMB: 128},
		WorkMeasurementLimits: &durante.ResourceLimits{CPUTimeMs: 4000, WallTimeMs: 8000, MemoryMB: 512},
	}
}

func TestWorkMeasurementLimitsValidateWithoutMutatingNominalLimits(t *testing.T) {
	req := validDuranteLimitsRequest()
	before, _ := json.Marshal(req)
	if err := Validate(req); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(req)
	if string(before) != string(after) {
		t.Fatal("validation modified request")
	}
	for _, language := range []string{"C", "C89", "CPP", "CPP98", "CPP26", "PYTHON3", "PYPY3", "RUST2024", "GO"} {
		req.Lang = language
		if err := Validate(req); err != nil {
			t.Fatalf("valid Durante profile %s: %v", language, err)
		}
	}
	var forwarded model.RunRequest
	if err := json.Unmarshal(before, &forwarded); err != nil || !reflect.DeepEqual(forwarded.WorkMeasurementLimits, req.WorkMeasurementLimits) {
		t.Fatalf("resource envelope did not survive wire encoding: %+v %v", forwarded.WorkMeasurementLimits, err)
	}
	if err := ValidateRemoteRequestSize(req); err != nil {
		t.Fatalf("remote size accounting rejected resource envelope: %v", err)
	}
}

func TestWorkMeasurementLimitsRejectInvalidOrMixedRequests(t *testing.T) {
	for name, edit := range map[string]func(*model.RunRequest){
		"disabled measurement":  func(r *model.RunRequest) { r.WorkMeasurement = false },
		"generic binary":        func(r *model.RunRequest) { r.Lang = "binary" },
		"unsupported JVM":       func(r *model.RunRequest) { r.Lang = "JAVA17" },
		"entry":                 func(r *model.RunRequest) { r.EntryMode = "state" },
		"interactor":            func(r *model.RunRequest) { r.Interactor = &model.InteractorSpec{} },
		"spj":                   func(r *model.RunRequest) { r.SPJ = &model.SPJSpec{} },
		"communication":         func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		"pipeline":              func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
		"program":               func(r *model.RunRequest) { r.Programs = []model.RunProgram{{}} },
		"step":                  func(r *model.RunRequest) { r.Steps = []model.RunStep{{}} },
		"cpu below nominal":     func(r *model.RunRequest) { r.WorkMeasurementLimits.CPUTimeMs = 999 },
		"memory below nominal":  func(r *model.RunRequest) { r.WorkMeasurementLimits.MemoryMB = 127 },
		"wall below nominal":    func(r *model.RunRequest) { r.Limits.WallTimeMs = 8001 },
		"wall below actual cpu": func(r *model.RunRequest) { r.WorkMeasurementLimits.WallTimeMs = 3999 },
		"huge cpu":              func(r *model.RunRequest) { r.WorkMeasurementLimits.CPUTimeMs = int(^uint(0) >> 1) },
		"huge memory":           func(r *model.RunRequest) { r.WorkMeasurementLimits.MemoryMB = int(^uint(0) >> 1) },
		"nominal zero":          func(r *model.RunRequest) { r.Limits.TimeMs = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			req := validDuranteLimitsRequest()
			edit(req)
			if err := ValidateWorkMeasurementLimits(req); err == nil {
				t.Fatal("direct runner validation accepted invalid envelope")
			}
			if err := Validate(req); err == nil {
				t.Fatal("API validation accepted invalid envelope")
			}
		})
	}
}

func TestWorkMeasurementLimitsPreserveLegacyRequests(t *testing.T) {
	req := validDuranteLimitsRequest()
	req.WorkMeasurementLimits = nil
	req.WorkMeasurement = false
	req.Lang = "binary"
	if err := Validate(req); err != nil {
		t.Fatalf("legacy request rejected: %v", err)
	}
	req.WorkMeasurement = true
	if err := Validate(req); err != nil {
		t.Fatalf("previous work_measurement validation changed: %v", err)
	}
	raw, _ := json.Marshal(req)
	if strings.Contains(string(raw), "work_measurement_limits") {
		t.Fatal("absent envelope changed legacy wire request")
	}
	if durante.MaxResourceTimeMs != MaxTimeMs || durante.MaxResourceMemoryMB != MaxMemoryMB {
		t.Fatal("resource and request caps drifted")
	}
}
