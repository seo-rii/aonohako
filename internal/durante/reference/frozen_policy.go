package reference

import (
	"fmt"

	"aonohako/internal/durante"
)

// FrozenPolicy binds parsed coefficients to the exact standalone policy file.
type FrozenPolicy struct {
	policy durante.TimePolicy
	digest string
}

func (p *FrozenPolicy) Policy() durante.TimePolicy {
	return p.policy
}

func (p *FrozenPolicy) SHA256() string {
	if p == nil {
		return ""
	}
	return p.digest
}

const maxFrozenPolicyBytes = 4096

func ParseFrozenPolicy(raw []byte) (*FrozenPolicy, error) {
	if len(raw) > maxFrozenPolicyBytes {
		return nil, fmt.Errorf("frozen policy exceeds %d bytes", maxFrozenPolicyBytes)
	}
	text := string(raw)
	policy, err := durante.ParseTimePolicy(text)
	if err != nil {
		return nil, fmt.Errorf("frozen policy: %w", err)
	}
	if policy == nil {
		return nil, fmt.Errorf("frozen policy is empty")
	}
	return &FrozenPolicy{policy: *policy, digest: collectionSHA([]byte(text))}, nil
}

func LoadFrozenPolicy(path string) (*FrozenPolicy, error) {
	raw, err := collectionReadFile(path, maxFrozenPolicyBytes)
	if err != nil {
		return nil, err
	}
	return ParseFrozenPolicy(raw)
}
