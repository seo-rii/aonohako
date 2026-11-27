//go:build durante_v10

package durante

// EngineProfile identifies the execution engine and client selected for this worker.
const EngineProfile = engineProfileV10

// SourceCommit pins the reviewed Durante source for EngineProfile.
const SourceCommit = sourceCommitV10

// The runtime patches this worker installs.
const runtimePatchSHA256 = runtimePatchSHA256V7
const runtimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256V7
