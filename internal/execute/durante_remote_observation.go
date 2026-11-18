package execute

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"aonohako/internal/durante"
	"aonohako/internal/model"
)

// A missing or newer diagnostic payload can only affect the optional replay.
// The remote native result must already have passed its ordinary validation.
func remoteObservation(req *model.RunRequest, primary model.RunResponse, cfg durante.Config, raw json.RawMessage) *model.WorkMeasurementObservation {
	if !automaticObservationRequested(req, cfg) {
		return nil
	}
	unavailable := func(reason string) *model.WorkMeasurementObservation {
		return &model.WorkMeasurementObservation{Version: 1, Authority: durante.Authority, State: "unavailable", Reason: reason}
	}
	if reason := observationEligible(req, cfg); reason != "" {
		return unavailable(reason)
	}
	if len(raw) == 0 {
		return unavailable("remote runner returned no observation")
	}
	var block model.WorkMeasurementObservation
	if err := json.Unmarshal(raw, &block); err != nil || block.Version != 1 || block.Authority != durante.Authority {
		return unavailable("incompatible remote observation")
	}
	if block.State == "unavailable" && block.Report == nil && block.StdinSHA256 == "" && block.OutputCheck == "" && strings.TrimSpace(block.Reason) != "" && len(block.Reason) <= 512 {
		return &block
	}
	check := observationOutputCheck(req, primary)
	if check == "" || block.OutputCheck != check || block.State != "complete" || block.Reason != "" || !observationReportMatches(req, cfg, block.Report) {
		return unavailable("remote observation is inconsistent with the request or primary verdict")
	}
	if strings.TrimSpace(req.StdinURL) != "" {
		digest, err := hex.DecodeString(block.StdinSHA256)
		if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != block.StdinSHA256 {
			return unavailable("remote observation has no valid shared input digest")
		}
	} else if block.StdinSHA256 != "" {
		return unavailable("unexpected remote observation input digest")
	}
	return &block
}
