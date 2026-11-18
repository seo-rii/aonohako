package reference

import (
	"strings"
	"testing"

	"aonohako/internal/durante"
)

func TestRawReferenceRejectsUnrequestedTimeEligibility(t *testing.T) {
	dataset := collectionTestDataset()
	response := collectionTestMetered(dataset)
	response.WorkMeasurementExecution.TimeEstimateEligibility = &durante.TimeEligibility{Version: 1, Eligible: true, ReasonCodes: []string{}}
	if err := validateCollectedMetered(response, dataset); err == nil {
		t.Fatal("raw collection accepted unsolicited conversion metadata")
	}
	full := syntheticDataset()
	full.Samples[0].Metered.WorkMeasurementExecution.TimeEstimateEligibility = response.WorkMeasurementExecution.TimeEstimateEligibility
	if evaluation, err := Fit(full, syntheticFitOptions()); err == nil || evaluation.Passed || evaluation.CandidatePolicy != nil {
		t.Fatal("raw fitter accepted unsolicited conversion metadata", evaluation, err)
	}
}

func TestReferenceCollectionUsesTheSharedSignalAndREPExclusionGate(t *testing.T) {
	dataset := collectionTestDataset()
	response := collectionTestMetered(dataset)
	r := response.WorkMeasurement
	r.SignalsObserved, r.Quality = 1, "signals_observed"
	unsupported := uint64(1)
	r.UnsupportedREPEntries, r.REPTelemetryStatus = &unsupported, durante.REPPartial
	err := validateCollectedMetered(response, dataset)
	if err == nil || !strings.Contains(err.Error(), durante.TimeSignalsObserved) || !strings.Contains(err.Error(), durante.TimeUnsupportedREP) {
		t.Fatal("reference collection hid a shared exclusion reason", err)
	}
}
