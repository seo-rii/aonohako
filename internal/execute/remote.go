package execute

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
	"aonohako/internal/runvalidation"
	"aonohako/internal/util"
)

const cloudRunMetadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

type remoteRunner struct {
	client           *http.Client
	metadataClient   *http.Client
	executeURL       string
	auth             config.RemoteAuthMode
	bearerToken      string
	audience         string
	metadataURL      string
	idleTimeout      time.Duration
	absoluteTimeout  time.Duration
	uploadTimeout    time.Duration
	errorBodyTimeout time.Duration
	strictProto      bool
}

func newRemoteRunner(cfg config.Config) Runner {
	auth := cfg.Execution.Remote.Auth
	if auth == "" {
		auth = config.RemoteAuthNone
	}
	return &remoteRunner{
		client:           remoteio.NewHTTPClient(),
		metadataClient:   remoteio.NewMetadataHTTPClient(),
		executeURL:       normalizeRemoteExecuteURL(cfg.Execution.Remote.URL),
		auth:             auth,
		bearerToken:      cfg.Execution.Remote.BearerToken,
		audience:         cfg.Execution.Remote.Audience,
		metadataURL:      cloudRunMetadataIdentityURL,
		idleTimeout:      cfg.Execution.Remote.SSEIdleTimeout,
		uploadTimeout:    remoteio.DefaultRequestUploadTimeout,
		errorBodyTimeout: remoteio.DefaultErrorResponseBodyTimeout,
		strictProto:      cfg.Execution.Remote.StrictProtocol,
	}
}

func (r *remoteRunner) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}

	body, err := json.Marshal(req)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request encode failed: " + err.Error()}
	}

	absoluteTimeout := r.absoluteTimeout
	if absoluteTimeout <= 0 {
		var requestedTimeMs int64
		if runvalidation.UsesSteps(req) {
			for _, step := range req.Steps {
				stepTimeMs := step.Limits.TimeMs
				if stepTimeMs < 0 {
					stepTimeMs = 0
				}
				if stepTimeMs > runvalidation.MaxTimeMs {
					stepTimeMs = runvalidation.MaxTimeMs
				}
				requestedTimeMs += int64(stepTimeMs)
			}
		} else {
			mainTimeMs := req.Limits.TimeMs
			if mainTimeMs < 0 {
				mainTimeMs = 0
			}
			if mainTimeMs > runvalidation.MaxTimeMs {
				mainTimeMs = runvalidation.MaxTimeMs
			}
			requestedTimeMs = int64(mainTimeMs)
			if req.Interactor != nil && req.Interactor.Limits != nil {
				interactorTimeMs := req.Interactor.Limits.TimeMs
				if interactorTimeMs < 0 {
					interactorTimeMs = 0
				}
				if interactorTimeMs > runvalidation.MaxTimeMs {
					interactorTimeMs = runvalidation.MaxTimeMs
				}
				if int64(interactorTimeMs) > requestedTimeMs {
					requestedTimeMs = int64(interactorTimeMs)
				}
			}
		}
		if req.SPJ != nil {
			spjTimeMs := defaultSPJTimeMs
			if req.SPJ.Limits != nil && req.SPJ.Limits.TimeMs > 0 {
				spjTimeMs = min(req.SPJ.Limits.TimeMs, runvalidation.MaxTimeMs)
			}
			requestedTimeMs += int64(spjTimeMs)
		}
		absoluteTimeout = time.Duration(requestedTimeMs)*time.Millisecond + remoteio.DefaultOperationOverhead
	}
	streamCtx, cancelStream := context.WithTimeout(ctx, absoluteTimeout)
	defer cancelStream()

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, r.executeURL, bytes.NewReader(body))
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request build failed: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	if authHeader, err := r.authorizationHeader(ctx); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote auth failed: " + err.Error()}
	} else if authHeader != "" {
		httpReq.Header.Set("Authorization", authHeader)
	}

	uploadCtx, finishUpload := remoteio.WithRequestUploadTimeout(httpReq.Context(), cancelStream, r.uploadTimeout)
	httpReq = httpReq.WithContext(uploadCtx)
	resp, err := r.client.Do(httpReq)
	if err != nil {
		if finishUpload() {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute request upload timed out"}
		}
		if errors.Is(streamCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute absolute deadline exceeded"}
		}
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute request failed: " + err.Error()}
	}
	defer finishUpload()
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errorBodyTimeout := r.errorBodyTimeout
		if errorBodyTimeout <= 0 {
			errorBodyTimeout = remoteio.DefaultErrorResponseBodyTimeout
		}
		raw, readErr := remoteio.ReadBoundedBodyWithTimeout(resp.Body, cancelStream, remoteio.MaxRemoteErrorResponseBytes, errorBodyTimeout)
		if readErr != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote execute returned %s but error response body read failed: %v", resp.Status, readErr)}
		}
		reason := strings.TrimSpace(string(raw))
		if reason == "" {
			reason = resp.Status
		}
		var payload map[string]any
		if err := json.Unmarshal(raw, &payload); err == nil {
			if msg, ok := payload["message"].(string); ok && strings.TrimSpace(msg) != "" {
				reason = msg
			} else if msg, ok := payload["error"].(string); ok && strings.TrimSpace(msg) != "" {
				reason = msg
			}
		}
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote execute returned %s: %s", resp.Status, reason)}
	}
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, mediaTypeErr := mime.ParseMediaType(contentType)
	if mediaTypeErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote execute returned unexpected content type: %s", contentType)}
	}
	if err := remoteio.CheckProtocolVersionWithPolicy(resp.Header, r.strictProto); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute protocol mismatch: " + err.Error()}
	}

	reader := remoteio.NewSSEReader(resp.Body)
	idleTimeout := r.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = remoteio.DefaultSSEIdleTimeout
	}
	if idleTimeout > 0 {
		idleTimer := time.AfterFunc(idleTimeout, cancelStream)
		defer idleTimer.Stop()
		reader.SetActivityCallback(func() {
			idleTimer.Reset(idleTimeout)
		})
	}
	result := model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream ended without result"}
	remoteErrorReason := ""
	logBytes := map[string]int{"stdout": 0, "stderr": 0}
	logCaptureBytes := map[string]int{"stdout": 0, "stderr": 0}
	var imageBytes int

	for {
		event, err := reader.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result
			}
			if streamCtx.Err() != nil && ctx.Err() == nil {
				if finishUpload() {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute request upload timed out"}
				}
				if errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute absolute deadline exceeded"}
				}
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream idle timeout exceeded"}
			}
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote execute stream failed: " + err.Error()}
		}
		switch event.Name {
		case "log":
			var chunk struct {
				Stream string `json:"stream"`
				Chunk  string `json:"chunk"`
			}
			if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote log decode failed: " + err.Error()}
			}
			if chunk.Stream != "stdout" && chunk.Stream != "stderr" {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote log event has invalid stream"}
			}
			logSafetyLimit := min(outputLimitBytes(req), maxResponseCaptureBytes)
			if len(chunk.Chunk) > logSafetyLimit {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote log event too large: max %d bytes", logSafetyLimit)}
			}
			logBytes[chunk.Stream] += len(chunk.Chunk)
			if logBytes[chunk.Stream] > logSafetyLimit {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote log stream too large: max %d bytes", logSafetyLimit)}
			}
			if hooks.OnLog != nil {
				captureLimit := responseStdoutLimitBytes(req)
				if chunk.Stream == "stderr" {
					captureLimit = responseStderrLimitBytes(req)
				}
				remaining := captureLimit - logCaptureBytes[chunk.Stream]
				if remaining > 0 {
					value, _ := capturedOutputValue([]byte(chunk.Chunk), remaining)
					if value != "" {
						logCaptureBytes[chunk.Stream] += len(value)
						hooks.OnLog(chunk.Stream, value)
					}
				}
			}
		case "image":
			var image struct {
				Mime string `json:"mime"`
				B64  string `json:"b64"`
				TS   int64  `json:"ts"`
			}
			if err := json.Unmarshal([]byte(event.Data), &image); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote image decode failed: " + err.Error()}
			}
			if strings.TrimSpace(image.Mime) == "" || strings.TrimSpace(image.B64) == "" {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote image event missing payload"}
			}
			if len(image.B64) > maxImageEventBytes {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote image event too large: max %d bytes", maxImageEventBytes)}
			}
			decodedImage, err := base64.StdEncoding.DecodeString(image.B64)
			if err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote image event has invalid base64"}
			}
			imageBytes += len(decodedImage)
			if imageBytes > maxImageStreamBytes {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote image stream too large: max %d bytes", maxImageStreamBytes)}
			}
			if hooks.OnImage != nil {
				ts := image.TS
				if ts == 0 {
					ts = time.Now().UnixMilli()
				}
				hooks.OnImage(image.Mime, image.B64, ts)
			}
		case "error":
			var remoteErr struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal([]byte(event.Data), &remoteErr); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote error decode failed: " + err.Error()}
			}
			if strings.TrimSpace(remoteErr.Message) != "" {
				remoteErrorReason = remoteErr.Message
			}
		case "result":
			var remoteResult model.RunResponse
			if err := json.Unmarshal([]byte(event.Data), &remoteResult); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote result decode failed: " + err.Error()}
			}
			if strings.TrimSpace(remoteResult.Reason) == "" {
				remoteResult.Reason = remoteErrorReason
			}
			switch remoteResult.Status {
			case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
			default:
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result status: %q", remoteResult.Status)}
			}
			if remoteResult.TimeMs < 0 || remoteResult.WallTimeMs < 0 || remoteResult.CPUTimeMs < 0 || remoteResult.ProcessCPUTimeMs < 0 || remoteResult.MemoryKB < 0 {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: negative resource measurement"}
			}
			if remoteResult.Score != nil && (math.IsNaN(*remoteResult.Score) || math.IsInf(*remoteResult.Score, 0) || *remoteResult.Score < 0 || *remoteResult.Score > 1) {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: score out of range"}
			}
			if len(remoteResult.Steps) > runvalidation.MaxSteps {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result: too many steps: max %d", runvalidation.MaxSteps)}
			}
			stdoutResponseLimit := responseStdoutLimitBytes(req)
			stderrResponseLimit := responseStderrLimitBytes(req)
			for i := range remoteResult.Steps {
				step := &remoteResult.Steps[i]
				switch step.Status {
				case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
				default:
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote step result status: %q", step.Status)}
				}
				if step.TimeMs < 0 || step.WallTimeMs < 0 || step.CPUTimeMs < 0 || step.ProcessCPUTimeMs < 0 || step.MemoryKB < 0 || step.HandoffBytes < 0 {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote step result: negative measurement"}
				}
				var truncated bool
				step.Stdout, truncated = capturedOutputValue([]byte(step.Stdout), stdoutResponseLimit)
				if truncated {
					step.StdoutTruncated = true
				}
				step.Stderr, truncated = capturedOutputValue([]byte(step.Stderr), stderrResponseLimit)
				if truncated {
					step.StderrTruncated = true
				}
			}
			if len(remoteResult.SidecarOutputs) > maxSidecarOutputSpecs || len(remoteResult.SidecarErrors) > maxSidecarOutputSpecs {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: too many sidecar entries"}
			}
			requestedSidecars := make(map[string]struct{}, len(req.SidecarOutputs))
			for _, sidecar := range req.SidecarOutputs {
				if clean, err := util.ValidateRelativePath(sidecar.Path); err == nil {
					requestedSidecars[clean] = struct{}{}
				}
			}
			seenSidecars := make(map[string]struct{}, len(remoteResult.SidecarOutputs))
			var sidecarBytes int
			for i, sidecar := range remoteResult.SidecarOutputs {
				clean, err := util.ValidateRelativePath(sidecar.Path)
				if err != nil {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result sidecar[%d].path: %v", i, err)}
				}
				if _, requested := requestedSidecars[clean]; !requested {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: unrequested sidecar path: " + clean}
				}
				if _, duplicate := seenSidecars[clean]; duplicate {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: duplicate sidecar path: " + clean}
				}
				seenSidecars[clean] = struct{}{}
				decoded, err := base64.StdEncoding.DecodeString(sidecar.DataB64)
				if err != nil {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result sidecar[%d].data_b64", i)}
				}
				if len(decoded) > maxCapturedFileBytes {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: sidecar too large: " + clean}
				}
				sidecarBytes += len(decoded)
				if sidecarBytes > maxCapturedSidecarTotalBytes {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: sidecar total size exceeded"}
				}
			}
			for i, sidecarErr := range remoteResult.SidecarErrors {
				clean, err := util.ValidateRelativePath(sidecarErr.Path)
				if err != nil {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result sidecar error[%d].path: %v", i, err)}
				}
				if _, requested := requestedSidecars[clean]; !requested {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: unrequested sidecar error path: " + clean}
				}
			}
			var truncated bool
			remoteResult.Stdout, truncated = capturedOutputValue([]byte(remoteResult.Stdout), stdoutResponseLimit)
			if truncated {
				remoteResult.StdoutTruncated = true
			}
			remoteResult.Stderr, truncated = capturedOutputValue([]byte(remoteResult.Stderr), stderrResponseLimit)
			if truncated {
				remoteResult.StderrTruncated = true
			}
			remoteResult.Status, remoteResult.Reason, remoteResult.VerdictSource = applyFinalCPUTimeStatus(remoteResult.Status, remoteResult.Reason, remoteResult.VerdictSource, remoteResult.CPUTimeMs, req.Limits.TimeMs, strings.HasPrefix(remoteResult.VerdictSource, "cpu_time_cgroup"))
			return remoteResult
		}
	}
}

func (r *remoteRunner) authorizationHeader(ctx context.Context) (string, error) {
	switch r.auth {
	case "", config.RemoteAuthNone:
		return "", nil
	case config.RemoteAuthBearer:
		return "Bearer " + r.bearerToken, nil
	case config.RemoteAuthCloudRunIDToken:
		query := url.Values{}
		query.Set("audience", r.audience)
		query.Set("format", "full")
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.metadataURL+"?"+query.Encode(), nil)
		if err != nil {
			return "", err
		}
		httpReq.Header.Set("Metadata-Flavor", "Google")
		resp, err := r.metadataClient.Do(httpReq)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		raw, err := remoteio.ReadBoundedBody(resp.Body, remoteio.MaxMetadataIdentityTokenBytes)
		if err != nil {
			return "", fmt.Errorf("metadata identity token read failed: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("metadata server returned %s: %s", resp.Status, strings.TrimSpace(string(raw)))
		}
		token := strings.TrimSpace(string(raw))
		if token == "" {
			return "", fmt.Errorf("metadata server returned an empty identity token")
		}
		return "Bearer " + token, nil
	default:
		return "", fmt.Errorf("unsupported remote auth mode: %s", r.auth)
	}
}

func normalizeRemoteExecuteURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil {
		return trimmed
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if strings.HasSuffix(parsed.Path, "/execute") {
		return parsed.String()
	}
	if parsed.Path == "" {
		parsed.Path = "/execute"
		return parsed.String()
	}
	parsed.Path += "/execute"
	return parsed.String()
}
