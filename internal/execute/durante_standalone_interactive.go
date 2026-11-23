package execute

import (
	"context"
	"crypto/sha256"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

// Only the native interactor receives the problem's input and answer. The
// contestant receives its stdin exclusively from the recorded dialogue.
func standaloneInteractiveContestant(req *model.RunRequest) *model.RunRequest {
	if req == nil || req.Interactor == nil || req.Communication != nil || req.Pipeline != nil ||
		runvalidation.UsesSteps(req) || req.EntryMode != "" || req.SPJ != nil || req.EnableNetwork ||
		req.IgnoreTLE || len(req.SidecarOutputs) != 0 || len(req.FileOutputs) != 0 {
		return nil
	}
	single := *req
	single.Interactor = nil
	single.Stdin, single.StdinURL, single.ExpectedStdout, single.ExpectedStdoutURL = "", "", "", ""
	if !runvalidation.UsesPython(&single) {
		single.PythonLibraryMode = ""
	}
	return &single
}

func completedStandaloneInteractiveExchange(primary model.RunResponse) bool {
	if len(primary.Steps) != 2 || primary.Score == nil || primary.ExitCode == nil || *primary.ExitCode != 0 {
		return false
	}
	contestant, interactor := primary.Steps[0], primary.Steps[1]
	if contestant.ID != "contestant" || contestant.ProgramID != "contestant" || contestant.VerdictSource != "contestant" ||
		contestant.Status != model.RunStatusAccepted || contestant.ExitCode == nil || *contestant.ExitCode != 0 ||
		interactor.ID != "interactor" || interactor.ProgramID != "interactor" || interactor.VerdictSource != "interactor:exit_code" ||
		interactor.StdoutTruncated || !completedInteractorVerdict(primary.Status, primary.VerdictSource, interactor.Status, interactor.ExitCode) {
		return false
	}
	// A contestant stdout overflow would have failed its role. Only display
	// truncation is possible here. Native AC suppresses stdout at the top level.
	if primary.Status == model.RunStatusAccepted {
		return *primary.Score == 1 && !primary.StdoutTruncated
	}
	return *primary.Score == 0 && primary.StdoutTruncated == contestant.StdoutTruncated
}

type standaloneInteractiveCaptureKey struct{}

type standaloneInteractiveCapture struct {
	trace   interactiveTranscript
	tuning  config.RuntimeTuningConfig
	outputs [2][sha256.Size]byte
}

func (s *Service) runWithStandaloneInteractiveObservation(ctx context.Context, req *model.RunRequest, hooks Hooks,
	run func(context.Context, *model.RunRequest, Hooks) model.RunResponse) model.RunResponse {
	single := standaloneInteractiveContestant(req)
	budget := stepObservationBudgetFromContext(ctx)
	var capture *standaloneInteractiveCapture
	if single != nil && budget.allowsReplay() && s.workMeasurementPolicyError == nil && observationEligible(single, s.workMeasurement) == "" {
		capture = &standaloneInteractiveCapture{}
		ctx = context.WithValue(ctx, standaloneInteractiveCaptureKey{}, capture)
	}
	primary := run(ctx, req, hooks)
	observation := &model.WorkMeasurementObservation{Version: 2, Authority: durante.Authority, State: "interactive"}
	primary.WorkMeasurementObservation = observation
	if len(primary.Steps) == 0 {
		return primary // Native initialization returned before either role produced a result.
	}
	row := model.PipelineStepObservation{StepID: "contestant", ProgramID: "contestant", State: "unavailable"}
	switch {
	case ctx.Err() != nil:
		row.Reason = "interactive observation cancelled"
	case capture == nil:
		row.Reason = "interactive contestant observation is unavailable for this request"
	case !completedStandaloneInteractiveExchange(primary) || !capture.trace.ready():
		row.Reason = "native interactive dialogue is unavailable for replay"
	default:
		replayCtx, cancel := budget.start(ctx)
		defer cancel()
		row.Report, row.Reason = s.replayInteractiveContestant(replayCtx, single, &capture.trace, capture.outputs, capture.tuning)
		if replayCtx.Err() != nil {
			row.Report, row.Reason = nil, "interactive observation cancelled"
		}
		if row.Report != nil {
			row.State, row.OutputCheck = "complete", "interactive_transcript"
		}
	}
	observation.Steps = []model.PipelineStepObservation{row}
	return primary
}
