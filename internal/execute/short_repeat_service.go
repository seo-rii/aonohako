package execute

import (
	"context"
	"fmt"
	"io"
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
	var stdinMaxBytes int64
	if strings.TrimSpace(req.StdinURL) != "" {
		stdinMaxBytes = stdinURLMaxBytes(req.Limits)
		reader, err := openStdinURL(ctx, req.StdinURL, stdinMaxBytes, nil)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + err.Error()}
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, stdinMaxBytes+1))
		closeErr := reader.Close()
		if readErr == nil {
			readErr = closeErr
		}
		if readErr != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + readErr.Error()}
		}
		if int64(len(data)) > stdinMaxBytes {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("stdin_url: download too large: max %d bytes", stdinMaxBytes)}
		}
		frozen.Stdin = string(data)
	}
	var imageEmitted atomic.Bool
	firstHooks := hooks
	firstHooks.OnImage = func(mime, b64 string, ts int64) {
		imageEmitted.Store(true)
		if hooks.OnImage != nil {
			hooks.OnImage(mime, b64, ts)
		}
	}
	first := run(ctx, &frozen, strings.NewReader(frozen.Stdin), stdinMaxBytes, firstHooks)
	if imageEmitted.Load() {
		return first
	}
	return runShortCaseMedian(ctx, first, func(ctx context.Context) model.RunResponse {
		return run(ctx, &frozen, strings.NewReader(frozen.Stdin), stdinMaxBytes, Hooks{})
	})
}
