package execute

import (
	"bytes"
	"context"
	"os"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

const (
	defaultMaxOutputBytes        = 64 << 10
	hardMaxOutputBytes           = 8 << 20
	defaultWorkspaceBytes        = 128 << 20
	hardMaxWorkspaceBytes        = 1 << 30
	addressSpaceSlackKB          = 8 << 10
	sandboxThreadLimit           = 128
	maxBinaryFileBytes           = 16 << 20
	maxBinaryTotalBytes          = 48 << 20
	maxCapturedFileBytes         = 8 << 20
	maxCapturedSidecarTotalBytes = 16 << 20
	ocamlRunParam                = "s=32k"
	elixirERLAFlags              = "+MIscs 128 +S 1:1 +A 1"
)

type Hooks struct {
	OnImage func(mime, b64 string, ts int64)
	OnLog   func(stream, msg string)
}

type cappedBuffer struct {
	limit int
	buf   bytes.Buffer
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - b.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		if _, err := b.buf.Write(p); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buf.Bytes()
}

type Service struct{}

func New() *Service {
	return &Service{}
}

func (s *Service) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	startWall := timing.MonotonicNow()
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	if len(req.FileOutputs) > 1 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "at most one file output is supported"}
	}
	capturedOutputLimit := outputLimitBytes(req)
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

	cmdArgs := buildCommand(primaryPath, runLang, req)
	if len(cmdArgs) == 0 {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "empty command"}
	}

	res := runCommandWithSandbox(ctx, ws, cmdArgs, req, hooks, capturedOutputLimit)
	if res.Status == model.RunStatusInitFail {
		wallMs := timing.SinceMillis(startWall)
		return model.RunResponse{Status: res.Status, TimeMs: wallMs, WallTimeMs: wallMs, CPUTimeMs: 0, Reason: res.Reason}
	}

	rawOut := res.Stdout
	judgeOut := rawOut
	fullErr := res.Stderr

	if len(req.FileOutputs) > 0 {
		captured, err := captureFileOutput(ws, req.FileOutputs[0])
		if err != nil {
			if res.Status == "OK" {
				res.Status = model.RunStatusRE
				res.Reason = "file output capture failed: " + err.Error()
			}
		} else {
			judgeOut = captured
		}
	}

	sidecarOutputs := captureSidecarOutputs(ws, req.SidecarOutputs)
	status, score := evaluateRunStatus(ctx, ws, req, res, judgeOut)

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
		Status:         status,
		TimeMs:         res.WallTimeMs,
		WallTimeMs:     res.WallTimeMs,
		CPUTimeMs:      res.CPUTimeMs,
		MemoryKB:       res.MemoryKB,
		ExitCode:       res.ExitCode,
		Stdout:         outResp,
		Stderr:         errResp,
		Reason:         res.Reason,
		Score:          score,
		SidecarOutputs: sidecarOutputs,
	}
}
