package execute

import (
	"fmt"

	"aonohako/internal/model"
)

// Clock evidence on each role is checked before this decision, locally and at
// the forwarding boundary. A coordinator cancellation alone can never confirm
// that the contestant finished an accepted exchange within budget.
func interactivePeerClockVerdict(rows []model.WorkMeasurementProcess, steps []model.StepResult) (status, source string) {
	c, j := rows[0], rows[1]
	for _, r := range []model.WorkMeasurementProcess{j, c} {
		if r.ExecutionStatus == model.RunStatusInitFail && r.TerminationSource != "durante_peer_cancelled" {
			return model.RunStatusInitFail, prefixInteractiveSource(r.Role, r.TerminationSource)
		}
	}
	if j.ExecutionStatus == model.RunStatusTLE {
		return model.RunStatusInitFail, prefixInteractiveSource("interactor", j.TerminationSource)
	}
	if j.ExecutionStatus == "OK" && j.ExitCode != nil {
		if *j.ExitCode == 3 {
			return model.RunStatusInitFail, "interactor:exit_code"
		}
		if steps[1].VerdictSource == "interactor:stdout_limit" && steps[1].StdoutTruncated {
			return model.RunStatusInitFail, "interactor:stdout_limit"
		}
	}
	if c.ExecutionStatus == model.RunStatusTLE {
		return model.RunStatusTLE, prefixInteractiveSource("contestant", c.TerminationSource)
	}
	if c.ExecutionStatus == "OK" && c.ExitCode != nil {
		if *c.ExitCode != 0 {
			return model.RunStatusRE, "contestant:exit_code"
		}
		if steps[0].VerdictSource == "contestant:stdout_limit" && steps[0].StdoutTruncated {
			return model.RunStatusRE, "contestant:stdout_limit"
		}
	}
	if j.ExecutionStatus == "OK" && j.ExitCode != nil {
		if *j.ExitCode != 0 {
			return model.RunStatusWA, "interactor:exit_code"
		}
		if c.ExecutionStatus == "OK" && c.ExitCode != nil {
			return model.RunStatusAccepted, "interactor"
		}
	}
	for _, r := range []model.WorkMeasurementProcess{c, j} {
		if r.TerminationSource == "durante_peer_cancelled" {
			return model.RunStatusInitFail, prefixInteractiveSource(r.Role, r.TerminationSource)
		}
	}
	return model.RunStatusInitFail, "interactive:durante_peer_clock_error"
}

func validateInteractivePeerClockVerdict(response model.RunResponse) error {
	if len(response.WorkMeasurements) == 0 && response.Status == model.RunStatusInitFail && response.Score == nil {
		return nil // Failure during admission or workspace preparation.
	}
	if len(response.WorkMeasurements) != 2 || len(response.Steps) != 2 {
		return fmt.Errorf("invalid remote result: missing interactive work clock roles")
	}
	for i, role := range []string{"contestant", "interactor"} {
		row, step := response.WorkMeasurements[i], response.Steps[i]
		if b := row.TimeBudget; b != nil && b.RawUsage != nil {
			u := b.RawUsage
			if !equalPipelineCounter(u.CPUTimeNs, step.CPUTimeNs) || u.WallTimeMs == nil || *u.WallTimeMs != step.WallTimeMs ||
				u.MemoryKB == nil || *u.MemoryKB != step.MemoryKB {
				return fmt.Errorf("invalid remote result: inconsistent interactive clock resource usage")
			}
		}
		// Step stdout truncation also includes clipping for the response. Only
		// the executor's stdout_limit source denotes an execution output limit.
		raw := execResult{Status: row.ExecutionStatus, ExitCode: row.ExitCode, VerdictSource: row.TerminationSource,
			StdoutTruncated: step.StdoutTruncated && step.VerdictSource == role+":stdout_limit"}
		var expected model.StepResult
		if i == 0 {
			expected = interactiveContestantStepResult(&model.RunRequest{}, raw)
		} else {
			expected = interactiveInteractorStepResult(&model.RunRequest{}, raw)
		}
		if step.Status != expected.Status || step.VerdictSource != expected.VerdictSource {
			return fmt.Errorf("invalid remote result: inconsistent interactive clock step status")
		}
		if row.Role != role || step.ID != role || step.ProgramID != role ||
			(row.ExitCode == nil) != (step.ExitCode == nil) ||
			(row.ExitCode != nil && *row.ExitCode != *step.ExitCode) {
			return fmt.Errorf("invalid remote result: inconsistent interactive work clock role")
		}
	}
	status, source := interactivePeerClockVerdict(response.WorkMeasurements, response.Steps)
	if status != response.Status || source != response.VerdictSource ||
		((response.Score == nil) != (status == model.RunStatusInitFail)) {
		return fmt.Errorf("invalid remote result: inconsistent interactive clock verdict")
	}
	if response.Score != nil {
		want := 0.0
		if status == model.RunStatusAccepted {
			want = 1
		}
		if *response.Score != want {
			return fmt.Errorf("invalid remote result: inconsistent interactive clock score")
		}
	}
	return nil
}
