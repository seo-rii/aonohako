//go:build !linux

package timing

import "fmt"

func CalibrateCPU(referenceTimeNs uint64) (CPUNormalizer, error) {
	return CPUNormalizer{}, fmt.Errorf("CPU normalization calibration requires Linux")
}
