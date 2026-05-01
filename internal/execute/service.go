package execute

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/platform"
	"aonohako/internal/runtimepolicy"
	"aonohako/internal/timing"
)

const (
	defaultMaxOutputBytes        = 64 << 10
	hardMaxOutputBytes           = 8 << 20
	defaultWorkspaceBytes        = 128 << 20
	hardMaxWorkspaceBytes        = 1 << 30
	maxBinaryFiles               = 512
	maxSidecarOutputSpecs        = 64
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = 16 << 20
	maxBinaryTotalBytes          = 48 << 20
	maxCapturedFileBytes         = 8 << 20
	maxCapturedSidecarTotalBytes = 16 << 20
	maxImageStreamBytes          = 8 << 20
	maxImageReadChunkBytes       = 256 << 10
	maxImageEventBytes           = 1 << 20
	maxImageEventsPerRead        = 8
	defaultSPJTimeMs             = 1000
	defaultSPJMemoryMB           = 256
	ocamlRunParam                = "s=32k"
)

type Hooks struct {
	OnImage func(mime, b64 string, ts int64)
	OnLog   func(stream, msg string)
}

type cappedBuffer struct {
	limit     int
	truncated bool
	buf       bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			b.truncated = true
			p = p[:remaining]
		}
		if _, err := b.buf.Write(p); err != nil {
			return 0, err
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return written, nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}

type Service struct {
	deploymentTarget      platform.DeploymentTarget
	runtimeTuning         config.RuntimeTuningConfig
	runtimeTuningProfiles map[string]config.RuntimeTuningConfig
	cgroupParentDir       string
}

func New() *Service {
	opts, err := platform.CurrentRuntimeOptions()
	if err != nil {
		return &Service{runtimeTuning: config.DefaultRuntimeTuningConfig()}
	}
	return &Service{deploymentTarget: opts.DeploymentTarget, runtimeTuning: config.DefaultRuntimeTuningConfig()}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	startWall := timing.MonotonicNow()
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	tuning := s.runtimeTuning.WithSafeDefaults()
	if req.RuntimeProfile != "" {
		if err := runtimepolicy.ValidateProfileName(req.RuntimeProfile); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid runtime_profile: " + err.Error()}
		}
		profileTuning, ok := s.runtimeTuningProfiles[req.RuntimeProfile]
		if !ok {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "unknown runtime_profile: " + req.RuntimeProfile}
		}
		tuning = profileTuning.WithSafeDefaults()
	}
	if req.EnableNetwork && s.deploymentTarget == platform.DeploymentTargetCloudRun {
		return model.RunResponse{
			Status: model.RunStatusInitFail,
			Reason: "embedded helper execution on cloudrun does not support enable_network=true; use a self-hosted remote runner for networked workloads",
		}
	}
	if len(req.FileOutputs) > 1 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "at most one file output is supported"}
	}
	if len(req.SidecarOutputs) > maxSidecarOutputSpecs {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many sidecar outputs: max %d", maxSidecarOutputSpecs)}
	}
	capturedOutputLimit := outputLimitBytes(req)
	if len(req.Binaries) > maxBinaryFiles {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("too many binaries: max %d", maxBinaryFiles)}
	}
	if len(req.Binaries) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "no binaries"}
	}

	workDir, err := createRunWorkDir()
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "mkdtemp failed: " + err.Error()}
	}
	defer os.RemoveAll(workDir)

	ws, err := prepareWorkspaceDirs(workDir)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "workspace prep failed: " + err.Error()}
	}

	primaryPath, runLang, err := materializeFiles(ws, req)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "materialize failed: " + err.Error()}
	}

	cmdArgs := buildCommandWithRuntimeTuning(primaryPath, runLang, req, tuning)
	if len(cmdArgs) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "empty command"}
	}

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, hooks, capturedOutputLimit, tuning, s.cgroupParentDir)
	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		return model.RunResponse{Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs, CPUTimeMs: 0, Reason: res.Reason, VerdictSource: res.VerdictSource}
	}

	rawOut := res.Stdout
	judgeOut := rawOut
	judgeSource := "stdout"
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err != nil {
			if res.Status == "OK" {
				res.Status = model.RunStatusRE
				res.Reason = "file output capture failed: " + err.Error()
				res.VerdictSource = "file_output"
			}
		} else {
			judgeOut = captured
			judgeSource = "file_output"
		}
	}

	sidecarOutputs, sidecarErrors := captureSidecarOutputs(ws, req.SidecarOutputs)
	status, score, evalReason, verdictSource := evaluateRunStatus(ctx, ws, req, res, judgeOut, judgeSource, s.runtimeTuning, s.cgroupParentDir)
	reason := res.Reason
	if evalReason != "" {
		reason = evalReason
	}

	var outResp, errResp string
	if status == model.RunStatusWA || status == model.RunStatusRE || (status == model.RunStatusTLE && req.IgnoreTLE) {
		outResp = clipUTF8(judgeOut, capturedOutputLimit)
	}
	if res.ExitCode != nil && *res.ExitCode != 0 {
		errResp = clipUTF8(fullErr, capturedOutputLimit)
	}

	if hooks.OnLog != nil {
		if len(rawOut) > 0 {
			hooks.OnLog("stdout", clipUTF8(rawOut, capturedOutputLimit))
		}
		if len(fullErr) > 0 {
			hooks.OnLog("stderr", clipUTF8(fullErr, capturedOutputLimit))
		}
	}

	return model.RunResponse{
		Status:          status,
		TimeMs:          res.WallTimeMs,
		WallTimeMs:      res.WallTimeMs,
		CPUTimeMs:       res.CPUTimeMs,
		MemoryKB:        res.MemoryKB,
		ExitCode:        res.ExitCode,
		Stdout:          outResp,
		Stderr:          errResp,
		StdoutTruncated: res.StdoutTruncated,
		StderrTruncated: res.StderrTruncated,
		Reason:          reason,
		VerdictSource:   verdictSource,
		Score:           score,
		SidecarOutputs:  sidecarOutputs,
		SidecarErrors:   sidecarErrors,
	}
}
