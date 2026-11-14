package execute

import (
	"encoding/json"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func peerMeasurementRequest(mode string) *model.RunRequest {
	req := &model.RunRequest{WorkMeasurement: true, Lang: "CPP20"}
	if mode == "interactive" {
		req.Interactor = &model.InteractorSpec{Lang: "CPP20"}
		return req
	}
	req.Lang = ""
	req.Programs = []model.RunProgram{{ID: "judge", Lang: "CPP20"}, {ID: "solver", Lang: "CPP20"}}
	req.Communication = &model.CommunicationSpec{Version: 1, ParticipantProgramID: "solver", ManagerProgramID: "judge", ParticipantCount: 2}
	if mode == "communication-v2" {
		req.Programs = append(req.Programs, model.RunProgram{ID: "other", Lang: "CPP20"})
		req.Communication.Version = 2
		req.Communication.ParticipantProgramID = ""
		req.Communication.ParticipantProgramIDs = []string{"other", "solver"}
	}
	return req
}

func completePeerMeasurementResponse(t *testing.T, req *model.RunRequest) model.RunResponse {
	t.Helper()
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		t.Fatal(err)
	}
	response := model.RunResponse{Status: model.RunStatusAccepted}
	for _, role := range roles {
		exit := 0
		response.WorkMeasurements = append(response.WorkMeasurements, model.WorkMeasurementProcess{
			Role: role.role, ProgramID: role.programID, ParticipantIndex: role.participantIndex,
			ExecutionStatus: "OK", ExitCode: &exit, Report: completeEnvelopeReport(),
		})
	}
	return response
}

func TestPeerWorkMeasurementRoleAdmission(t *testing.T) {
	cfg, err := durante.ParseConfig("cpp,native,jvm", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		t.Run(mode, func(t *testing.T) {
			req := peerMeasurementRequest(mode)
			if err := validateWorkMeasurementRequest(req, cfg); err != nil {
				t.Fatal(err)
			}
			roles, err := workMeasurementRequestRoles(req)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "interactive" {
				if len(roles) != 2 || roles[0].programID != "contestant" || roles[1].programID != "interactor" || roles[1].participantIndex != nil {
					t.Fatalf("incorrect interactive roles: %+v", roles)
				}
				req.Interactor.Lang = "binary"
			} else {
				firstID := "solver"
				if mode == "communication-v2" {
					firstID = "other"
				}
				if len(roles) != 3 || roles[0].programID != firstID || *roles[0].participantIndex != 0 || *roles[1].participantIndex != 1 || roles[2].programID != "judge" || roles[2].participantIndex != nil {
					t.Fatalf("incorrect communication roles: %+v", roles)
				}
				req.Programs[0].Lang = "binary"
			}
			if err := validateWorkMeasurementRequest(req, cfg); err == nil || !strings.Contains(err.Error(), "source-language") {
				t.Fatalf("ambiguous judge language admitted: %v", err)
			}
		})
	}
	req := peerMeasurementRequest("interactive")
	req.Interactor.Lang = "JAVA"
	if err := validateWorkMeasurementRequest(req, cfg); err == nil || !strings.Contains(err.Error(), "JVM") {
		t.Fatalf("unqualified JVM admitted: %v", err)
	}
	req.Interactor.Lang = "RUST2021"
	if err := validateWorkMeasurementRequest(req, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Families = []string{"cpp"}
	if err := validateWorkMeasurementRequest(req, cfg); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("disabled peer family admitted: %v", err)
	}
	req = peerMeasurementRequest("communication-v1")
	req.Communication.ParticipantCount = runvalidation.MaxCommunicationParticipants
	roles, err := workMeasurementRequestRoles(req)
	if err != nil || len(roles) != runvalidation.MaxCommunicationParticipants+1 || *roles[len(roles)-2].participantIndex != runvalidation.MaxCommunicationParticipants-1 {
		t.Fatalf("maximum participant roles: %d, %v", len(roles), err)
	}
	req.Communication.ParticipantCount++
	if _, err := workMeasurementRequestRoles(req); err == nil {
		t.Fatal("unbounded participant roles admitted")
	}
}

func TestPeerWorkMeasurementRemoteRoundTrip(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		t.Run(mode, func(t *testing.T) {
			req := peerMeasurementRequest(mode)
			response := completePeerMeasurementResponse(t, req)
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var decoded model.RunResponse
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkMeasurement(req, decoded); err != nil {
				t.Fatal(err)
			}
			// A peer's ordinary nonzero exit does not erase a completed
			// observation; the existing interactive/match verdict still wins.
			nonzero := 7
			decoded.Status = model.RunStatusRE
			decoded.WorkMeasurements[0].ExitCode = &nonzero
			if err := validateRemoteWorkMeasurement(req, decoded); err != nil {
				t.Fatal(err)
			}
			// Conversely, an accepted manager verdict does not complete a
			// participant terminated by coordinator shutdown.
			decoded.Status = model.RunStatusAccepted
			decoded.WorkMeasurements[0].ExecutionStatus = model.RunStatusTLE
			decoded.WorkMeasurements[0].TerminationSource = "wall_time"
			decoded.WorkMeasurements[0].Report = decoded.WorkMeasurements[0].Report.WithoutTotals("peer was stopped")
			if err := validateRemoteWorkMeasurement(req, decoded); err != nil {
				t.Fatal(err)
			}
			if !decoded.WorkMeasurements[1].Report.Complete {
				t.Fatal("finished peer's report was discarded")
			}
		})
	}
}

func TestPeerWorkMeasurementRemoteRejectsInconsistentRoles(t *testing.T) {
	changes := map[string]func(*model.RunResponse){
		"missing all":        func(r *model.RunResponse) { r.WorkMeasurements = nil },
		"missing peer":       func(r *model.RunResponse) { r.WorkMeasurements = r.WorkMeasurements[:1] },
		"extra peer":         func(r *model.RunResponse) { r.WorkMeasurements = append(r.WorkMeasurements, r.WorkMeasurements[0]) },
		"duplicate peer":     func(r *model.RunResponse) { r.WorkMeasurements[1] = r.WorkMeasurements[0] },
		"wrong program":      func(r *model.RunResponse) { r.WorkMeasurements[0].ProgramID = "other" },
		"missing index":      func(r *model.RunResponse) { r.WorkMeasurements[0].ParticipantIndex = nil },
		"wrong index":        func(r *model.RunResponse) { v := 1; r.WorkMeasurements[0].ParticipantIndex = &v },
		"missing report":     func(r *model.RunResponse) { r.WorkMeasurements[0].Report = nil },
		"missing exit":       func(r *model.RunResponse) { r.WorkMeasurements[0].ExitCode = nil },
		"signal":             func(r *model.RunResponse) { v := -1; r.WorkMeasurements[0].ExitCode = &v },
		"resource stop":      func(r *model.RunResponse) { r.WorkMeasurements[0].ExecutionStatus = model.RunStatusTLE },
		"canceled":           func(r *model.RunResponse) { r.WorkMeasurements[0].TerminationSource = "request_context" },
		"wrong status":       func(r *model.RunResponse) { r.WorkMeasurements[0].ExecutionStatus = "unknown" },
		"wrong family":       func(r *model.RunResponse) { r.WorkMeasurements[0].Report.Family = "native" },
		"promoted authority": func(r *model.RunResponse) { r.WorkMeasurements[0].Report.Authority = "trusted" },
		"promoted integrity": func(r *model.RunResponse) { v := true; r.WorkMeasurements[0].Report.IntegrityVerified = &v },
		"singular report":    func(r *model.RunResponse) { r.WorkMeasurement = completeEnvelopeReport() },
		"envelope metadata":  func(r *model.RunResponse) { r.WorkMeasurementExecution = &model.WorkMeasurementExecution{} },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			req := peerMeasurementRequest("communication-v1")
			response := completePeerMeasurementResponse(t, req)
			change(&response)
			if err := validateRemoteWorkMeasurement(req, response); err == nil {
				t.Fatal("inconsistent per-role observation accepted")
			}
		})
	}
	req := peerMeasurementRequest("interactive")
	if err := validateRemoteWorkMeasurement(req, model.RunResponse{Status: model.RunStatusInitFail}); err != nil {
		t.Fatal("pre-execution failure rejected", err)
	}
	response := completePeerMeasurementResponse(t, req)
	req.WorkMeasurement = false
	if err := validateRemoteWorkMeasurement(req, response); err == nil {
		t.Fatal("unsolicited per-role report accepted")
	}
	req.WorkMeasurement = true
	req.Interactor = nil
	if err := validateRemoteWorkMeasurement(req, response); err == nil {
		t.Fatal("ordinary request accepted per-role reports")
	}
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		req := peerMeasurementRequest(mode)
		req.WorkMeasurementLimits = &durante.ResourceLimits{CPUTimeMs: 1000, WallTimeMs: 2000, MemoryMB: 128}
		if err := runvalidation.ValidateWorkMeasurementLimits(req); err == nil {
			t.Fatalf("%s silently admitted an ordinary-only raw envelope", mode)
		}
	}
}
