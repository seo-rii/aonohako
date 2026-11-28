//go:build !durante_v12 && !durante_v13

package durante

// Runtime source patch digests for this worker's selected installation.
const RuntimePatchSHA256 = legacyRuntimePatchSHA256
const RuntimeCodeReplayPatchSHA256 = legacyRuntimeCodeReplayPatchSHA256
