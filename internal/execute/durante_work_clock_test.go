package execute

import (
	"context"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
	"testing"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

func TestWorkClockDiagnosticAndRemote(t *testing.T) {
	const instructions = 1234
	zero := 0
	req := envelopeRequest()
	req.WorkMeasurementClock = &durante.WorkClockRequest{Version: 1, Budget: instructions}
	for _, tc := range []struct {
		name, status, source               string
		live                               *durante.WorkClockProgress
		expectedState, expectedTermination string
		exit                               *int
	}{
		{"exact at exit", model.RunStatusAccepted, "stdout", &durante.WorkClockProgress{Instructions: instructions}, "complete", "stdout", &zero},
		{"unsupported client", model.RunStatusAccepted, "stdout", nil, "incomplete", "stdout", &zero},
		{"inconsistent final", model.RunStatusAccepted, "stdout", &durante.WorkClockProgress{Instructions: instructions - 1}, "incomplete", "stdout", &zero},
		{"live cutoff", model.RunStatusTLE, "durante_work_clock", &durante.WorkClockProgress{Instructions: instructions + 1}, "budget_exceeded", "durante_work_clock", nil},
		{"lost progress", model.RunStatusTLE, "durante_work_clock", nil, "incomplete", "durante_work_clock_unavailable", nil},
		{"budget equality cannot stop", model.RunStatusTLE, "durante_work_clock", &durante.WorkClockProgress{Instructions: instructions}, "incomplete", "durante_work_clock_unavailable", nil},
		{"wall wins", model.RunStatusTLE, "wall_time", &durante.WorkClockProgress{Instructions: instructions + 1}, "resource_exhausted", "wall_time", nil},
		{"raw CPU wins", model.RunStatusTLE, "cpu_time", &durante.WorkClockProgress{Instructions: instructions + 1}, "resource_exhausted", "cpu_time", nil},
		{"memory wins", model.RunStatusMLE, "memory_rss", &durante.WorkClockProgress{Instructions: instructions + 1}, "resource_exhausted", "memory_rss", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := completeEnvelopeReport()
			report.REPBreakdown = &durante.REPBreakdown{Version: 1}
			resp := finishWorkMeasurementDiagnostic(context.Background(), req, model.RunResponse{Status: tc.status, VerdictSource: tc.source, WorkMeasurement: report, ExitCode: tc.exit})
			resp = finishWorkClock(req, req.WorkMeasurementClock, resp, tc.live)
			d := resp.WorkMeasurementExecution
			if d.State != tc.expectedState || d.TerminationSource != tc.expectedTermination || resp.Status != model.RunStatusInitFail || resp.WorkMeasurement.Complete != (d.State == "complete") {
				t.Fatalf("unexpected envelope %+v, status %s", d, resp.Status)
			}
			if err := validateRemoteWorkMeasurement(req, resp); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkClock(req, resp, req.WorkMeasurementClock); err != nil {
				t.Fatal(err)
			}
			if err := validateRemoteWorkClock(req, resp, nil); err == nil {
				t.Fatal("accepted unsolicited receipt")
			}
			d.Clock = nil
			if err := validateRemoteWorkClock(req, resp, req.WorkMeasurementClock); err == nil {
				t.Fatal("ignored missing receipt")
			}
		})
	}
	report := completeEnvelopeReport()
	report.REPBreakdown = &durante.REPBreakdown{Version: 1}
	req.WorkMeasurementClock.Budget--
	resp := finishWorkMeasurementDiagnostic(context.Background(), req, model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "stdout", WorkMeasurement: report, ExitCode: &zero})
	resp = finishWorkClock(req, req.WorkMeasurementClock, resp, &durante.WorkClockProgress{Instructions: instructions})
	d := resp.WorkMeasurementExecution
	if d.State != "budget_exceeded" || d.TerminationSource != "durante_work_clock_final" || !resp.WorkMeasurement.Complete || d.OutputMatches == nil {
		t.Fatalf("missed final boundary: %+v", d)
	}
	if err := validateRemoteWorkClock(req, resp, req.WorkMeasurementClock); err != nil {
		t.Fatal(err)
	}
	conflict := resp
	conflict.CPUTimeNs = uint64(req.WorkMeasurementLimits.CPUTimeMs)*1_000_000 + 1
	if err := validateRemoteWorkMeasurement(req, conflict); err == nil {
		t.Fatal("accepted work cutoff over its raw CPU watchdog")
	}
	d.Clock.Progress.Instructions--
	if err := validateRemoteWorkClock(req, resp, req.WorkMeasurementClock); err == nil {
		t.Fatal("accepted false work cutoff")
	}
}

func TestWorkClockAdmission(t *testing.T) {
	req := envelopeRequest()
	req.WorkMeasurementClock = &durante.WorkClockRequest{Version: 1, Budget: 0}
	if err := runvalidation.ValidateWorkMeasurementLimits(req); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*model.RunRequest){
		func(r *model.RunRequest) { r.WorkMeasurementLimits = nil },
		func(r *model.RunRequest) { r.WorkMeasurement = false },
		func(r *model.RunRequest) { r.WorkMeasurementShadow = true },
		func(r *model.RunRequest) { r.WorkMeasurementJudge = true; r.WorkMeasurementTime = true },
		func(r *model.RunRequest) { r.Interactor = &model.InteractorSpec{} },
		func(r *model.RunRequest) { r.Communication = &model.CommunicationSpec{} },
		func(r *model.RunRequest) { r.Pipeline = &model.PipelineV1{} },
	} {
		copyReq := *req
		change(&copyReq)
		if err := runvalidation.ValidateWorkMeasurementLimits(&copyReq); err == nil {
			t.Fatal("accepted unsupported clock request")
		}
	}
}

func TestWorkClockSignalsLiveRootOnly(t *testing.T) {
	for _, command := range [][]string{{"sleep", "30"}, {"true"}} {
		t.Run(command[0], func(t *testing.T) {
			cmd := exec.Command(command[0], command[1:]...)
			fd := -1
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, PidFD: &fd}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				if fd >= 0 {
					_ = unix.Close(fd)
				}
			})
			if fd < 0 {
				t.Skip("kernel pidfd unavailable")
			}
			live := command[0] == "sleep"
			if live {
				if workClockGroupStopped(cmd.Process.Pid) {
					t.Fatal("acknowledged active group as stopped")
				}
			} else {
				poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
				if n, err := unix.Poll(poll, 2000); err != nil || n != 1 {
					t.Fatalf("wait for uncollected root: %d %v", n, err)
				}
			}
			if delivered, err := signalWorkClockRoot(fd); err != nil || delivered != live {
				t.Fatalf("signal delivery=%v err=%v", delivered, err)
			}
			_ = cmd.Wait()
			if !workClockGroupStopped(cmd.Process.Pid) {
				t.Fatal("reaped group reported active")
			}
			if delivered, err := signalWorkClockRoot(fd); err != nil || delivered {
				t.Fatalf("signaled reaped root: %v %v", delivered, err)
			}
		})
	}
}
