//go:build durante_v13

package durante

// EngineProfile identifies the execution engine and client selected for this worker.
const EngineProfile = engineProfileV13

// SourceCommit pins the reviewed Durante source for EngineProfile.
const SourceCommit = sourceCommitV13

// Runtime source patch digests for this worker's selected installation.
const RuntimePatchSHA256 = v13RuntimePatchSHA256
const RuntimeCodeReplayPatchSHA256 = v12RuntimeCodeReplayPatchSHA256
