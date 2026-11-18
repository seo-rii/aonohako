package execute

import (
	"context"
	"crypto/sha256"
	"os"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/timing"
)

// Replay only the contestant, with a private peer that follows the completed
// native exchange. The caller has already finished the original judgment.
func (s *Service) replayInteractiveContestant(ctx context.Context, single *model.RunRequest, trace *interactiveTranscript, outputs [2][sha256.Size]byte, tuning config.RuntimeTuningConfig) (*durante.Report, string) {
	if ctx.Err() != nil {
		return nil, "pipeline observation cancelled"
	}
	if single == nil || trace == nil || !trace.ready() || single.SPJ != nil || len(single.FileOutputs) > 1 ||
		s.workMeasurementPolicyError != nil || observationEligible(single, s.workMeasurement) != "" {
		return nil, "interactive contestant replay unavailable"
	}
	policy := s.workMeasurement.ResourcePolicy
	if policy.ValidateRequest(policy.MaxLimits) != nil {
		return nil, "interactive contestant resource policy unavailable"
	}
	replay := observationRequest(single, policy.MaxLimits)
	raw := workMeasurementRawRequest(&replay)
	workDir, ws, command, initFailure := prepareInteractiveCommand(&raw, tuning, "contestant")
	if initFailure != nil {
		return nil, "interactive contestant replay preparation failed"
	}
	defer os.RemoveAll(workDir)
	meter, err := s.prepareContestantDurante(ctx, ws, &raw)
	if err != nil || meter == nil {
		return nil, "interactive contestant instrumentation unavailable"
	}
	stdin, send, err := os.Pipe()
	if err != nil {
		return nil, "interactive contestant replay input unavailable"
	}
	read, stdout, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		_ = send.Close()
		return nil, "interactive contestant replay output unavailable"
	}
	capture := cappedBuffer{limit: outputLimitBytes(&raw)}
	executionCtx, start, cancel := sandboxRequestContext(ctx, &raw, timing.CPUNormalizer{})
	defer cancel()
	var matched bool
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		matched = trace.play(executionCtx, send, read, &capture)
		if !matched {
			cancel()
		}
	}()
	// Close the worker's pipe ends and join the live peer before collecting
	// the report. Early sandbox initialization failures also need this join.
	finishPeer := func() {
		_ = stdin.Close()
		_ = stdout.Close()
		<-peerDone
	}
	streams := sandboxStreamConfig{stdinFile: stdin, stdoutFile: stdout, onStdoutDone: finishPeer,
		startWallTimer: start, workMeasurement: meter}
	result := runCommandWithSandboxStreams(executionCtx, cancel, ws, command, &raw, streams, Hooks{}, outputLimitBytes(&raw), tuning, s.cgroupParentDir)
	finishPeer()
	if ctx.Err() != nil {
		return nil, "pipeline observation cancelled"
	}
	if !matched {
		return nil, "interactive contestant replay did not match the native exchange"
	}
	status, _, _ := classifyRunStatusWithoutOutput(&raw, result)
	if status != "OK" || result.ExitCode == nil || *result.ExitCode != 0 || capture.Truncated() || !observationReportMatches(single, s.workMeasurement, result.WorkMeasurement) {
		return nil, "interactive contestant replay did not produce a complete compatible measurement"
	}
	var file []byte
	if len(single.FileOutputs) != 0 {
		file, err = captureFileOutput(ws, single.FileOutputs[0])
		if err != nil {
			return nil, "interactive contestant replay output file unavailable"
		}
	}
	if pipelineOutputDigests(capture.Bytes(), file) != outputs {
		return nil, "interactive contestant replay output differs from native output"
	}
	return result.WorkMeasurement, ""
}
