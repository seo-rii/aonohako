package execute

import (
	"context"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"aonohako/internal/model"
)

// stdinSnapshot is owned by one request. Its sequential executions open
// independent read-only descriptors into a file outside contestant workspaces.
type stdinSnapshot struct {
	dir  string
	path string
	size int64
	hash hash.Hash // optional receipt for URL-backed shadow comparisons
}

type shadowStdinKey struct{}

func (s *stdinSnapshot) close() {
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

func (s *stdinSnapshot) open(ctx context.Context, url string, maxBytes int64) (*sandboxPreparedStdin, error) {
	if s.path == "" {
		dir, err := createRunWorkDir()
		if err != nil {
			return nil, fmt.Errorf("input workspace failed: %w", err)
		}
		s.dir = dir
		source, err := openStdinURL(ctx, url, maxBytes, nil)
		if err != nil {
			return nil, err
		}
		var reader io.Reader = source
		if s.hash != nil {
			reader = io.TeeReader(source, s.hash)
		}
		path, err := writeTempFileFromReader(dir, "stdin-*", reader, maxBytes)
		closeErr := source.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("frozen input stat failed: %w", err)
		}
		s.path, s.size = path, info.Size()
	}
	file, err := os.Open(s.path)
	if err != nil {
		return nil, fmt.Errorf("frozen input open failed: %w", err)
	}
	return &sandboxPreparedStdin{file: file, outsideWorkspaceBytes: s.size}, nil
}

// runShortCaseBatch freezes URL stdin once and gives each execution a fresh
// reader. Binary and expected-output URLs have already been resolved by the API;
// direct callers retaining them are ineligible. run must create a new workspace.
func runShortCaseBatch(ctx context.Context, req *model.RunRequest, hooks Hooks, run func(context.Context, *model.RunRequest, io.Reader, int64, Hooks) model.RunResponse) model.RunResponse {
	frozen := *req
	frozen.StdinURL = ""
	if !shortCaseRepeatEligible(&frozen, hooks) {
		return run(ctx, req, nil, 0, hooks)
	}
	snapshot, _ := ctx.Value(shadowStdinKey{}).(*stdinSnapshot)
	if snapshot == nil {
		snapshot = &stdinSnapshot{}
		defer snapshot.close()
	}
	url := strings.TrimSpace(req.StdinURL)
	runOnce := func(ctx context.Context, hooks Hooks) model.RunResponse {
		if err := ctx.Err(); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "short-case execution interrupted: " + err.Error()}
		}
		if url == "" {
			return run(ctx, &frozen, strings.NewReader(frozen.Stdin), 0, hooks)
		}
		maxBytes := stdinURLMaxBytes(req.Limits)
		stdin, err := snapshot.open(ctx, req.StdinURL, maxBytes)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "stdin_url: " + err.Error()}
		}
		defer stdin.file.Close()
		frozen.Stdin = ""
		return run(ctx, &frozen, stdin, maxBytes, hooks)
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
