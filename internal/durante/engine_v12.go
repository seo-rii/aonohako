//go:build durante_v12

package durante

// EngineProfile identifies the execution engine and client selected for this worker.
const EngineProfile = engineProfileV12

// SourceCommit pins the reviewed Durante source for EngineProfile.
const SourceCommit = sourceCommitV12

// The runtime patches this worker installs.
const runtimePatchSHA256 = runtimePatchSHA256V12
const runtimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256V12
