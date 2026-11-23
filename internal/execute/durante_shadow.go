package execute

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"aonohako/internal/config"
	"aonohako/internal/durante"
	"aonohako/internal/model"
	"aonohako/internal/runvalidation"
)

// shadowJudgeContextKey marks the internal shadow execution. It lets judge
// admission accept a judge-mode request under mode "shadow" without allowing
// clients to obtain verdicts from it; it cannot be set from a request.
type shadowJudgeContextKey struct{}

func withShadowJudge(ctx context.Context) context.Context {
	return context.WithValue(ctx, shadowJudgeContextKey{}, true)
}

func isShadowJudge(ctx context.Context) bool {
	marked, _ := ctx.Value(shadowJudgeContextKey{}).(bool)
	return marked
}

// shadowGateUnavailable reports why the operator configuration cannot run a
// shadow for this request, or "" when it can. These gates never reject the
// request: the authoritative primary verdict is unaffected by shadow policy.
func shadowGateUnavailable(req *model.RunRequest, cfg durante.Config) string {
	if !cfg.Judge.Enabled {
		return "judge shadow mode is not enabled on this deployment"
	}
	family, ok := durante.FamilyForLanguage(req.Lang)
	if !ok || !cfg.Judge.JudgesFamily(family) {
		return "judge shadow mode is not enabled for this language family"
	}
	return ""
}

func trimShadowResponse(response model.RunResponse) *model.RunResponse {
	response.Stdout, response.Stderr = "", ""
	response.StdoutTruncated, response.StderrTruncated = false, false
	response.SidecarOutputs, response.SidecarErrors = nil, nil
	response.WorkMeasurementShadow = nil
	return &response
}

// attachShadow records a comparison on the authoritative response. Only the
// shadow block is added; the primary verdict and resource values are kept.
func attachShadow(primary model.RunResponse, shadow *model.RunResponse, unavailable string) model.RunResponse {
	block := &model.WorkMeasurementShadow{
		Version: 1, Authority: durante.Authority,
		PrimaryStatus: primary.Status, PrimaryCPUTimeMs: primary.CPUTimeMs,
	}
	if shadow != nil {
		block.ShadowStatus = shadow.Status
		block.Response = trimShadowResponse(*shadow)
	}
	switch {
	case unavailable != "":
		block.Agreement, block.Unavailable = model.ShadowUnavailable, unavailable
	case shadow == nil:
		block.Agreement, block.Unavailable = model.ShadowUnavailable, "shadow execution produced no result"
	case shadow.WorkMeasurementJudgment == nil:
		block.Agreement = model.ShadowUnavailable
		block.Unavailable = "shadow execution produced no judgment: " + shadow.Reason
	case shadow.WorkMeasurementJudgment.State == "resource_exhausted":
		block.Agreement, block.Unavailable = model.ShadowUnavailable, "raw resource limit stopped the shadow execution"
	case shadow.WorkMeasurementJudgment.State == "cancelled":
		block.Agreement, block.Unavailable = model.ShadowUnavailable, "shadow execution was cancelled"
	default:
		block.Agreement = model.ShadowDisagree
		if shadow.Status == primary.Status {
			block.Agreement = model.ShadowAgree
		}
	}
	primary.WorkMeasurementShadow = block
	return primary
}

// runWithShadow executes the authoritative request unchanged, then a separate
// judge-mode execution of the same input, and attaches their comparison.
func (s *Service) runWithShadow(ctx context.Context, req *model.RunRequest, hooks Hooks) model.RunResponse {
	if err := runvalidation.ValidateWorkMeasurementShadow(req); err != nil {
		return model.RunResponse{Status: model.RunStatusInitFail, Reason: err.Error()}
	}
	primaryReq, shadowReq := runvalidation.ShadowRequests(req)
	if reason := shadowGateUnavailable(req, s.workMeasurement); reason != "" {
		return attachShadow(s.runRequest(ctx, &primaryReq, hooks), nil, reason)
	}
	var snapshot *stdinSnapshot
	if strings.TrimSpace(req.StdinURL) != "" {
		// Capture the URL at the point where the primary would normally open
		// it, after admission, and share the file with every later execution.
		snapshot = &stdinSnapshot{hash: sha256.New()}
		defer snapshot.close()
		ctx = context.WithValue(ctx, shadowStdinKey{}, snapshot)
	}
	primary := s.runRequest(ctx, &primaryReq, hooks)
	if snapshot != nil && snapshot.path == "" {
		return attachShadow(primary, nil, "primary did not acquire the URL input")
	}
	if err := ctx.Err(); err != nil {
		return attachShadow(primary, nil, "request cancelled before the shadow execution")
	}
	// The shadow never emits logs or images: the stream belongs to the primary.
	shadow := s.runRequest(withShadowJudge(ctx), &shadowReq, Hooks{})
	compared := attachShadow(primary, &shadow, "")
	if snapshot != nil {
		compared.WorkMeasurementShadow.StdinSHA256 = hex.EncodeToString(snapshot.hash.Sum(nil))
	}
	return compared
}

// shadowPrimaryRequest is the request that the authoritative portion of a
// shadow response must be validated against.
func shadowPrimaryRequest(req *model.RunRequest) *model.RunRequest {
	if !req.WorkMeasurementShadow {
		return req
	}
	primary, _ := runvalidation.ShadowRequests(req)
	return &primary
}

// validateRemoteShadow recomputes the shadow comparison from the returned raw
// shadow observation and the control plane's own configuration.
func validateRemoteShadow(req *model.RunRequest, response model.RunResponse, cfg durante.Config, tuning config.RuntimeTuningConfig) error {
	block := response.WorkMeasurementShadow
	if !req.WorkMeasurementShadow {
		if block != nil {
			return fmt.Errorf("invalid remote result: unsolicited work measurement shadow")
		}
		return nil
	}
	if block == nil {
		return fmt.Errorf("remote runner returned no shadow result")
	}
	if block.Version != 1 || block.Authority != durante.Authority || block.PrimaryStatus != response.Status || block.PrimaryCPUTimeMs != response.CPUTimeMs {
		return fmt.Errorf("invalid remote result: shadow does not describe the returned primary verdict")
	}
	// Earlier runners re-fetched stdin_url for the shadow. Only newer runners
	// identify the single snapshot used by both executions.
	if block.Response != nil && strings.TrimSpace(req.StdinURL) != "" {
		digest, err := hex.DecodeString(block.StdinSHA256)
		if err != nil || len(digest) != sha256.Size || block.StdinSHA256 != hex.EncodeToString(digest) {
			return fmt.Errorf("invalid remote result: URL shadow requires a shared stdin SHA-256")
		}
	} else if block.StdinSHA256 != "" {
		return fmt.Errorf("invalid remote result: unexpected shared stdin SHA-256")
	}
	if block.Response == nil {
		if block.Agreement != model.ShadowUnavailable || block.Unavailable == "" || block.ShadowStatus != "" {
			return fmt.Errorf("invalid remote result: shadow without an observation must be unavailable")
		}
		return nil
	}
	shadow := *block.Response
	if shadow.WorkMeasurementShadow != nil || shadow.WorkMeasurementObservation != nil || shadow.Stdout != "" || shadow.Stderr != "" ||
		shadow.StdoutTruncated || shadow.StderrTruncated || len(shadow.SidecarOutputs) != 0 || len(shadow.SidecarErrors) != 0 ||
		len(shadow.Steps) != 0 || shadow.Match != nil {
		return fmt.Errorf("invalid remote result: shadow observation is not trimmed")
	}
	_, shadowReq := runvalidation.ShadowRequests(req)
	if d := shadow.WorkMeasurementExecution; d != nil && d.Clock != nil {
		return fmt.Errorf("invalid remote result: unsolicited shadow work clock")
	}
	if err := validateRemoteMeasurementCounters(&shadowReq, shadow, cfg); err != nil {
		return fmt.Errorf("shadow %w", err)
	}
	if err := validateRemoteWorkMeasurementTime(&shadowReq, shadow, cfg, tuning); err != nil {
		return fmt.Errorf("shadow %w", err)
	}
	if err := validateRemoteWorkMeasurementJudge(&shadowReq, shadow, cfg); err != nil {
		return fmt.Errorf("shadow %w", err)
	}
	expected := attachShadow(response, &shadow, "").WorkMeasurementShadow
	if block.Agreement != expected.Agreement || block.ShadowStatus != expected.ShadowStatus || block.Unavailable != expected.Unavailable {
		return fmt.Errorf("invalid remote result: shadow agreement mismatch")
	}
	return nil
}
