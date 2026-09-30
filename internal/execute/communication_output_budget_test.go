package execute

import (
	"io"
	"testing"

	"aonohako/internal/model"
)

func TestCommunicationOutputBudgetForwardsLargeLegalStreams(t *testing.T) {
	for _, size := range []int{(64 << 20) + 1, 100002480, 128 << 20} {
		req := &model.RunRequest{Limits: model.Limits{OutputBytes: 128 << 20}}
		cancelled := false
		writer := &communicationOutputWriter{
			target:    io.Discard,
			remaining: int64(communicationOutputLimitBytes(req)),
			onLimit:   func() { cancelled = true },
		}
		chunk := make([]byte, 64<<10)
		for sent := 0; sent < size; {
			n := min(len(chunk), size-sent)
			if written, err := writer.Write(chunk[:n]); err != nil || written != n {
				t.Fatalf("Write() = %d, %v", written, err)
			}
			sent += n
		}
		if cancelled || writer.exceeded.Load() || writer.remaining != int64((128<<20)-size) {
			t.Fatalf("legal %d-byte stream was not fully forwarded: remaining=%d cancelled=%v", size, writer.remaining, cancelled)
		}
		if size == 128<<20 {
			_, _ = writer.Write([]byte{0})
			if !cancelled || !writer.exceeded.Load() {
				t.Fatal("one byte above 128 MiB must cancel participant")
			}
		}
	}
}

func TestCommunicationOutputBudgetDefaultsAndClamps(t *testing.T) {
	if got := communicationOutputLimitBytes(nil); got != defaultMaxOutputBytes {
		t.Fatalf("nil request default = %d", got)
	}
	for _, bytes := range []int{-1, 0, 1024, 64 << 20, 128 << 20, (128 << 20) + 1} {
		req := &model.RunRequest{Limits: model.Limits{OutputBytes: bytes}}
		want := min(bytes, 128<<20)
		if bytes <= 0 {
			want = defaultMaxOutputBytes
		}
		if got := communicationOutputLimitBytes(req); got != want {
			t.Fatalf("communication budget(%d) = %d, want %d", bytes, got, want)
		}
		ordinaryWant := min(bytes, hardMaxOutputBytes)
		if bytes <= 0 {
			ordinaryWant = defaultMaxOutputBytes
		}
		if got := outputLimitBytes(req); got != ordinaryWant {
			t.Fatalf("ordinary budget(%d) = %d, want %d", bytes, got, ordinaryWant)
		}
	}
}
