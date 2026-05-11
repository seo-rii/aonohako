package compile

import (
	"log/slog"
	"path/filepath"
	"strings"

	"aonohako/internal/model"
)

func capCompileResponseOutput(resp model.CompileResponse, workDirs ...string) model.CompileResponse {
	var truncated bool
	resp.Stdout, truncated = capCompileOutputValue(resp.Stdout)
	resp.StdoutTruncated = resp.StdoutTruncated || truncated
	resp.Stderr, truncated = capCompileOutputValue(resp.Stderr)
	resp.StderrTruncated = resp.StderrTruncated || truncated
	if resp.Status == model.CompileStatusInternal && resp.Reason != "" {
		rawReason := resp.Reason
		for _, workDir := range workDirs {
			workDir = strings.TrimSpace(workDir)
			if workDir == "" {
				continue
			}
			cleanWorkDir := filepath.Clean(workDir)
			if cleanWorkDir == "." || !filepath.IsAbs(cleanWorkDir) {
				continue
			}
			resp.Reason = strings.ReplaceAll(resp.Reason, cleanWorkDir, "$WORKDIR")
			if realWorkDir, err := filepath.EvalSymlinks(cleanWorkDir); err == nil && realWorkDir != "" && realWorkDir != cleanWorkDir {
				resp.Reason = strings.ReplaceAll(resp.Reason, realWorkDir, "$WORKDIR")
			}
		}
		if resp.Reason != rawReason {
			slog.Warn("compile internal reason redacted", "reason", rawReason)
		}
	}
	if resp.ReasonCode == "" {
		resp.ReasonCode = compileReasonCode(resp.Status, resp.Reason)
	}
	return resp
}

func capCompileOutputValue(value string) (string, bool) {
	if len(value) > compileOutputCaptureBytes {
		return value[:compileOutputCaptureBytes], true
	}
	return value, false
}

func compileReasonCode(status, reason string) string {
	lowerReason := strings.ToLower(reason)
	switch {
	case status == model.CompileStatusTimeout || strings.Contains(lowerReason, "context deadline exceeded"):
		return "timeout"
	case strings.Contains(lowerReason, "memory limit exceeded"):
		return "memory_limit_exceeded"
	case strings.Contains(lowerReason, "workspace limit exceeded") || strings.Contains(lowerReason, "workspace scan failed"):
		return "workspace_limit_exceeded"
	case strings.Contains(lowerReason, "pids limit exceeded") || strings.Contains(lowerReason, "process limit exceeded"):
		return "process_limit_exceeded"
	case strings.Contains(lowerReason, "file size limit exceeded"):
		return "file_size_limit_exceeded"
	case strings.Contains(lowerReason, "cpu time limit exceeded"):
		return "cpu_time_limit_exceeded"
	default:
		return ""
	}
}

type cappedTextBuffer struct {
	limit     int
	buf       strings.Builder
	truncated bool
}

func newCompileOutputBuffer() *cappedTextBuffer {
	return &cappedTextBuffer{limit: compileOutputCaptureBytes}
}

func (b *cappedTextBuffer) Append(value string) {
	if b == nil || value == "" || b.limit <= 0 {
		return
	}
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return
	}
	if len(value) > remaining {
		b.buf.WriteString(value[:remaining])
		b.truncated = true
		return
	}
	b.buf.WriteString(value)
}

func (b *cappedTextBuffer) String() string {
	if b == nil {
		return ""
	}
	return b.buf.String()
}

func (b *cappedTextBuffer) Truncated() bool {
	return b != nil && b.truncated
}

func compileResponseWithCapturedOutput(status string, artifacts []model.Artifact, reason string, stdout, stderr *cappedTextBuffer) model.CompileResponse {
	return model.CompileResponse{
		Status:          status,
		Artifacts:       artifacts,
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		Reason:          reason,
	}
}
