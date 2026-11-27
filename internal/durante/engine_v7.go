//go:build !durante_v8 && !durante_v9 && !durante_v10 && !durante_v11 && !durante_v12

package durante

// EngineProfile identifies the execution engine and client selected for this worker.
const EngineProfile = engineProfileV7

// SourceCommit pins the reviewed Durante source for EngineProfile.
const SourceCommit = sourceCommitV7

// The runtime patches this worker installs.
const runtimePatchSHA256 = runtimePatchSHA256V7
const runtimeCodeReplayPatchSHA256 = runtimeCodeReplayPatchSHA256V7
