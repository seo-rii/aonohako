package execute

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestWriteSandboxRequestDeliversPayloadAndEOF(t *testing.T) {
	for _, size := range []int{0, 1 << 20} {
		t.Run(map[int]string{0: "empty", 1 << 20: "larger-than-pipe"}[size], func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			payload := bytes.Repeat([]byte("x"), size)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			type readResult struct {
				data []byte
				err  error
			}
			readDone := make(chan readResult, 1)
			go func() {
				data, err := io.ReadAll(reader)
				readDone <- readResult{data, err}
			}()
			if err := writeSandboxRequest(ctx, writer, payload); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-readDone:
				if result.err != nil || !bytes.Equal(result.data, payload) {
					t.Fatalf("request payload changed: bytes=%d error=%v", len(result.data), result.err)
				}
			case <-ctx.Done():
				t.Fatal("request reader did not receive EOF")
			}
		})
	}
}

func TestWriteSandboxRequestReportsPipeErrors(t *testing.T) {
	for _, end := range []string{"reader", "writer"} {
		t.Run("closed-"+end, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			want := error(syscall.EPIPE)
			if end == "reader" {
				_ = reader.Close()
			} else {
				_ = writer.Close()
				want = os.ErrClosed
			}
			if err := writeSandboxRequest(context.Background(), writer, []byte("request")); !errors.Is(err, want) {
				t.Fatalf("request error = %v, want %v", err, want)
			}
		})
	}
}

func TestWriteSandboxRequestCancelsBlockedWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	done := make(chan error, 1)
	go func() {
		done <- writeSandboxRequest(ctx, writer, bytes.Repeat([]byte("x"), 4<<20))
	}()
	// Observe the write starting, then stop consuming before the request can
	// fit in the pipe. Cancellation must unblock it without closing the reader.
	if err := reader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var first [1]byte
	if _, err := io.ReadFull(reader, first[:]); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		t.Fatalf("large write completed before cancellation: %v", err)
	default:
	}
	cause := errors.New("sandbox startup canceled")
	cancel(cause)
	select {
	case err := <-done:
		if !errors.Is(err, cause) {
			t.Fatalf("request cancellation = %v, want %v", err, cause)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request write remained blocked after cancellation")
	}
	if _, err := io.ReadAll(reader); err != nil {
		t.Fatalf("canceled request did not close its pipe: %v", err)
	}
}

func TestWriteSandboxRequestCanceledBeforeWrite(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := writeSandboxRequest(ctx, writer, []byte("request")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
	if err := reader.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil || len(data) != 0 {
		t.Fatalf("canceled request wrote data or left the pipe open: bytes=%d error=%v", len(data), err)
	}
}
