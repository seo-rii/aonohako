// Package compilewire defines the compile response contract shared by artifact
// collection, the API producer and the remote SSE consumer.
package compilewire

const (
	ArtifactBytes      = 16 << 20
	ArtifactTotalBytes = 48 << 20
	ArtifactCount      = 512
	ArtifactNameBytes  = 4096
	OutputBytes        = 1 << 20
	ReasonBytes        = 1 << 20
	ReasonCodeBytes    = 128

	// 48 MiB of artifacts require 64 MiB of Base64, plus per-file padding.
	// At most 512 names of 4096 bytes and three 1 MiB diagnostic fields can
	// expand sixfold in JSON: another 30 MiB. The remaining 2 MiB covers
	// padding, object keys, modes, status, reason code and truncation flags.
	SSEEventBytes = 96 << 20
	SSELineBytes  = SSEEventBytes + len("data: \n")
	// Reserve one full result frame. The remaining almost 32 MiB accommodates
	// the two duplicated log fields (12 MiB), an error (6 MiB), progress and
	// heartbeats. Writers and readers also enforce this aggregate limit.
	SSEStreamBytes = 128 << 20
)
