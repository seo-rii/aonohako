package execute

import (
	"errors"
	"fmt"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

func validWorkClockIdentity(req *model.RunRequest, r *durante.Report) bool {
	family, ok := durante.FamilyForLanguage(req.Lang)
	return ok && r != nil && r.Family == family && r.EngineProfile == durante.EngineProfile &&
		r.ValidInstallationIdentity() && r.ValidDiagnosticContract()
}

func terminalWorkClockMatches(req *model.RunRequest, plan *durante.WorkClockRequest, report *durante.Report, progress *durante.WorkClockProgress) bool {
	if !validDiagnosticWorkReport(req.Lang, report) || progress == nil || !progress.Matches(report) {
		return false
	}
	if policy := plan.TimePolicy; policy != nil {
		_, _, err := policy.EstimateWithEligibility(policy.RuntimeID, *report)
		return err == nil
	}
	return true
}

// Finalize after the target has been reaped and its installation was checked.
// A live cutoff has only a prefix; a natural exit must reconcile every component
// before the observation can be used, including exits between watchdog ticks.
func finishWorkClock(req *model.RunRequest, plan *durante.WorkClockRequest, response model.RunResponse, progress *durante.WorkClockProgress) model.RunResponse {
	d := response.WorkMeasurementExecution
	if !validWorkClockIdentity(req, response.WorkMeasurement) {
		progress = nil
	}
	d.Clock = plan.Receipt(progress)
	switch {
	case d.State == "complete":
		if !terminalWorkClockMatches(req, plan, response.WorkMeasurement, progress) {
			d.State = "incomplete"
			d.Clock = plan.Receipt(nil)
			response.Reason = "Durante work clock unavailable or inconsistent with terminal counters"
		} else if plan.Exceeds(*progress) {
			d.State = "budget_exceeded"
			d.TerminationSource = "durante_work_clock_final"
			response.Reason = "Durante work budget exceeded at target exit"
		} else {
			return response
		}
	case d.State == "resource_exhausted" && d.TerminationSource == "durante_work_clock":
		if progress == nil || !plan.Exceeds(*progress) {
			d.State = "incomplete"
			d.TerminationSource = "durante_work_clock_unavailable"
			d.Clock = plan.Receipt(nil)
			response.Reason = "Durante work clock unavailable after stop"
		} else {
			d.State = "budget_exceeded"
			response.Reason = "Durante work budget exceeded during execution"
		}
	default:
		// An independent raw limit, cancellation or target failure wins even
		// when the final clock snapshot also crossed the budget.
		return response
	}
	if d.TerminationSource != "durante_work_clock_final" {
		response.WorkMeasurement = response.WorkMeasurement.WithoutTotals(response.Reason)
		d.OutputMatches = nil
	}
	response.VerdictSource = "durante_" + d.State
	return response
}

func validateRemoteWorkClock(req *model.RunRequest, response model.RunResponse, plan *durante.WorkClockRequest) error {
	d := response.WorkMeasurementExecution
	if d == nil { // Local admission or remote initialization can fail before launch.
		return nil
	}
	clockSource := d.TerminationSource == "durante_work_clock" || d.TerminationSource == "durante_work_clock_final"
	if plan == nil {
		if d.Clock != nil || d.State == "budget_exceeded" || clockSource {
			return fmt.Errorf("invalid remote result: unsolicited work clock")
		}
		return nil
	}
	r := d.Clock
	if r == nil || r.Version != 1 || r.Metric != plan.MetricName() || r.ModelBinding != plan.ModelBinding() || r.Budget != plan.Budget {
		return fmt.Errorf("invalid remote result: missing or mismatched Durante work clock")
	}
	if (r.State != "observed" && r.State != "unavailable") || (r.Progress != nil) != (r.State == "observed") ||
		(r.Progress != nil && (!r.Progress.Valid() || !validWorkClockIdentity(req, response.WorkMeasurement))) {
		return fmt.Errorf("invalid remote result: invalid Durante work clock progress")
	}
	switch d.State {
	case "complete":
		if !terminalWorkClockMatches(req, plan, response.WorkMeasurement, r.Progress) || plan.Exceeds(*r.Progress) {
			return fmt.Errorf("invalid remote result: terminal work clock mismatch or exceeded budget")
		}
	case "budget_exceeded":
		if !clockSource || r.Progress == nil || !plan.Exceeds(*r.Progress) {
			return fmt.Errorf("invalid remote result: unverified Durante work budget stop")
		}
		if d.TerminationSource == "durante_work_clock_final" {
			if response.ExitCode == nil || *response.ExitCode != 0 || d.OutputMatches == nil || !terminalWorkClockMatches(req, plan, response.WorkMeasurement, r.Progress) {
				return fmt.Errorf("invalid remote result: missing final work budget evidence")
			}
		} else if response.ExitCode != nil || d.OutputMatches != nil || !response.WorkMeasurement.ValidUnavailableResult() {
			return fmt.Errorf("invalid remote result: interrupted work budget claims terminal evidence")
		}
	default:
		if clockSource {
			return fmt.Errorf("invalid remote result: work clock stop state mismatch")
		}
	}
	return nil
}

// The helper owns its process group and its seccomp policy forbids changing
// groups. After the root is waited for, lingering descendants must disappear
// before a clock can claim terminal progress or a clean budget stop.
func workClockGroupStopped(pgid int) bool {
	deadline := time.Now().Add(250 * time.Millisecond)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		if err != nil || !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func rejectWorkClockCleanup(result *execResult) {
	result.workClockProgress = nil
	result.Status, result.VerdictSource = model.RunStatusInitFail, "durante_work_clock_cleanup"
	result.Reason = "could not confirm cleanup of Durante work clock execution"
}

// A group signal may reach surviving children of an already exited root.
// Polling the launch pidfd also recognizes an unreaped zombie. ESRCH between
// poll and send leaves attribution to the normal wait path.
func signalWorkClockRoot(fd int) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(fds, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, err
		}
		if fds[0].Revents&(unix.POLLERR|unix.POLLNVAL) != 0 {
			return false, fmt.Errorf("unavailable work clock pidfd")
		}
		if n != 0 {
			return false, nil
		}
		err = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
		if errors.Is(err, unix.ESRCH) {
			return false, nil
		}
		return err == nil, err
	}
}
