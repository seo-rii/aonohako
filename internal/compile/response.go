package compile

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"aonohako/internal/compilewire"
	"aonohako/internal/model"
)

// PrepareResponse applies the diagnostic and final artifact bounds at the API
// boundary, including compilers that combine multiple artifact collections.
func PrepareResponse(resp model.CompileResponse) model.CompileResponse {
	if len(resp.ReasonCode) > compilewire.ReasonCodeBytes {
		return model.CompileResponse{Status: model.CompileStatusInternal, Reason: "invalid compile result: reason_code too long"}
	}
	var artifactErr error
	if len(resp.Artifacts) > compilewire.ArtifactCount {
		artifactErr = fmt.Errorf("too many artifacts: max %d", compilewire.ArtifactCount)
	} else {
		// Reserve one extra Base64 quantum per file for separate padding. CR/LF
		// are valid Base64 whitespace, but each occupies two JSON wire bytes.
		// Bound their expansion before any decoding, without normalizing or
		// allocating another copy of the artifact payloads.
		encodedLimit := int64(base64.StdEncoding.EncodedLen(compilewire.ArtifactTotalBytes) + 4*compilewire.ArtifactCount)
		var encodedTotal int64
		for _, artifact := range resp.Artifacts {
			if len(artifact.Name) > compilewire.ArtifactNameBytes {
				artifactErr = fmt.Errorf("artifact name too long: max %d bytes", compilewire.ArtifactNameBytes)
				break
			}
			encodedTotal += int64(len(artifact.DataB64))
			if encodedTotal <= encodedLimit {
				encodedTotal += int64(strings.Count(artifact.DataB64, "\r")) + int64(strings.Count(artifact.DataB64, "\n"))
			}
			if encodedTotal > encodedLimit {
				artifactErr = fmt.Errorf("encoded artifact total size exceeded: max %d JSON bytes", encodedLimit)
				break
			}
		}
		var total int64
		for _, artifact := range resp.Artifacts {
			if artifactErr != nil {
				break
			}
			// Count decoded bytes with a fixed-size buffer, including for remote
			// artifacts whose Base64 contains CR/LF. Stop after the file limit.
			decoded, err := io.Copy(io.Discard, io.LimitReader(base64.NewDecoder(base64.StdEncoding, strings.NewReader(artifact.DataB64)), compilewire.ArtifactBytes+1))
			if err != nil {
				artifactErr = fmt.Errorf("artifact data invalid base64: %w", err)
				break
			}
			if decoded > compilewire.ArtifactBytes {
				artifactErr = fmt.Errorf("artifact too large: max %d bytes", compilewire.ArtifactBytes)
				break
			}
			total += decoded
			if total > compilewire.ArtifactTotalBytes {
				artifactErr = fmt.Errorf("artifact total size exceeded: max %d bytes", compilewire.ArtifactTotalBytes)
				break
			}
		}
	}
	if artifactErr != nil {
		resp.Status = model.CompileStatusInternal
		resp.Artifacts = nil
		resp.Reason = "invalid compile result: " + artifactErr.Error()
		resp.ReasonCode = ""
		resp.ReasonTruncated = false
	}
	return capCompileResponseOutput(resp)
}

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
	if len(resp.Reason) > compilewire.ReasonBytes {
		end := compilewire.ReasonBytes
		for end > 0 && !utf8.RuneStart(resp.Reason[end]) {
			end--
		}
		resp.Reason = resp.Reason[:end]
		resp.ReasonTruncated = true
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
	case strings.Contains(lowerReason, "workspace limit exceeded") || strings.Contains(lowerReason, "workspace quota exceeded") || strings.Contains(lowerReason, "workspace entry limit exceeded") || strings.Contains(lowerReason, "workspace depth exceeded") || strings.Contains(lowerReason, "workspace scan failed"):
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
