package execute

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"os"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// The native runner and the control plane use the same contestant, limits,
// outputs and final judge when attributing a legacy step.
func legacyStepRequest(req *model.RunRequest, index int) *model.RunRequest {
	if index < 0 || index >= len(req.Steps) {
		return nil
	}
	step := req.Steps[index]
	for _, program := range req.Programs {
		if strings.TrimSpace(program.ID) != strings.TrimSpace(step.ProgramID) {
			continue
		}
		single := pipelineProgramRequest(req, program, effectiveStepLimits(step))
		single.Stdin = step.Stdin
		if index == len(req.Steps)-1 {
			single.ExpectedStdout, single.ExpectedStdoutURL = req.ExpectedStdout, req.ExpectedStdoutURL
			single.SPJ, single.FileOutputs = req.SPJ, req.FileOutputs
			single.SidecarOutputs, single.IgnoreTLE = req.SidecarOutputs, req.IgnoreTLE
		} else if h := step.Handoff; h != nil {
			switch strings.ToLower(strings.TrimSpace(h.From)) {
			case "file", "file_output":
				single.FileOutputs = []model.OutputFile{{Path: h.Path}}
			}
		}
		return single
	}
	return nil
}

func legacyNativeStepOutput(single *model.RunRequest, row model.StepResult, final bool) bool {
	if row.ExitCode == nil || *row.ExitCode != 0 {
		return false
	}
	// On a stdout-judged WA (including SPJ), execution overflow would have
	// stopped judging with RE. A truncated public preview therefore still
	// has complete private bytes. A file verdict cannot disambiguate the flag.
	if row.StdoutTruncated && !(final && row.Status == model.RunStatusWA && len(single.FileOutputs) == 0) {
		return false
	}
	if !final {
		return row.Status == model.RunStatusAccepted && row.VerdictSource == ""
	}
	return observationOutputCheck(single, model.RunResponse{
		Status: row.Status, ExitCode: row.ExitCode, VerdictSource: row.VerdictSource,
	}) == "native_output"
}

// The legacy grammar allows direct URL stdin only on the first stage.
// Snapshot up to 8 MiB as the native download is consumed. Oversize or partial
// reads only disqualify the observation; the native reader keeps its original
// byte limit, error, and cumulative timeout.
const legacyURLSnapshotBytes = 8 << 20

type legacyURLSnapshot struct {
	source io.Reader
	bytes  []byte
	over   bool
	eof    bool
}

func (s *legacyURLSnapshot) Read(p []byte) (int, error) {
	n, err := s.source.Read(p)
	if !s.over {
		if n > legacyURLSnapshotBytes-len(s.bytes) {
			s.over, s.bytes = true, nil
		} else {
			s.bytes = append(s.bytes, p[:n]...)
		}
	}
	s.eof = err == io.EOF
	return n, err
}

type legacyStepReplay struct {
	request  *model.RunRequest
	outputs  [2][sha256.Size]byte
	maxBytes int64
	path     string
	url      *legacyURLSnapshot
}

func prepareLegacyReplay(req *model.RunRequest, reader io.Reader, maxBytes int64) (legacyStepReplay, io.Reader) {
	replay := legacyStepReplay{request: req, maxBytes: maxBytes}
	switch input := reader.(type) {
	case nil: // The original inline string is already in the step request.
	case *os.File:
		// Both assembled stdin_parts and handoff files belong to the request's
		// private directory and outlive the native descriptor.
		replay.path = input.Name()
	default:
		replay.url = &legacyURLSnapshot{source: input}
		return replay, replay.url
	}
	return replay, reader
}

func (s *Service) observeLegacySteps(ctx context.Context, primary model.RunResponse, replays map[string]legacyStepReplay, tuning config.RuntimeTuningConfig) *model.WorkMeasurementObservation {
	observation := &model.WorkMeasurementObservation{Version: 2, Authority: durante.Authority, State: "steps"}
	for _, native := range primary.Steps {
		row := model.PipelineStepObservation{StepID: native.ID, ProgramID: native.ProgramID, State: "unavailable"}
		replay, ready := replays[native.ID]
		switch {
		case ctx.Err() != nil:
			row.Reason = "step observation cancelled"
		case !ready || primary.VerdictSource == "step:"+native.ID+":handoff":
			row.Reason = "step has no eligible native output"
		default:
			row.Report, row.Reason = s.observeLegacyStep(ctx, replay, tuning)
			if row.Report != nil {
				row.State, row.OutputCheck = "complete", "native_output"
			}
		}
		observation.Steps = append(observation.Steps, row)
	}
	return observation
}

func (s *Service) observeLegacyStep(ctx context.Context, replay legacyStepReplay, tuning config.RuntimeTuningConfig) (*durante.Report, string) {
	var input io.Reader
	if replay.url != nil {
		if replay.url.over || !replay.url.eof {
			return nil, "step URL input snapshot unavailable"
		}
		input = bytes.NewReader(replay.url.bytes)
	} else if replay.path != "" {
		file, err := os.Open(replay.path)
		if err != nil {
			return nil, "step observation input unavailable"
		}
		defer file.Close()
		input = file
	}
	return s.observeBatchWithStdin(ctx, replay.request, replay.outputs, input, replay.maxBytes, tuning)
}
