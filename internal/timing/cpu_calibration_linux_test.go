//go:build linux

package timing

import "testing"

func TestCPUCalibrationWorkIsDeterministic(t *testing.T) {
	first := cpuCalibrationWork(10_000)
	second := cpuCalibrationWork(10_000)
	if first == 0 || first != second {
		t.Fatalf("CPU calibration checksum is unstable: first=%d second=%d", first, second)
	}
}

func TestCalibrateCPUProducesUsableNormalizer(t *testing.T) {
	normalizer, err := CalibrateCPU(DefaultCPUReferenceTimeNs)
	if err != nil {
		t.Fatalf("CalibrateCPU: %v", err)
	}
	info, ok := normalizer.Info()
	if !ok {
		t.Fatal("calibrated normalizer is disabled")
	}
	if info.Method != CPUNormalizationMethod || info.ScalePPM <= 0 {
		t.Fatalf("unexpected calibration info: %+v", info)
	}
	t.Logf("calibration info: %+v", info)
}
