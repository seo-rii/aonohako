package reference

import (
	"fmt"

	"aonohako/internal/durante"
)

// DefaultFitOptions supplies the shared accuracy and repeatability gates for
// fitted and frozen reference policies. Model identities are chosen by the caller.
func DefaultFitOptions() FitOptions {
	return FitOptions{
		MinRepetitions: 3, MinNativeNs: 20_000_000,
		MaxNativeSpreadPPM: 100_000, MaxWorkSpreadPPM: 1_000, MaxErrorPPM: 200_000,
	}
}

// ValidateFrozen measures a supplied diagnostic policy on a separate held-out
// corpus. The fixed release gates apply to every repetition. The resulting
// evaluation never contains a candidate policy for export.
func ValidateFrozen(dataset Dataset, corpus *ValidationCorpus, frozen *FrozenPolicy) (Evaluation, error) {
	options := DefaultFitOptions()
	if frozen == nil || !referenceSHA256(frozen.SHA256()) || dataset.FrozenPolicySHA256 != frozen.SHA256() {
		err := fmt.Errorf("dataset does not match the frozen policy SHA256")
		return Evaluation{Schema: Schema, Authority: durante.Authority, Options: options, Failures: []string{err.Error()}}, err
	}
	policy := frozen.Policy()
	options.ModelID, options.ModelVersion, options.ReferenceID = policy.ModelID, policy.ModelVersion, policy.ReferenceID
	options.Method = policy.Formula()
	return evaluateReference(dataset, options, corpus, &policy)
}
