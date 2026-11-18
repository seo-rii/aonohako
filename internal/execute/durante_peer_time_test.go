package execute

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func peerTimeFixture(t *testing.T, mode string) (*model.RunRequest, model.RunResponse) {
	t.Helper()
	req := peerMeasurementRequest(mode)
	req.WorkMeasurementPeerTime = &model.PeerTimeRequest{Version: 1}
	response := completePeerMeasurementResponse(t, req)
	score := 0.5
	response.Score, response.CPUTimeNs, response.CPUTimeMs, response.MemoryKB = &score, 12345678, 13, 8192
	for i := range response.WorkMeasurements {
		role := &response.WorkMeasurements[i]
		instructions := uint64(i+1) * 1_000_000
		role.Report.Instructions = &instructions
		role.Report.REPBreakdown = &durante.REPBreakdown{Version: 1}
		req.WorkMeasurementPeerTime.Budgets = append(req.WorkMeasurementPeerTime.Budgets, model.PeerTimeBudget{
			Role: role.Role, ProgramID: role.ProgramID, ParticipantIndex: role.ParticipantIndex, TimeMs: i + 1,
		})
	}
	// The response stays in canonical role order; the request deliberately does not.
	budgets := req.WorkMeasurementPeerTime.Budgets
	for i, j := 0, len(budgets)-1; i < j; i, j = i+1, j-1 {
		budgets[i], budgets[j] = budgets[j], budgets[i]
	}
	return req, response
}

func peerTimeExchange(t *testing.T, req *model.RunRequest, response model.RunResponse, cfg durante.Config, wantCalled bool) model.RunResponse {
	t.Helper()
	var called atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called.Store(true)
		var forwarded model.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&forwarded); err != nil {
			t.Error(err)
			return
		}
		if !reflect.DeepEqual(forwarded.WorkMeasurementPeerTime, req.WorkMeasurementPeerTime) {
			t.Error("forwarding changed peer budget values, identities, seats or request order")
		}
		data, err := json.Marshal(response)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: result\ndata: %s\n\n", data)
	}))
	defer server.Close()
	runner := newRemoteRunner(config.Config{Execution: config.ExecutionConfig{
		WorkMeasurement: cfg, Remote: config.RemoteExecutorConfig{URL: server.URL},
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := runner.Run(ctx, req, Hooks{})
	if called.Load() != wantCalled {
		t.Fatalf("downstream called=%v, want=%v: %+v", called.Load(), wantCalled, got)
	}
	return got
}

func TestPeerTimeBudgetsPreserveRolesAndRawResultsThroughSSE(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		t.Run(mode, func(t *testing.T) {
			req, original := peerTimeFixture(t, mode)
			before, _ := json.Marshal(original)
			cfg := timeMeasurementConfig()
			local := finishWorkMeasurementPeerTime(req, original, cfg)
			for i, role := range local.WorkMeasurements {
				result := role.TimeBudget
				if result == nil || result.Version != 1 || result.Authority != durante.Authority || result.BudgetMs != i+1 || result.Estimate == nil || result.Exceeded == nil || *result.Exceeded || result.Unavailable != "" {
					t.Fatalf("wrong independent role budget: %+v", role)
				}
				if result.Estimate.Nanoseconds != int64(i+1)*1_000_000 {
					t.Fatal("role observations were combined or attributed to another seat")
				}
			}
			after, _ := json.Marshal(original)
			if string(before) != string(after) {
				t.Fatal("conversion changed its original observation")
			}
			got := peerTimeExchange(t, req, local, cfg, true)
			if !reflect.DeepEqual(got, local) {
				t.Fatalf("remote result changed independent budgets or raw verdict/resource fields: %+v", got)
			}
			without := local
			without.WorkMeasurements = append([]model.WorkMeasurementProcess(nil), local.WorkMeasurements...)
			for i := range without.WorkMeasurements {
				without.WorkMeasurements[i].TimeBudget = nil
			}
			if !reflect.DeepEqual(without, original) {
				t.Fatal("budget conversion changed fields other than the requested diagnostic result")
			}
		})
	}
}

func TestPeerTimeBudgetExactBoundaryAndV2(t *testing.T) {
	for _, instructions := range []uint64{999999, 1000000, 1000001} {
		req, response := peerTimeFixture(t, "interactive")
		response.WorkMeasurements[0].Report.Instructions = &instructions
		got := finishWorkMeasurementPeerTime(req, response, timeMeasurementConfig()).WorkMeasurements[0].TimeBudget
		if got == nil || got.Estimate == nil || got.Exceeded == nil || *got.Exceeded != (instructions > 1000000) || got.Estimate.Nanoseconds != int64(instructions) {
			t.Fatalf("strict nanosecond boundary failed for %d: %+v", instructions, got)
		}
	}
	req, response := peerTimeFixture(t, "communication-v1")
	setREPBreakdown(response.WorkMeasurements[0].Report, 1)
	cfg := timeMeasurementConfig()
	cfg.TimePolicy.Method = durante.TimeMethodREPBreakdown
	cfg.TimePolicy.REPByteNSNumerator = 0
	cfg.TimePolicy.MOVSByteNSNumerator, cfg.TimePolicy.STOSByteNSNumerator = 2, 3
	got := finishWorkMeasurementPeerTime(req, response, cfg)
	estimate := got.WorkMeasurements[0].TimeBudget.Estimate
	if estimate == nil || estimate.Method != durante.TimeMethodREPBreakdown || estimate.Nanoseconds != 1_000_000+65*2+66*3 {
		t.Fatalf("v2 operation coefficients were not applied independently: %+v", estimate)
	}
	if remote := peerTimeExchange(t, req, got, cfg, true); !reflect.DeepEqual(remote, got) {
		t.Fatal("v2 peer estimate did not survive independent downstream verification")
	}
}

func TestPeerTimeUnavailableDoesNotDiscardCompletedPeers(t *testing.T) {
	for _, mode := range []string{"interactive", "communication-v1", "communication-v2"} {
		for name, edit := range map[string]func(*model.WorkMeasurementProcess){
			"coordinator cancellation": func(p *model.WorkMeasurementProcess) {
				p.ExecutionStatus = model.RunStatusTLE
				p.TerminationSource = "peer_cancelled"
				p.Report = p.Report.WithoutTotals("peer cancelled")
			},
			"manager failure": func(p *model.WorkMeasurementProcess) {
				p.ExecutionStatus = model.RunStatusRE
				p.Report = p.Report.WithoutTotals("peer execution failed")
			},
			"absent observation": func(p *model.WorkMeasurementProcess) {
				p.ExecutionStatus = model.RunStatusInitFail
				p.ExitCode, p.Report = nil, nil
			},
			"signals": func(p *model.WorkMeasurementProcess) {
				p.Report.SignalsObserved = 1
				p.Report.Quality = "signals_observed"
			},
			"partial REP": func(p *model.WorkMeasurementProcess) {
				n := uint64(1)
				p.Report.UnsupportedREPEntries = &n
				p.Report.REPTelemetryStatus = durante.REPPartial
			},
			"legacy split":        func(p *model.WorkMeasurementProcess) { p.Report.REPBreakdown = nil },
			"conversion overflow": func(p *model.WorkMeasurementProcess) { n := uint64(math.MaxUint64); p.Report.Instructions = &n },
		} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				req, response := peerTimeFixture(t, mode)
				response.Status = model.RunStatusRE
				last := len(response.WorkMeasurements) - 1
				edit(&response.WorkMeasurements[last])
				cfg := timeMeasurementConfig()
				got := finishWorkMeasurementPeerTime(req, response, cfg)
				if got.Status != response.Status || got.CPUTimeNs != response.CPUTimeNs || got.Score != response.Score {
					t.Fatal("diagnostic budget changed the coordinator result")
				}
				for i, role := range got.WorkMeasurements {
					if role.TimeBudget == nil || (i == last) != (role.TimeBudget.Unavailable != "") {
						t.Fatalf("one unavailable role discarded or converted another role: %+v", role)
					}
					if i == last && (role.TimeBudget.Estimate != nil || role.TimeBudget.Exceeded != nil) {
						t.Fatal("unavailable observation gained an estimate or budget verdict")
					}
				}
				if remote := peerTimeExchange(t, req, got, cfg, true); !reflect.DeepEqual(remote, got) {
					t.Fatalf("remote unavailable result changed: %+v", remote)
				}
			})
		}
	}
}

func TestPeerTimeNormalNonzeroExitPreservesDiagnosticEstimate(t *testing.T) {
	req, response := peerTimeFixture(t, "interactive")
	response.Status = model.RunStatusRE
	exit := 1
	response.WorkMeasurements[1].ExitCode = &exit
	got := finishWorkMeasurementPeerTime(req, response, timeMeasurementConfig())
	if got.Status != model.RunStatusRE || got.WorkMeasurements[1].TimeBudget.Estimate == nil {
		t.Fatal("normal nonzero exit lost its completed diagnostic observation or changed verdict")
	}
}

func TestPeerTimeRemoteRejectsAlteredOrMissingBudgets(t *testing.T) {
	for name, edit := range map[string]func(*model.RunResponse){
		"old runner ignored request":     func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget = nil },
		"budget":                         func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.BudgetMs++ },
		"authority":                      func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Authority = "authoritative" },
		"version":                        func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Version++ },
		"exceeded":                       func(r *model.RunResponse) { *r.WorkMeasurements[0].TimeBudget.Exceeded = true },
		"weights":                        func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Estimate.InstructionNSNumerator++ },
		"model":                          func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Estimate.ModelID = "remote-model" },
		"nanoseconds":                    func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Estimate.Nanoseconds++ },
		"unavailable alongside estimate": func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Unavailable = "invented" },
	} {
		t.Run(name, func(t *testing.T) {
			req, response := peerTimeFixture(t, "communication-v2")
			cfg := timeMeasurementConfig()
			response = finishWorkMeasurementPeerTime(req, response, cfg)
			edit(&response)
			got := peerTimeExchange(t, req, response, cfg, true)
			if got.Status != model.RunStatusInitFail || len(got.WorkMeasurements) != 0 || !strings.Contains(got.Reason, "invalid remote result") {
				t.Fatalf("altered peer time accepted: %+v", got)
			}
		})
	}
	req, response := peerTimeFixture(t, "interactive")
	cfg := timeMeasurementConfig()
	response = finishWorkMeasurementPeerTime(req, response, cfg)
	req.WorkMeasurementPeerTime = nil
	if got := peerTimeExchange(t, req, response, cfg, true); got.Status != model.RunStatusInitFail {
		t.Fatal("unsolicited per-role time accepted")
	}
	for i := range response.WorkMeasurements {
		response.WorkMeasurements[i].TimeBudget = nil
	}
	if got := finishWorkMeasurementPeerTime(req, response, durante.Config{}); !reflect.DeepEqual(got, response) {
		t.Fatal("unselected option changed existing results")
	}
}

func TestPeerTimeRequiresExactRoleBudgetsBeforeUpload(t *testing.T) {
	for name, edit := range map[string]func(*model.RunRequest){
		"missing": func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets = r.WorkMeasurementPeerTime.Budgets[:2] },
		"extra": func(r *model.RunRequest) {
			r.WorkMeasurementPeerTime.Budgets = append(r.WorkMeasurementPeerTime.Budgets, model.PeerTimeBudget{Role: "contestant", ProgramID: "unused", TimeMs: 1})
		},
		"wrong program":  func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[0].ProgramID = "unused" },
		"duplicate seat": func(r *model.RunRequest) { r.WorkMeasurementPeerTime.Budgets[2] = r.WorkMeasurementPeerTime.Budgets[1] },
	} {
		t.Run(name, func(t *testing.T) {
			req, response := peerTimeFixture(t, "communication-v1")
			edit(req)
			if got := peerTimeExchange(t, req, response, timeMeasurementConfig(), false); got.Status != model.RunStatusInitFail {
				t.Fatal("invalid role budget request uploaded")
			}
		})
	}
}

func peerTimeSharedConfig() durante.Config {
	cfg := timeMeasurementConfig()
	cfg.Families = []string{"cpp", "cpython"}
	cfg.TimePolicy = nil
	cfg.TimeModels = &durante.TimeModels{Schema: 1, Models: []durante.TimeModel{{
		ModelID: "shared-unit", ModelVersion: 1, ReferenceID: "synthetic-only",
		Coefficients: durante.TimeCoefficients{InstructionNSNumerator: 1, REPByteNSNumerator: 1, NSDenominator: 1},
	}}}
	for _, source := range []string{"CPP20", "PYTHON3"} {
		family, _ := durante.FamilyForLanguage(source)
		cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, durante.TimeProfileBinding{
			SourceLanguage: source, Family: family, RuntimeImage: cfg.RuntimeImage,
			EngineProfile: durante.EngineProfile, DispatchProfile: durante.DispatchDefault,
			ModelID: "shared-unit", ModelVersion: 1, QualificationID: "synthetic-" + source,
		})
	}
	return cfg
}

func TestPeerTimeBindsEveryRoleLanguageBeforeUploadOrExecution(t *testing.T) {
	req, response := peerTimeFixture(t, "interactive")
	req.Interactor.Lang = "PYTHON3"
	response.WorkMeasurements[1].Report.Family = "cpython"
	cfg := peerTimeSharedConfig()
	got := finishWorkMeasurementPeerTime(req, response, cfg)
	first, second := got.WorkMeasurements[0].TimeBudget.Estimate, got.WorkMeasurements[1].TimeBudget.Estimate
	if first == nil || second == nil || first.ModelID != second.ModelID || !strings.HasPrefix(first.RuntimeID, "CPP20@") || !strings.HasPrefix(second.RuntimeID, "PYTHON3@") || second.Family != "cpython" {
		t.Fatal("peer source profiles did not bind independently to the shared model")
	}
	if remote := peerTimeExchange(t, req, got, cfg, true); !reflect.DeepEqual(remote, got) {
		t.Fatal("cross-language shared-model result rejected")
	}
	cfg.TimeModels.Profiles = cfg.TimeModels.Profiles[:1]
	if rejected := peerTimeExchange(t, req, response, cfg, false); rejected.Status != model.RunStatusInitFail {
		t.Fatal("missing peer model did not fail before upload")
	}
	service := NewWithConfig(config.Config{Execution: config.ExecutionConfig{WorkMeasurement: cfg}})
	if rejected := service.Run(context.Background(), req, Hooks{}); rejected.Status != model.RunStatusInitFail || !strings.Contains(rejected.Reason, "approved profile") {
		t.Fatalf("local admission reached execution without a model for every role: %+v", rejected)
	}
}

// Synthetic weights test exact plumbing against the observed counts. They do
// not qualify an execution-time model for either source-language profile.
func peerTimeIntegrationConfig(t *testing.T, cfg durante.Config) durante.Config {
	t.Helper()
	cfg.RuntimeImage = os.Getenv("AONOHAKO_DURANTE_RUNTIME_IMAGE")
	cfg.ResourcePolicy = timeMeasurementConfig().ResourcePolicy
	cfg.TimePolicy = nil
	cfg.TimeModels = &durante.TimeModels{Schema: 1, Models: []durante.TimeModel{{
		ModelID: "peer-arithmetic-fixture", ModelVersion: 1, ReferenceID: "synthetic-only",
		Method:       durante.TimeMethodREPBreakdown,
		Coefficients: durante.TimeCoefficients{InstructionNSNumerator: 1, MOVSByteNSNumerator: 2, STOSByteNSNumerator: 3, NSDenominator: 1},
	}}}
	for _, language := range []string{"CPP20", "C11"} {
		cfg.TimeModels.Profiles = append(cfg.TimeModels.Profiles, durante.TimeProfileBinding{
			SourceLanguage: language, Family: "cpp", RuntimeImage: cfg.RuntimeImage,
			EngineProfile: durante.EngineProfile, DispatchProfile: cfg.DispatchProfile,
			ModelID: "peer-arithmetic-fixture", ModelVersion: 1, QualificationID: "synthetic-" + language,
		})
	}
	if err := cfg.ValidateTimeConfiguration(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func addPeerTimeIntegrationBudgets(t *testing.T, req *model.RunRequest) {
	t.Helper()
	roles, err := workMeasurementRequestRoles(req)
	if err != nil {
		t.Fatal(err)
	}
	req.WorkMeasurementPeerTime = &model.PeerTimeRequest{Version: 1}
	for i := len(roles) - 1; i >= 0; i-- {
		role := roles[i]
		req.WorkMeasurementPeerTime.Budgets = append(req.WorkMeasurementPeerTime.Budgets, model.PeerTimeBudget{
			Role: role.role, ProgramID: role.programID, ParticipantIndex: role.participantIndex, TimeMs: i + 1,
		})
	}
}

func assertPeerTimeIntegrationBudgets(t *testing.T, req *model.RunRequest, response model.RunResponse, cfg durante.Config) {
	t.Helper()
	for i, process := range response.WorkMeasurements {
		result, report := process.TimeBudget, process.Report
		if result == nil || result.Estimate == nil || result.Exceeded == nil || result.Unavailable != "" || result.BudgetMs != i+1 || result.Authority != durante.Authority {
			t.Fatalf("role %d has no complete diagnostic budget: %+v", i, process)
		}
		if report == nil || report.Instructions == nil || report.REPBreakdown == nil || report.SignalsObserved != 0 || report.REPTelemetryStatus != durante.REPMovsStosOnly {
			t.Fatalf("role %d has no convertible raw observation: %+v", i, report)
		}
		// This fixture is bounded far below uint64/int64 overflow; compute the
		// arithmetic independently of the production calculator.
		wantNS := int64(*report.Instructions + 2*report.REPBreakdown.MOVSBytes + 3*report.REPBreakdown.STOSBytes)
		if result.Estimate.Nanoseconds != wantNS || *result.Exceeded != (wantNS > int64(i+1)*1_000_000) || result.Estimate.Method != durante.TimeMethodREPBreakdown {
			t.Fatalf("role %d has an incorrect estimate or budget comparison: %+v", i, result)
		}
	}
	if err := validateRemoteWorkMeasurementPeerTime(req, response, cfg); err != nil {
		t.Fatalf("actual role estimates violate the forwarding contract: %v", err)
	}
}
