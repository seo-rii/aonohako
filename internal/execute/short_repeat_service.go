package execute

import (
	"context"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"aonohako/internal/model"
)

// runShortCaseBatch freezes URL stdin once and gives each execution a fresh
// reader. Binary and expected-output URLs have already been resolved by the API;
// direct callers retaining them are ineligible. run must create a new workspace.
func runShortCaseBatch(ctx context.Context, req *model.RunRequest, hooks Hooks, run func(context.Context, *model.RunRequest, io.Reader, int64, Hooks) model.RunResponse) model.RunResponse {
	frozen := *req
	frozen.StdinURL = ""
	if !shortCaseRepeatEligible(&frozen, hooks) {
		return run(ctx, req, nil, 0, hooks)
	}
	var stdinMaxBytes, frozenInputBytes int64
	var frozenInputPath string
	if strings.TrimSpace(req.StdinURL) != "" {
		// Keep the fixture outside every contestant workspace. It stays under
		// the configured work root and its existing URL size bound, while a
		// fixed-size copy buffer avoids retaining the payload on the Go heap.
		workDir, err := createRunWorkDir()
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: input workspace failed: " + err.Error()}
		}
		defer os.RemoveAll(workDir)
		stdinMaxBytes = stdinURLMaxBytes(req.Limits)
		reader, err := openStdinURL(ctx, req.StdinURL, stdinMaxBytes, nil)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + err.Error()}
		}
		path, readErr := writeTempFileFromReader(workDir, "stdin-*", reader, stdinMaxBytes)
		closeErr := reader.Close()
		if readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + readErr.Error()}
		}
		info, err := os.Stat(path)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: frozen input stat failed: " + err.Error()}
		}
		frozenInputPath, frozenInputBytes = path, info.Size()
		frozen.Stdin = ""
	}
	runOnce := func(ctx context.Context, hooks Hooks) model.RunResponse {
		if err := ctx.Err(); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "short-case execution interrupted: " + err.Error()}
		}
		if frozenInputPath == "" {
			return run(ctx, &frozen, strings.NewReader(frozen.Stdin), stdinMaxBytes, hooks)
		}
		// A new read-only descriptor gives every sample an independent offset
		// and cannot modify the shared fixture. The sandbox consumes it directly
		// instead of creating another full-sized copy in its workspace.
		file, err := os.Open(frozenInputPath)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: frozen input open failed: " + err.Error()}
		}
		defer file.Close()
		stdin := &sandboxPreparedStdin{file: file, outsideWorkspaceBytes: frozenInputBytes}
		return run(ctx, &frozen, stdin, stdinMaxBytes, hooks)
	}
	var imageEmitted atomic.Bool
	firstHooks := hooks
	firstHooks.OnImage = func(mime, b64 string, ts int64) {
		imageEmitted.Store(true)
		if hooks.OnImage != nil {
			hooks.OnImage(mime, b64, ts)
		}
	}
	first := runOnce(ctx, firstHooks)
	if imageEmitted.Load() {
		return first
	}
	return runShortCaseMedian(ctx, first, func(ctx context.Context) model.RunResponse {
		return runOnce(ctx, Hooks{})
	})
}
