package execute

import (
	"encoding/json"
	"math"

	"aonohako/internal/model"
	"aonohako/internal/timing"
)

// Sum available samples without wrapping. One missing sample suppresses the
// published precise total while other roles retain their individual readings.
type communicationNanoSum struct {
	value   uint64
	missing bool
}

func (s *communicationNanoSum) add(ns *uint64) bool {
	if ns == nil {
		s.missing = true
		return true
	}
	if *ns > math.MaxUint64-s.value {
		return false
	}
	s.value += *ns
	return true
}

func (s communicationNanoSum) total(started int) *uint64 {
	if started == 0 || s.missing {
		return nil
	}
	return &s.value
}

func remoteCommunicationCPU(req *model.RunRequest, native *model.RunResponse, raw json.RawMessage) []model.CommunicationRoleCPU {
	if len(raw) == 0 || native.CPUTimeMs < 0 || (req.Communication.Version == 1 && native.Match != nil) {
		return nil
	}
	expected, err := workMeasurementRequestRoles(req)
	// A missing start, interruption or CPU reading cannot establish false
	// or a measured zero. Preserve that distinction at the wire boundary.
	var encoded []struct {
		model.CommunicationRoleCPU
		Started     *bool  `json:"started"`
		Interrupted *bool  `json:"interrupted"`
		CPUTimeMs   *int64 `json:"cpu_time_ms"`
	}
	if err != nil || json.Unmarshal(raw, &encoded) != nil || len(encoded) != len(expected) {
		return nil
	}
	rows := make([]model.CommunicationRoleCPU, len(encoded))
	for i, r := range encoded {
		if r.Started == nil || r.Interrupted == nil || r.CPUTimeMs == nil {
			return nil
		}
		rows[i] = r.CommunicationRoleCPU
		rows[i].Started, rows[i].Interrupted, rows[i].CPUTimeMs = *r.Started, *r.Interrupted, *r.CPUTimeMs
	}
	var calibration timing.CPUNormalizer
	if norm := native.CPUTimeNormalization; norm != nil {
		calibration, err = timing.NewCPUNormalizer(norm.Method, norm.ReferenceTimeNs, norm.ObservedTimeNs)
		info, ok := calibration.Info()
		if err != nil || !ok || norm.Method != timing.CPUNormalizationMethod || info.ScalePPM != norm.ScalePPM {
			return nil
		}
	}
	remainingMs := native.CPUTimeMs
	var remainingRawMs int64
	if native.RawCPUTimeMs != nil {
		remainingRawMs = *native.RawCPUTimeMs
	}
	var ns, rawNs communicationNanoSum
	started, seats := 0, 0
	hasRawMs := false
	for i, row := range rows {
		role := expected[i]
		if row.Role != role.role || row.ProgramID != role.programID || !equalPipelineCounter(row.ParticipantIndex, role.participantIndex) ||
			!roleCPUMillisecondsMatch(row.CPUTimeMs, row.CPUTimeNs) || row.CPUTimeMs > remainingMs {
			return nil
		}
		remainingMs -= row.CPUTimeMs
		limit := req.Limits.TimeMs
		if row.Role == "manager" {
			limit = communicationManagerLimits(req.Limits).TimeMs
		} else {
			if row.Started {
				seats++
			}
			if req.Communication.Version == 2 && !matchSeatCPUConsistent(native.Match, i, row) {
				return nil
			}
		}
		if row.CPULimitMs != limit || !communicationCPUExecutionMatches(row, native.WorkMeasurements, i) {
			return nil
		}
		if !row.Started {
			if row.CPUTimeNs != nil || row.RawCPUTimeNs != nil || row.RawCPUTimeMs != nil {
				return nil
			}
			continue
		}
		started++
		if !ns.add(row.CPUTimeNs) || !rawNs.add(row.RawCPUTimeNs) {
			return nil
		}
		if row.RawCPUTimeMs != nil {
			hasRawMs = true
			if !calibration.Enabled() || !roleCPUMillisecondsMatch(*row.RawCPUTimeMs, row.RawCPUTimeNs) || *row.RawCPUTimeMs > remainingRawMs {
				return nil
			}
			remainingRawMs -= *row.RawCPUTimeMs
		}
		if row.RawCPUTimeNs != nil {
			if row.RawCPUTimeMs == nil || row.CPUTimeNs == nil || calibration.NormalizeNanoseconds(*row.RawCPUTimeNs) != *row.CPUTimeNs {
				return nil
			}
		} else if calibration.Enabled() && (row.CPUTimeNs != nil ||
			(row.RawCPUTimeMs != nil && calibration.NormalizeMillis(*row.RawCPUTimeMs) != row.CPUTimeMs)) {
			return nil
		}
	}
	if seats != native.StartedParticipants || remainingMs != 0 || remainingRawMs != 0 || hasRawMs != (native.RawCPUTimeMs != nil) {
		return nil
	}
	var expectedNs uint64 // The legacy scalar uses zero when a precise total is unavailable.
	if total := ns.total(started); total != nil {
		expectedNs = *total
	}
	if native.CPUTimeNs != expectedNs || !equalPipelineCounter(native.RawCPUTimeNs, rawNs.total(started)) {
		return nil
	}
	return rows
}

func matchSeatCPUConsistent(match *model.MatchResult, index int, row model.CommunicationRoleCPU) bool {
	if match == nil || len(match.Participants) != 2 {
		return false
	}
	seat := match.Participants[index]
	if seat.Seat != index || seat.ProgramID != row.ProgramID {
		return false
	}
	// A coordinator-level failure can rebuild the public match as an unscored
	// judge_error, discarding its per-seat display measurements.
	return match.Outcome == "judge_error" || ((match.Outcome == "completed" || match.Outcome == "forfeit") && seat.CPUTimeMs == row.CPUTimeMs)
}

func communicationCPUExecutionMatches(row model.CommunicationRoleCPU, measured []model.WorkMeasurementProcess, index int) bool {
	switch row.ExecutionStatus {
	case "OK":
		if !row.Started || row.ExitCode == nil {
			return false
		}
	case model.RunStatusTLE, model.RunStatusMLE, model.RunStatusWLE, model.RunStatusRE, model.RunStatusInitFail:
	default:
		return false
	}
	if !row.Started && (row.ExecutionStatus != model.RunStatusInitFail || row.ExitCode != nil) {
		return false
	}
	if row.ExitCode != nil && (*row.ExitCode < -1 || *row.ExitCode > 255) {
		return false
	}
	if len(measured) == 0 {
		return true
	}
	if index >= len(measured) {
		return false
	}
	m := measured[index]
	return m.ExecutionStatus == row.ExecutionStatus && equalPipelineCounter(m.ExitCode, row.ExitCode) &&
		(!(row.Interrupted || !row.Started) || m.Report == nil || !m.Report.Complete)
}
