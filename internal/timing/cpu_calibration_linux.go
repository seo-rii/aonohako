//go:build linux

package timing

import (
	"fmt"
	"math/bits"
	"runtime"
)

const (
	cpuCalibrationSamples    = 5
	cpuCalibrationIterations = 20_000_000
)

var cpuCalibrationSink uint64

func CalibrateCPU(referenceTimeNs uint64) (CPUNormalizer, error) {
	if referenceTimeNs < MinimumCPUCalibrationTimeNs || referenceTimeNs > MaximumCPUCalibrationTimeNs {
		return CPUNormalizer{}, fmt.Errorf(
			"CPU normalization reference time %d ns is outside [%d, %d]",
			referenceTimeNs,
			MinimumCPUCalibrationTimeNs,
			MaximumCPUCalibrationTimeNs,
		)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Warm the code and thread before collecting fixed-work CPU samples.
	cpuCalibrationSink = cpuCalibrationWork(cpuCalibrationIterations / 10)
	samples := make([]uint64, 0, cpuCalibrationSamples)
	var expectedChecksum uint64
	for sample := 0; sample < cpuCalibrationSamples; sample++ {
		before, err := CurrentThreadCPUTimeNs()
		if err != nil {
			return CPUNormalizer{}, fmt.Errorf("read CPU calibration start clock: %w", err)
		}
		checksum := cpuCalibrationWork(cpuCalibrationIterations)
		after, err := CurrentThreadCPUTimeNs()
		if err != nil {
			return CPUNormalizer{}, fmt.Errorf("read CPU calibration end clock: %w", err)
		}
		if after <= before {
			return CPUNormalizer{}, fmt.Errorf("CPU calibration clock did not advance")
		}
		if sample == 0 {
			expectedChecksum = checksum
		} else if checksum != expectedChecksum {
			return CPUNormalizer{}, fmt.Errorf("CPU calibration checksum changed between samples")
		}
		cpuCalibrationSink = checksum
		samples = append(samples, after-before)
	}
	runtime.KeepAlive(cpuCalibrationSink)

	observedTimeNs, err := calibrationMedian(samples)
	if err != nil {
		return CPUNormalizer{}, err
	}
	if observedTimeNs < MinimumCPUCalibrationTimeNs || observedTimeNs > MaximumCPUCalibrationTimeNs {
		return CPUNormalizer{}, fmt.Errorf(
			"CPU calibration observed time %d ns is outside [%d, %d]",
			observedTimeNs,
			MinimumCPUCalibrationTimeNs,
			MaximumCPUCalibrationTimeNs,
		)
	}
	if referenceTimeNs > observedTimeNs*4 || observedTimeNs > referenceTimeNs*4 {
		return CPUNormalizer{}, fmt.Errorf(
			"CPU calibration scale is outside the supported 0.25x..4x range: reference=%d ns observed=%d ns",
			referenceTimeNs,
			observedTimeNs,
		)
	}
	return NewCPUNormalizer(CPUNormalizationMethod, referenceTimeNs, observedTimeNs)
}

func cpuCalibrationWork(iterations uint64) uint64 {
	value := uint64(0x243f6a8885a308d3)
	for i := uint64(0); i < iterations; i++ {
		value ^= value << 7
		value ^= value >> 9
		value += i ^ 0x9e3779b97f4a7c15
		value = bits.RotateLeft64(value, 13)
	}
	return value
}
