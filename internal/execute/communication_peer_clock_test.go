package execute

import (
	"testing"

	"aonohako/internal/model"
)

func TestCommunicationPeerClockVerdictRequiresTheForfeitingSeatsReceipt(t *testing.T) {
	req := competitiveRequest()
	req.WorkMeasurementPeerTime = &model.PeerTimeRequest{Version: 2}
	zero, seat0, seat1 := 0, 0, 1
	crossed := true
	makeResponse := func() model.RunResponse {
		match := communicationMatchFailure(req, "")
		match.Outcome, match.Retriable, match.Wins = "forfeit", false, &[2]int{0, 20}
		match.Participants[0].Status, match.Participants[0].VerdictSource = model.RunStatusTLE, "durante_work_clock"
		match.Participants[1].Status, match.Participants[1].VerdictSource = "not_finished", "communication:forfeit_shutdown"
		return model.RunResponse{Status: model.RunStatusAccepted, VerdictSource: "communication:match", Match: match, StartedParticipants: 2, WorkMeasurements: []model.WorkMeasurementProcess{
			{Role: "participant", ParticipantIndex: &seat0, ExecutionStatus: model.RunStatusTLE, TerminationSource: "durante_work_clock", TimeBudget: &model.PeerTimeBudgetResult{Exceeded: &crossed}},
			{Role: "participant", ParticipantIndex: &seat1, ExecutionStatus: model.RunStatusInitFail, TerminationSource: "durante_peer_cancelled"},
			{Role: "manager", ExecutionStatus: "OK", ExitCode: &zero},
		}}
	}
	if err := validateCommunicationPeerClockVerdict(req, makeResponse()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*model.RunResponse)
	}{
		{"raw contest timeout", func(r *model.RunResponse) { r.WorkMeasurements[0].TerminationSource = "wall_time" }},
		{"unverified threshold", func(r *model.RunResponse) { r.WorkMeasurements[0].TimeBudget.Exceeded = nil }},
		{"independent manager failure", func(r *model.RunResponse) {
			r.WorkMeasurements[2].ExecutionStatus = model.RunStatusInitFail
			r.WorkMeasurements[2].TerminationSource = "durante_peer_clock_error"
		}},
		{"winner and loser swapped", func(r *model.RunResponse) { r.Match.Wins = &[2]int{20, 0} }},
		{"crossing reassigned", func(r *model.RunResponse) { r.Match.Participants[0].ProgramID = r.Match.Participants[1].ProgramID }},
		{"sibling raw watchdog scored", func(r *model.RunResponse) { r.WorkMeasurements[1].TerminationSource = "durante_peer_clock_error" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := makeResponse()
			tc.change(&response)
			if err := validateCommunicationPeerClockVerdict(req, response); err == nil {
				t.Fatal("accepted inconsistent match")
			}
		})
	}
}
