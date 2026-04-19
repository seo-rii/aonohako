//go:build linux

package timing

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

const (
	processCPUClockType = 2
)

func MonotonicNow() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return time.Now().UnixNano()
	}
	return ts.Nano()
}

func SinceMillis(startNs int64) int64 {
	end := MonotonicNow()
	if end <= startNs {
		return 0
	}
	return (end - startNs) / int64(time.Millisecond)
}

func CurrentProcessCPUTimeNs() (uint64, error) {
	return readClockNs(unix.CLOCK_PROCESS_CPUTIME_ID)
}

func ProcessCPUTimeNs(pid int) (uint64, error) {
	return readClockNs(processCPUClockID(pid))
}

type ProcessCPUSampler struct {
	stopCh chan struct{}
	doneCh chan uint64
}

func StartProcessCPUSampler(pid int) *ProcessCPUSampler {
	s := &ProcessCPUSampler{
		stopCh: make(chan struct{}),
		doneCh: make(chan uint64, 1),
	}
	go s.run(pid)
	return s
}

func (s *ProcessCPUSampler) Stop() uint64 {
	if s == nil {
		return 0
	}
	close(s.stopCh)
	return <-s.doneCh
}

func (s *ProcessCPUSampler) run(pid int) {
	ticker := time.NewTicker(250 * time.Microsecond)
	defer ticker.Stop()

	var last uint64
	update := func() {
		if v, err := ProcessCPUTimeNs(pid); err == nil {
			last = v
		}
	}

	update()
	for {
		select {
		case <-ticker.C:
			update()
		case <-s.stopCh:
			update()
			s.doneCh <- last
			return
		}
	}
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

func processCPUClockID(pid int) int32 {
	return int32((^uint32(pid) << 3) | processCPUClockType)
}

func readClockNs(clockID int32) (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(clockID, &ts); err != nil {
		return 0, err
	}
	if ts.Sec < 0 || ts.Nsec < 0 {
		return 0, errors.New("negative clock value")
	}
	return uint64(ts.Sec)*uint64(time.Second) + uint64(ts.Nsec), nil
}
