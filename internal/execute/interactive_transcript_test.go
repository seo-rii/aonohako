package execute

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

type transcriptSink struct{ io.Writer }

func (transcriptSink) Close() error { return nil }

func recordedDialogue(t *testing.T) *interactiveTranscript {
	t.Helper()
	record := new(interactiveTranscript)
	judge := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record}
	player := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record, fromContestant: true}
	for _, action := range []struct {
		to   *interactivePipeWriter
		data []byte
	}{
		{judge, []byte("A")}, {judge, []byte{0}}, {player, []byte("first")},
		{judge, []byte("B")}, {player, []byte("sec")}, {player, []byte("ond")},
	} {
		if n, err := action.to.Write(action.data); n != len(action.data) || err != nil {
			t.Fatal("unable to construct recorded dialogue")
		}
	}
	player.Close()
	judge.Close()
	record.seal(true)
	if !record.ready() || len(record.turns) != 6 {
		t.Fatal("expected four turns and two EOFs")
	}
	return record
}

func TestInteractiveTranscriptReplay(t *testing.T) {
	transcript := recordedDialogue(t)
	for _, scenario := range []string{"match", "changed", "early next reply", "missing", "extra", "early EOF"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			input, send := io.Pipe()
			read, output := io.Pipe()
			seen := make(chan bool, 1)
			go func() { seen <- transcript.play(ctx, send, read) }()
			defer input.Close()
			defer output.Close()

			first := make([]byte, 2)
			if _, err := io.ReadFull(input, first); err != nil || !bytes.Equal(first, []byte{'A', 0}) {
				t.Fatalf("first prompt = %v err=%v", first, err)
			}
			switch scenario {
			case "early next reply":
				_, _ = output.Write([]byte("firstsecond"))
			case "changed":
				_, _ = output.Write([]byte("Third"))
			case "early EOF":
			default:
				// Fragment differently than the two native peers did.
				_, _ = output.Write([]byte("fi"))
				_, _ = output.Write([]byte("rst"))
				next := make([]byte, 1)
				if _, err := io.ReadFull(input, next); err != nil || string(next) != "B" {
					t.Fatal("second prompt unavailable")
				}
				if scenario == "match" {
					_, _ = output.Write([]byte("second"))
				} else if scenario == "extra" {
					_, _ = output.Write([]byte("second!"))
				}
			}
			_ = output.Close()
			if got := <-seen; got != (scenario == "match") || ctx.Err() != nil {
				t.Fatalf("matched=%v, ctx=%v", got, ctx.Err())
			}
		})
	}
}

type transcriptFaultWriter struct{ err error }

func (w transcriptFaultWriter) Write(p []byte) (int, error) { return len(p) - 1, w.err }
func (transcriptFaultWriter) Close() error                  { return nil }

func TestInteractiveTranscriptPrivateBoundsAndFailedWrites(t *testing.T) {
	for _, scenario := range []string{"partial", "post-close", "bytes", "events", "cancelled", "missing EOF"} {
		t.Run(scenario, func(t *testing.T) {
			tr := new(interactiveTranscript)
			j := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: tr}
			c := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: tr, fromContestant: true}
			switch scenario {
			case "partial":
				want := errors.New("partial")
				j.w = transcriptFaultWriter{want}
				if n, err := j.Write([]byte("a!")); n != 1 || err != want || j.Err() != want {
					t.Fatal("recorder changed the native partial write")
				}
			case "post-close":
				j.Close()
				if n, err := j.Write([]byte("private")); n != 0 || err != io.ErrClosedPipe {
					t.Fatal("recorder changed a native closed-pipe write")
				}
			case "bytes":
				p := make([]byte, maxInteractiveTranscriptBytes+1)
				if n, err := j.Write(p); n != len(p) || err != nil {
					t.Fatal("recorder changed write beyond transcript bound")
				}
			case "events":
				for range maxInteractiveTranscriptWrites + 1 {
					if _, err := c.Write([]byte("x")); err != nil {
						t.Fatal(err)
					}
				}
			}
			j.Close()
			if scenario != "missing EOF" {
				c.Close()
			}
			tr.seal(scenario != "cancelled")
			if tr.ready() || tr.turns != nil {
				t.Fatal("incomplete dialogue retained")
			}
		})
	}
}

type transcriptBlockingSink struct {
	started chan struct{}
	release chan struct{}
}

func (s *transcriptBlockingSink) Write(data []byte) (int, error) {
	close(s.started)
	<-s.release
	return len(data), nil
}

func (*transcriptBlockingSink) Close() error { return nil }

func TestInteractiveTranscriptAmbiguityDoesNotBlockNativePipes(t *testing.T) {
	tr := new(interactiveTranscript)
	j := &transcriptBlockingSink{make(chan struct{}), make(chan struct{})}
	c := &transcriptBlockingSink{make(chan struct{}), make(chan struct{})}
	judge := &interactivePipeWriter{w: j, transcript: tr}
	player := &interactivePipeWriter{w: c, transcript: tr, fromContestant: true}
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = judge.Write([]byte("a")) })
	<-j.started
	wg.Go(func() { _, _ = player.Write([]byte("b")) })
	select {
	case <-c.started:
	case <-time.After(time.Second):
		close(j.release)
		close(c.release)
		wg.Wait()
		t.Fatal("recording serialized opposing native writes")
	}
	close(j.release)
	close(c.release)
	wg.Wait()
	judge.Close()
	player.Close()
	tr.seal(true)
	if tr.ready() || tr.turns != nil {
		t.Fatal("overlapping relay writes produced an ordered transcript")
	}
}

func TestInteractiveTranscriptCancellationUnblocksPlayback(t *testing.T) {
	tr := recordedDialogue(t)
	ctx, cancel := context.WithCancel(t.Context())
	input, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	read, output := io.Pipe()
	defer output.Close()
	done := make(chan bool, 1)
	go func() { done <- tr.play(ctx, send, read) }()
	first := make([]byte, 2)
	if _, err := io.ReadFull(input, first); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled dialogue matched")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled playback did not stop")
	}
}

type waitingTranscriptPipe struct {
	*os.File
	entered chan struct{}
}

func (w *waitingTranscriptPipe) Write(data []byte) (int, error) {
	close(w.entered)
	return w.File.Write(data)
}

func TestInteractiveTranscriptCancellationUnblocksFullInputPipe(t *testing.T) {
	record := new(interactiveTranscript)
	judge := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record}
	player := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record, fromContestant: true}
	_, _ = judge.Write(make([]byte, maxInteractiveTranscriptBytes))
	player.Close()
	judge.Close()
	record.seal(true)
	input, send, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close() // Nobody drains this pipe.
	w := &waitingTranscriptPipe{File: send, entered: make(chan struct{})}
	read, output := io.Pipe()
	defer output.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan bool, 1)
	go func() { done <- record.play(ctx, w, read) }()
	<-w.entered
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled write matched")
		}
	case <-time.After(time.Second):
		t.Fatal("could not stop playback blocked on contestant input")
	}
}

func TestInteractiveTranscriptRejectsEOFBeforeFinalDataPrompt(t *testing.T) {
	record := new(interactiveTranscript)
	judge := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record}
	player := &interactivePipeWriter{w: transcriptSink{io.Discard}, transcript: record, fromContestant: true}
	_, _ = player.Write([]byte("done"))
	_, _ = judge.Write([]byte("ack"))
	player.Close()
	judge.Close()
	record.seal(true)
	// The reader reports the final bytes and EOF together. A real pipe can
	// still accept the judge's final write into its kernel buffer.
	output := io.NopCloser(iotest.DataErrReader(bytes.NewReader([]byte("done"))))
	if record.play(t.Context(), transcriptSink{io.Discard}, output) {
		t.Fatal("matched a contestant that exited before its final prompt")
	}
}
