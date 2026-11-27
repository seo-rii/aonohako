//go:build durante_v9

package durante

// EngineProfile identifies the execution engine and client selected for this worker.
const EngineProfile = engineProfileV9

// SourceCommit pins the reviewed Durante source for EngineProfile.
const SourceCommit = sourceCommitV9

// The runtime patches this worker installs.
const runtimePatchSHA256 = runtimePatchSHA256V7
const runtimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256V7
