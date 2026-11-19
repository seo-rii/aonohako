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
	"strconv"
	"strings"
	"time"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/remoteio"
	"aonohako/internal/runvalidation"
	"aonohako/internal/util"
)

const cloudRunMetadataIdentityURL = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/identity"

type remoteRunner struct {
	runtimeTuning         config.RuntimeTuningConfig
	runtimeTuningProfiles map[string]config.RuntimeTuningConfig
	workMeasurement       durante.Config
	client                *http.Client
	metadataClient        *http.Client
	executeURL            string
	auth                  config.RemoteAuthMode
	bearerToken           string
	audience              string
	metadataURL           string
	idleTimeout           time.Duration
	absoluteTimeout       time.Duration
	uploadTimeout         time.Duration
	errorBodyTimeout      time.Duration
	strictProto           bool
}

func newRemoteRunner(cfg config.Config) Runner {
	auth := cfg.Execution.Remote.Auth
	if auth == "" {
		auth = config.RemoteAuthNone
	}
	return &remoteRunner{
		runtimeTuning:         cfg.Execution.RuntimeTuning.WithSafeDefaults(),
		runtimeTuningProfiles: cloneRuntimeTuningProfiles(cfg.Execution.RuntimeTuningProfiles),
		workMeasurement:       snapshotWorkMeasurementConfig(cfg.Execution.WorkMeasurement),
		client:                remoteio.NewHTTPClient(),
		metadataClient:        remoteio.NewMetadataHTTPClient(),
		executeURL:            normalizeRemoteExecuteURL(cfg.Execution.Remote.URL),
		auth:                  auth,
		bearerToken:           cfg.Execution.Remote.BearerToken,
		audience:              cfg.Execution.Remote.Audience,
		metadataURL:           cloudRunMetadataIdentityURL,
		idleTimeout:           cfg.Execution.Remote.SSEIdleTimeout,
		uploadTimeout:         remoteio.DefaultRequestUploadTimeout,
		errorBodyTimeout:      remoteio.DefaultErrorResponseBodyTimeout,
		strictProto:           cfg.Execution.Remote.StrictProtocol,
	}
}

func (r *remoteRunner) Run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req != nil && req.Pipeline != nil {
		// Pipeline logs and diagnostics may contain private resources or artifacts.
		// Enforce the secrecy boundary again at the control plane even when the
		// downstream runner is expected to implement the same contract.
		return RedactPipelineResponse(r.run(ctx, req, Hooks{}))
	}
	return r.run(ctx, req, hooks)
}

func (r *remoteRunner) run(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if req == nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "nil request"}
	}
	if err := runvalidation.ValidateDisallowedCPUFeatures(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	// A shadow request's authoritative portion is an ordinary request; validate
	// it and its result as such, and the shadow block separately below.
	if err := runvalidation.ValidateWorkMeasurementShadow(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	checked := shadowPrimaryRequest(req)
	if req.WorkMeasurementShadow {
		if reason := shadowGateUnavailable(req, r.workMeasurement); reason != "" {
			return attachShadow(r.run(ctx, checked, hooks), nil, reason)
		}
	}
	if err := runvalidation.ValidateWorkMeasurementLimits(checked); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	tuning := r.runtimeTuning.WithSafeDefaults()
	schema2 := r.workMeasurement.TimeModels != nil && r.workMeasurement.TimeModels.Schema == 2
	if (checked.WorkMeasurementTime || checked.WorkMeasurementPeerTime != nil || req.WorkMeasurementShadow) && schema2 {
		var err error
		tuning, err = resolveRuntimeTuning(checked, r.runtimeTuning, r.runtimeTuningProfiles)
		if err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if checked.WorkMeasurementTime {
		if _, _, err := ordinaryTimePolicy(checked, r.workMeasurement, tuning); err != nil {
			return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
		}
	}
	if _, err := workMeasurementPeerTimePlans(checked, r.workMeasurement, tuning); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := validateJudgeRequest(ctx, checked, r.workMeasurement); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := runvalidation.ValidateTextEncoding(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if err := runvalidation.ValidateSidecarOutputs(req.SidecarOutputs); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	forward := req
	if err := validateJudgeClockBinding(req, r.workMeasurement, tuning); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	if clock := effectiveWorkClock(req, r.workMeasurement, tuning); clock != nil && clock.TimePolicy != nil {
		// An earlier worker rejects the versioned request field before execution.
		// A current worker checks the model binding against its own configuration.
		copy := *req
		copy.JudgeClockBinding = clock.ModelBinding()
		forward = &copy
	}
	if err := runvalidation.ValidateRemoteRequestSize(forward); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	body, err := json.Marshal(forward)
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request encode failed: " + err.Error()}
	}

	observationBudget := remoteObservationBudget(req, r.workMeasurement)
	legacySteps := runvalidation.UsesSteps(req)
	standaloneInteractive := req.Interactor != nil && req.Pipeline == nil && !legacySteps
	if legacySteps || standaloneInteractive {
		if incoming := stepObservationBudgetFromContext(ctx); incoming.bounded {
			observationBudget = min(observationBudget, incoming.maximum)
		}
	}
	if standaloneInteractive {
		// The downstream worker receives whole, nonnegative milliseconds.
		// Use exactly that allowance for the deadline and returned receipt.
		observationBudget = max(0, observationBudget.Truncate(time.Millisecond))
	}
	absoluteTimeout := r.absoluteTimeout
	if absoluteTimeout <= 0 {
		absoluteTimeout = remoteExecutionTimeout(req, r.uploadTimeout)
		absoluteTimeout += observationBudget
		if (req.Pipeline != nil || legacySteps || standaloneInteractive) && observationBudget > 0 {
			// Leave time to reap a replay stopped at its deadline and return the native result.
			absoluteTimeout += remoteio.DefaultOperationOverhead
		}
	}
	streamCtx, cancelStream := context.WithTimeout(ctx, absoluteTimeout)
	defer cancelStream()

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, r.executeURL, bytes.NewReader(body))
	if err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote request build failed: " + err.Error()}
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if req.Pipeline != nil {
		httpReq.Header.Set(model.PipelineV1WorkerHeader, model.PipelineV1WorkerProtocol)
		if observationBudget > 0 {
			httpReq.Header.Set(model.PipelineInteractiveObservationHeader, "v1")
		}
		httpReq.Header.Set(model.PipelineObservationBudgetHeader, strconv.FormatInt(observationBudget.Milliseconds(), 10))
	} else if legacySteps {
		// Older workers ignore this optional ceiling and run their native steps normally.
		httpReq.Header.Set(model.LegacyObservationBudgetHeader, strconv.FormatInt(observationBudget.Milliseconds(), 10))
	} else if standaloneInteractive {
		httpReq.Header.Set(model.InteractiveObservationBudgetHeader, strconv.FormatInt(observationBudget.Milliseconds(), 10))
	}

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
			// Legacy runners reject unknown JSON fields before queueing. Send the
			// ordinary request once under the original deadline only for that
			// explicit admission error; execution and transport errors are final.
			if req.WorkMeasurementShadow && resp.StatusCode == http.StatusBadRequest &&
				payload["error"] == "unknown_json_field" && payload["message"] == `invalid json: json: unknown field "work_measurement_shadow"` {
				primary := r.run(streamCtx, checked, hooks)
				return attachShadow(primary, nil, "remote runner does not support work_measurement_shadow")
			}
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
	if req.Pipeline != nil && resp.Header.Get(model.PipelineV1WorkerHeader) != model.PipelineV1WorkerProtocol {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote worker did not confirm Pipeline V1 execution"}
	}
	if err := remoteio.CheckProtocolVersionWithPolicy(resp.Header, r.strictProto || req.Pipeline != nil); err != nil {
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
	logLimits := remoteLogLimits(req)
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
			if len(chunk.Chunk) > logLimits.event {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote log event too large: max %d bytes", logLimits.event)}
			}
			logBytes[chunk.Stream] += len(chunk.Chunk)
			if logBytes[chunk.Stream] > logLimits.stream {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("remote log stream too large: max %d bytes", logLimits.stream)}
			}
			if hooks.OnLog != nil {
				captureLimit := logLimits.stdout
				if chunk.Stream == "stderr" {
					captureLimit = logLimits.stderr
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
			// Decode the optional shadow separately. Its receipt can use a newer
			// wire format even when the ordinary verdict is fully compatible.
			var wire struct {
				model.RunResponse
				Shadow           json.RawMessage `json:"work_measurement_shadow"`
				Observation      json.RawMessage `json:"work_measurement_observation"`
				CommunicationCPU json.RawMessage `json:"communication_cpu_by_role"`
			}
			if err := json.Unmarshal([]byte(event.Data), &wire); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "remote result decode failed: " + err.Error()}
			}
			remoteResult := wire.RunResponse
			if req.Pipeline != nil {
				if err := validateRemotePipelineResult(req, &remoteResult); err != nil {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
				}
			}
			if strings.TrimSpace(remoteResult.Reason) == "" {
				remoteResult.Reason = remoteErrorReason
			}
			if err := validateRemoteMeasurementCounters(checked, remoteResult, r.workMeasurement); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
			}
			if err := validateRemoteWorkClock(checked, remoteResult, effectiveWorkClock(checked, r.workMeasurement, tuning)); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
			}
			if err := validateRemoteWorkMeasurementTime(checked, remoteResult, r.workMeasurement, tuning); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
			}
			if err := validateRemoteWorkMeasurementPeerTime(checked, remoteResult, r.workMeasurement, tuning); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
			}
			if err := validateRemoteWorkMeasurementJudge(checked, remoteResult, r.workMeasurement, tuning); err != nil {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
			}
			var shadowErr error
			if wire.Shadow != nil {
				shadowErr = json.Unmarshal(wire.Shadow, &remoteResult.WorkMeasurementShadow)
			}
			if shadowErr == nil {
				shadowErr = validateRemoteShadow(req, remoteResult, r.workMeasurement, tuning)
			}
			if shadowErr != nil {
				if !req.WorkMeasurementShadow {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: shadowErr.Error()}
				}
				// The shadow never decides: an old runner or an unverifiable
				// comparison leaves the validated primary verdict intact.
				remoteResult.WorkMeasurementShadow = nil
				remoteResult = attachShadow(remoteResult, nil, "remote shadow result rejected: "+shadowErr.Error())
			}
			switch remoteResult.Status {
			case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
			default:
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result status: %q", remoteResult.Status)}
			}
			if remoteResult.TimeMs < 0 || remoteResult.WallTimeMs < 0 || remoteResult.CPUTimeMs < 0 || remoteResult.ProcessCPUTimeMs < 0 || remoteResult.MemoryKB < 0 {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: negative resource measurement"}
			}
			if remoteResult.RawCPUTimeMs != nil && *remoteResult.RawCPUTimeMs < 0 {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: negative raw CPU measurement"}
			}
			if normalization := remoteResult.CPUTimeNormalization; normalization != nil {
				if strings.TrimSpace(normalization.Method) == "" || normalization.ScalePPM <= 0 || normalization.ReferenceTimeNs == 0 || normalization.ObservedTimeNs == 0 {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: malformed CPU normalization metadata"}
				}
			}
			if remoteResult.Score != nil && (math.IsNaN(*remoteResult.Score) || math.IsInf(*remoteResult.Score, 0) || *remoteResult.Score < 0 || *remoteResult.Score > 1) {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote result: score out of range"}
			}
			maxResultSteps := runvalidation.MaxSteps
			if req.Pipeline != nil {
				maxResultSteps = runvalidation.MaxPipelineSteps
			}
			if len(remoteResult.Steps) > maxResultSteps {
				return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote result: too many steps: max %d", maxResultSteps)}
			}
			stdoutResponseLimit := responseStdoutLimitBytes(req)
			stderrResponseLimit := responseStderrLimitBytes(req)
			if runvalidation.UsesSteps(req) && len(remoteResult.Steps) > 0 && len(remoteResult.Steps) <= len(req.Steps) {
				lastReq := &model.RunRequest{Limits: effectiveStepLimits(req.Steps[len(remoteResult.Steps)-1]), CaptureLimits: req.CaptureLimits}
				stdoutResponseLimit = responseStdoutLimitBytes(lastReq)
				stderrResponseLimit = responseStderrLimitBytes(lastReq)
			}
			for i := range remoteResult.Steps {
				step := &remoteResult.Steps[i]
				if req.Pipeline == nil {
					step.CPUTimeByRole = nil
				}
				switch step.Status {
				case model.RunStatusAccepted, model.RunStatusWA, model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
				default:
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: fmt.Sprintf("invalid remote step result status: %q", step.Status)}
				}
				if step.TimeMs < 0 || step.WallTimeMs < 0 || step.CPUTimeMs < 0 || step.ProcessCPUTimeMs < 0 || step.MemoryKB < 0 || step.HandoffBytes < 0 {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote step result: negative measurement"}
				}
				if step.RawCPUTimeMs != nil && *step.RawCPUTimeMs < 0 {
					return model.RunResponse{Status: model.RunStatusInitFail, Reason: "invalid remote step result: negative raw CPU measurement"}
				}
				stepStdoutLimit, stepStderrLimit := stdoutResponseLimit, stderrResponseLimit
				if runvalidation.UsesSteps(req) && i < len(req.Steps) {
					stepReq := &model.RunRequest{Limits: effectiveStepLimits(req.Steps[i]), CaptureLimits: req.CaptureLimits}
					stepStdoutLimit = responseStdoutLimitBytes(stepReq)
					stepStderrLimit = responseStderrLimitBytes(stepReq)
				}
				var truncated bool
				step.Stdout, truncated = capturedOutputValue([]byte(step.Stdout), stepStdoutLimit)
				if truncated {
					step.StdoutTruncated = true
				}
				step.Stderr, truncated = capturedOutputValue([]byte(step.Stderr), stepStderrLimit)
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
			if req.Communication != nil && len(wire.CommunicationCPU) != 0 && !bytes.Equal(wire.CommunicationCPU, []byte("null")) {
				rows := remoteCommunicationCPU(req, &remoteResult, wire.CommunicationCPU)
				remoteResult.CommunicationCPUByRole = rows
				if rows == nil {
					// Drop inconsistent role-derived totals while preserving the native
					// verdict. Workers omitting the new field retain their native totals.
					remoteResult.CPUTimeNs, remoteResult.RawCPUTimeNs = 0, nil
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
			// Peer coordinators enforce CPU limits for each role. Their total
			// also includes the judge and other participants, so comparing it
			// with one contestant's allowance would change a valid verdict.
			// A validated Durante judgment compares model time to the nominal
			// allowance. Its raw CPU belongs to the independent safety envelope.
			if !req.WorkMeasurementJudge && req.Interactor == nil && req.Communication == nil {
				remoteResult.Status, remoteResult.Reason, remoteResult.VerdictSource = applyFinalCPUTimeStatus(remoteResult.Status, remoteResult.Reason, remoteResult.VerdictSource, remoteResult.CPUTimeMs, req.Limits.TimeMs, strings.HasPrefix(remoteResult.VerdictSource, "cpu_time_cgroup"))
			}
			policy := r.workMeasurement
			if (legacySteps || standaloneInteractive) && observationBudget == 0 {
				// Retain the native stage identities, but no replay report is eligible.
				policy.ResourcePolicy = nil
			}
			remoteResult.WorkMeasurementObservation = remoteObservation(req, remoteResult, policy, wire.Observation)
			return remoteResult
		}
	}
}

type remoteLogBudget struct {
	event, stream  int
	stdout, stderr int
}

func remoteLogLimits(req *model.RunRequest) remoteLogBudget {
	if !runvalidation.UsesSteps(req) {
		limit := min(outputLimitBytes(req), maxResponseCaptureBytes)
		return remoteLogBudget{event: limit, stream: limit, stdout: responseStdoutLimitBytes(req), stderr: responseStderrLimitBytes(req)}
	}
	// Legacy log events have no step identifier. Bound the stream by the sum
	// of the budgets that the local runner applies independently to each step.
	// Display clipping is separate from the larger protocol safety bound.
	var budget remoteLogBudget
	for _, step := range req.Steps[:min(len(req.Steps), runvalidation.MaxSteps)] {
		stepReq := &model.RunRequest{Limits: effectiveStepLimits(step), CaptureLimits: req.CaptureLimits}
		limit := min(outputLimitBytes(stepReq), maxResponseCaptureBytes)
		budget.event = max(budget.event, limit)
		budget.stream += limit
		budget.stdout += responseStdoutLimitBytes(stepReq)
		budget.stderr += responseStderrLimitBytes(stepReq)
	}
	return budget
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
