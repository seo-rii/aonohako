//go:build !linux

package timing

import "time"

func MonotonicNow() int64 {
	return time.Now().UnixNano()
}

func SinceMillis(startNs int64) int64 {
	end := MonotonicNow()
	if end <= startNs {
		return 0
	}
	return (end - startNs) / int64(time.Millisecond)
}

func CurrentProcessCPUTimeNs() (uint64, error) {
	return 0, nil
}

func ProcessCPUTimeNs(pid int) (uint64, error) {
	return 0, nil
}

type ProcessCPUSampler struct{}

func StartProcessCPUSampler(pid int) *ProcessCPUSampler {
	return &ProcessCPUSampler{}
}

func (s *ProcessCPUSampler) Stop() uint64 {
	return 0
}

func MilliFromDuration(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64(d / time.Millisecond)
}

func MilliFromNanoseconds(v uint64) int64 {
	return int64(v / uint64(time.Millisecond))
}
