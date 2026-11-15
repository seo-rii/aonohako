package execute

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func setREPBreakdown(r *durante.Report, n uint64) {
	bytes, entries := 131*n, 6*n
	r.REPBytes, r.REPEntries = &bytes, &entries
	r.REPBreakdown = &durante.REPBreakdown{Version: 1, MOVSBytes: 65 * n, STOSBytes: 66 * n, MOVSEntries: 3 * n, STOSEntries: 3 * n}
}

func TestRemoteWorkMeasurementREPBreakdown(t *testing.T) {
	for _, mode := range []string{"ordinary", "envelope", "time", "interactive", "communication-v1", "communication-v2"} {
		for _, detail := range []string{"legacy absent", "explicit zero", "mixed REP"} {
			t.Run(mode+"/"+detail, func(t *testing.T) {
				req := &model.RunRequest{Lang: "CPP20", WorkMeasurement: true}
				local := model.RunResponse{Status: model.RunStatusAccepted, WorkMeasurement: completeEnvelopeReport()}
				reports := []*durante.Report{local.WorkMeasurement}
				if mode == "envelope" {
					req = envelopeRequest()
				} else if mode == "time" {
					req = timeMeasurementRequest()
				} else if mode != "ordinary" {
					req = peerMeasurementRequest(mode)
					local = completePeerMeasurementResponse(t, req)
					reports = nil
					for _, role := range local.WorkMeasurements {
						reports = append(reports, role.Report)
					}
				}
				for i, report := range reports {
					if detail == "explicit zero" {
						setREPBreakdown(report, 0)
					} else if detail == "mixed REP" {
						// Different role totals detect accidental peer aggregation.
						setREPBreakdown(report, uint64(i+1))
					}
				}
				if mode == "envelope" || mode == "time" {
					exit := 0
					local.ExitCode, local.VerdictSource = &exit, "stdout"
					local = finishWorkMeasurementDiagnostic(context.Background(), req, local)
					if mode == "time" {
						local = finishWorkMeasurementTime(req, local, timeMeasurementConfig())
						if local.WorkMeasurementExecution.TimeEstimate == nil {
							t.Fatal("fixture did not produce a time estimate")
						}
					}
				}
				got := exchangeTimeMeasurementResult(t, req, local, timeMeasurementConfig())
				if !reflect.DeepEqual(got, local) {
					t.Fatalf("SSE forwarding changed REP detail or execution: local=%+v remote=%+v", local, got)
				}
				if detail != "mixed REP" {
					return
				}
				for _, report := range reports {
					report.REPBreakdown.MOVSBytes++
					got := exchangeTimeMeasurementResult(t, req, local, timeMeasurementConfig())
					if got.Status != model.RunStatusInitFail || got.WorkMeasurement != nil || len(got.WorkMeasurements) != 0 || !strings.Contains(got.Reason, "invalid remote result") {
						t.Fatalf("inconsistent REP partition survived forwarding: %+v", got)
					}
					report.REPBreakdown.MOVSBytes--
				}
			})
		}
	}
}

func TestWorkMeasurementAbortDiscardsREPBreakdown(t *testing.T) {
	for _, state := range []string{"resource_exhausted", "failed", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			r := completeEnvelopeReport()
			setREPBreakdown(r, 1)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			status := model.RunStatusTLE
			if state == "failed" {
				status = model.RunStatusRE
			} else if state == "cancelled" {
				cancel()
			}
			req := envelopeRequest()
			got := finishWorkMeasurementDiagnostic(ctx, req, model.RunResponse{Status: status, WorkMeasurement: r})
			if got.WorkMeasurementExecution.State != state || got.WorkMeasurement.REPBreakdown != nil || !got.WorkMeasurement.ValidUnavailableResult() {
				t.Fatalf("stopped execution retained REP detail: response=%+v report=%+v", got, got.WorkMeasurement)
			}
			if err := validateRemoteWorkMeasurement(req, got); err != nil {
				t.Fatal(err)
			}
			if r.REPBreakdown == nil || r.REPBreakdown.MOVSBytes != 65 {
				t.Fatal("discarding totals changed the original observation")
			}
		})
	}
}
