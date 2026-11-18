package execute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"

	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/profiles"
	"aonohako/internal/runvalidation"
)

func automaticObservationRequested(req *model.RunRequest, cfg durante.Config) bool {
	return cfg.Observe && req != nil && !req.WorkMeasurement && !req.WorkMeasurementTime &&
		!req.WorkMeasurementJudge && !req.WorkMeasurementShadow && req.WorkMeasurementLimits == nil &&
		req.WorkMeasurementPeerTime == nil
}

func observationRequest(req *model.RunRequest, limits durante.ResourceLimits) model.RunRequest {
	replay := *req
	if replay.SPJ != nil {
		// The native checker owns the score. Its answer file is not a stdout oracle.
		replay.SPJ, replay.ExpectedStdout = nil, ""
		// Admission may set library_mode for the Python checker alone.
		if !runvalidation.UsesPython(&replay) {
			replay.PythonLibraryMode = ""
		}
	}
	replay.WorkMeasurement = true
	replay.WorkMeasurementLimits = &limits
	return replay
}

// observationEligible is shared with remote validation and request budgeting.
func observationEligible(req *model.RunRequest, cfg durante.Config) string {
	if req.Interactor != nil || req.Communication != nil || req.Pipeline != nil ||
		req.EntryMode != "" || len(req.Programs) != 0 || len(req.Steps) != 0 {
		return "observation requires a standalone contestant"
	}
	if req.EnableNetwork || req.IgnoreTLE || len(req.SidecarOutputs) != 0 ||
		(req.SPJ != nil && len(req.SPJ.SidecarOutputs) != 0) {
		return "submission is not replayable for observation"
	}
	if strings.TrimSpace(req.ExpectedStdoutURL) != "" ||
		(req.SPJ != nil && req.SPJ.Binary != nil && strings.TrimSpace(req.SPJ.Binary.DataURL) != "") {
		return "replay artifacts must first be resolved"
	}
	for _, binary := range req.Binaries {
		if strings.TrimSpace(binary.DataURL) != "" {
			return "replay artifacts must first be resolved"
		}
	}
	if _, err := cfg.Family(req.Lang); err != nil {
		return err.Error()
	}
	if cfg.ResourcePolicy == nil {
		return "observation resource policy is unavailable"
	}
	replay := observationRequest(req, cfg.ResourcePolicy.MaxLimits)
	if err := runvalidation.ValidateWorkMeasurementLimits(&replay); err != nil {
		return err.Error()
	}
	return ""
}

// observationOutputCheck identifies which output evidence is available for
// this primary. A program stopped by its CPU budget may have written nothing.
func observationOutputCheck(req *model.RunRequest, primary model.RunResponse) string {
	if primary.Status == model.RunStatusTLE {
		if req.SPJ != nil || len(req.FileOutputs) != 0 || strings.TrimRight(req.ExpectedStdout, " \t\r\n") == "" {
			return ""
		}
		// Polling and final accounting measured contestant CPU. Wall timeouts,
		// cancelled executions, and a signal alone do not establish that.
		switch primary.VerdictSource {
		case "cpu_time", "cpu_time_cgroup", "cpu_time_final", "cpu_time_cgroup_final":
			return "expected_output"
		}
		return ""
	}
	if (primary.Status != model.RunStatusAccepted && primary.Status != model.RunStatusWA) ||
		primary.ExitCode == nil || *primary.ExitCode != 0 {
		return ""
	}
	source := "stdout"
	if req.SPJ != nil {
		source = "spj"
	} else if len(req.FileOutputs) != 0 {
		source = "file_output"
	}
	if primary.VerdictSource == source {
		return "native_output"
	}
	return ""
}

func observationReportMatches(req *model.RunRequest, cfg durante.Config, report *durante.Report) bool {
	dispatch := cfg.DispatchProfile
	if dispatch == "" {
		dispatch = durante.DispatchDefault
	}
	expectedFeatures := slices.Clone(req.DisallowedCPUFeatures)
	slices.Sort(expectedFeatures)
	return validDiagnosticWorkReport(req.Lang, report) && slices.Equal(report.DisallowedCPUFeatures, expectedFeatures) &&
		report.CPUIDProfile == cfg.CPUIDProfile &&
		report.DispatchProfile == dispatch && (report.LatencyBound != nil) == cfg.LatencyBound &&
		report.RuntimeDeterminism == cfg.RuntimeDeterminismFor(profiles.NormalizeRunLang(req.Lang))
}

type observationOutputKey struct{}

// Each ordinary short-case sample must agree on the contestant's judged bytes.
// Capture before the SPJ and before response truncation or AC output suppression.
type observationOutput struct {
	digest   [sha256.Size]byte
	seen     bool
	diverged bool
}

func (o *observationOutput) record(output []byte) {
	digest := sha256.Sum256(output)
	if o.seen && digest != o.digest {
		o.diverged = true
	}
	o.digest, o.seen = digest, true
}

func (s *Service) runWithObservation(ctx context.Context, req *model.RunRequest, hooks Hooks,
	run func(context.Context, *model.RunRequest, Hooks) model.RunResponse) model.RunResponse {
	reason := observationEligible(req, s.workMeasurement)
	if reason == "" && s.workMeasurementPolicyError != nil {
		reason = "observation resource policy is unavailable"
	}
	var snapshot *stdinSnapshot
	var nativeOutput observationOutput
	if reason == "" {
		if strings.TrimSpace(req.StdinURL) != "" {
			snapshot = &stdinSnapshot{hash: sha256.New()}
			defer snapshot.close()
			ctx = context.WithValue(ctx, shadowStdinKey{}, snapshot)
		}
		ctx = context.WithValue(ctx, observationOutputKey{}, &nativeOutput)
	}
	primary := run(ctx, req, hooks)
	observation := &model.WorkMeasurementObservation{Version: 1, Authority: durante.Authority, State: "unavailable"}
	primary.WorkMeasurementObservation = observation
	outputCheck := observationOutputCheck(req, primary)
	switch {
	case reason != "":
		observation.Reason = reason
	case ctx.Err() != nil:
		observation.Reason = "request cancelled before observation"
	case outputCheck == "":
		observation.Reason = "native verdict has no eligible output comparison"
	case snapshot != nil && snapshot.path == "":
		observation.Reason = "native contestant did not acquire the URL input"
	case outputCheck == "native_output" && (!nativeOutput.seen || nativeOutput.diverged):
		observation.Reason = "native contestant output is unavailable or inconsistent"
	default:
		replay := observationRequest(req, s.workMeasurement.ResourcePolicy.MaxLimits)
		var replayOutput observationOutput
		replayCtx := context.WithValue(ctx, observationOutputKey{}, &replayOutput)
		measured := run(replayCtx, &replay, Hooks{})
		envelope := measured.WorkMeasurementExecution
		switch {
		case ctx.Err() != nil:
			observation.Reason = "request cancelled during observation"
		case envelope == nil:
			observation.Reason = "measurement did not produce a diagnostic execution"
		case envelope.State != "complete":
			observation.Reason = "measurement execution " + envelope.State
		case !observationReportMatches(req, s.workMeasurement, measured.WorkMeasurement):
			observation.Reason = "measurement did not produce a compatible report"
		case !replayOutput.seen || replayOutput.diverged:
			observation.Reason = "replayed contestant output is unavailable or inconsistent"
		case outputCheck == "native_output" && nativeOutput.digest != replayOutput.digest:
			observation.Reason = "replayed contestant output differs from native output"
		case outputCheck == "expected_output" && (envelope.TerminationSource != "stdout" || envelope.OutputMatches == nil || !*envelope.OutputMatches):
			observation.Reason = "replayed contestant stdout did not match the expected output"
		default:
			observation.State, observation.Report, observation.OutputCheck = "complete", measured.WorkMeasurement, outputCheck
			if snapshot != nil {
				observation.StdinSHA256 = hex.EncodeToString(snapshot.hash.Sum(nil))
			}
		}
	}
	return primary
}
