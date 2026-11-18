package execute

import (
	"bytes"
	"context"
	"io"
	"sync"
	"sync/atomic"
)

type interactiveTranscriptKey struct{}

const (
	maxInteractiveTranscriptBytes  = 1 << 20
	maxInteractiveTranscriptWrites = 4096
)

type interactiveTurn struct {
	fromContestant bool
	data           []byte
	eof            bool
}

// Both native relay goroutines share this request-private recorder. Its lock
// protects only bookkeeping, never a blocking read, write or close on a pipe.
type interactiveTranscript struct {
	mu          sync.Mutex
	turns       []interactiveTurn
	bytes       int
	writes      int
	inFlight    int
	writingData bool
	failed      bool
	complete    bool
}

func (t *interactiveTranscript) begin(eof bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed {
		return
	}
	// Separate EOFs may coincide at clean shutdown. Overlapping data and
	// any other transfer have no provable order at this observation point.
	if t.inFlight != 0 && (t.writingData || !eof) {
		t.failed, t.turns = true, nil
		return
	}
	t.inFlight++
	t.writingData = !eof
}

func (t *interactiveTranscript) end(fromContestant bool, data []byte, eof, delivered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed {
		return
	}
	t.inFlight--
	t.writingData = false
	if !delivered || len(data) > maxInteractiveTranscriptBytes-t.bytes ||
		(!eof && len(data) > 0 && t.writes == maxInteractiveTranscriptWrites) {
		t.failed, t.turns = true, nil
		return
	}
	if !eof {
		if len(data) == 0 {
			return
		}
		t.writes++
		t.bytes += len(data)
		if n := len(t.turns); n > 0 && !t.turns[n-1].eof && t.turns[n-1].fromContestant == fromContestant {
			t.turns[n-1].data = append(t.turns[n-1].data, data...)
			return
		}
	}
	t.turns = append(t.turns, interactiveTurn{fromContestant: fromContestant, data: bytes.Clone(data), eof: eof})
}

func (t *interactiveTranscript) seal(normal bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var contestantEOF, interactorEOF bool
	for _, turn := range t.turns {
		if turn.eof {
			if turn.fromContestant {
				contestantEOF = true
			} else {
				interactorEOF = true
			}
		}
	}
	t.complete = normal && !t.failed && t.inFlight == 0 && contestantEOF && interactorEOF
	if !t.complete {
		t.failed, t.turns = true, nil
	}
}

func (t *interactiveTranscript) ready() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.complete
}

type interactiveReply struct {
	data   []byte
	prompt int64
	err    error
}

// play owns both pipe ends. Closing them must interrupt blocked I/O. The
// sandbox's stdout writer feeds fromContestant and closes it at target exit.
func (t *interactiveTranscript) play(parent context.Context, toContestant io.WriteCloser, fromContestant io.ReadCloser) bool {
	if !t.ready() || parent.Err() != nil {
		_ = toContestant.Close()
		_ = fromContestant.Close()
		return false
	}
	ctx, cancel := context.WithCancel(parent)
	closeInput := sync.OnceValue(toContestant.Close)
	closePipes := sync.OnceFunc(func() {
		closeInput()
		_ = fromContestant.Close()
	})
	stopClosing := context.AfterFunc(ctx, closePipes)

	// Read continuously: a single write that crosses an unreleased prompt
	// must not be split and silently credited to two separate replies.
	var issuedPrompt atomic.Int64
	replies := make(chan interactiveReply)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		var buf [32 << 10]byte
		for {
			n, err := fromContestant.Read(buf[:])
			prompt := issuedPrompt.Load()
			if n > 0 {
				select {
				case replies <- interactiveReply{data: bytes.Clone(buf[:n]), prompt: prompt}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				select {
				case replies <- interactiveReply{prompt: prompt, err: err}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()
	defer func() {
		stopClosing()
		cancel()
		closePipes()
		<-readerDone
	}()

	var prompt, lastDataPrompt int64
	for _, turn := range t.turns {
		if ctx.Err() != nil {
			return false
		}
		if !turn.fromContestant {
			prompt++
			issuedPrompt.Store(prompt)
			if turn.eof {
				if closeInput() != nil {
					return false
				}
			} else {
				lastDataPrompt = prompt
				if n, err := toContestant.Write(turn.data); err != nil || n != len(turn.data) {
					return false
				}
			}
			continue
		}
		expected := turn.data
		for len(expected) != 0 || turn.eof {
			var reply interactiveReply
			select {
			case <-ctx.Done():
				return false
			case reply = <-replies:
			}
			if turn.eof {
				// The two final EOFs may race at clean shutdown. A contestant
				// which exited before the final data prompt cannot match it.
				if reply.err != io.EOF || reply.prompt < lastDataPrompt {
					return false
				}
				break
			}
			if reply.err != nil || reply.prompt < prompt || !bytes.HasPrefix(expected, reply.data) {
				return false
			}
			expected = expected[len(reply.data):]
		}
	}
	return ctx.Err() == nil
}
