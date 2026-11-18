package execute

import (
	"bytes"
	"context"
	"io"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
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

type interactiveOutputPipe interface {
	io.ReadCloser
	SyscallConn() (syscall.RawConn, error)
}

// idleReplayPipe samples the child's actual stdout pipe. No Go relay can
// hold a later reply outside this queue while playback issues another prompt.
func idleReplayPipe(file interactiveOutputPipe, allowEOF bool) bool {
	raw, err := file.SyscallConn()
	if err != nil {
		return false
	}
	idle := false
	err = raw.Control(func(fd uintptr) {
		poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(poll, 0)
		idle = err == nil && (n == 0 || (allowEOF && poll[0].Revents == unix.POLLHUP))
	})
	return err == nil && idle
}

// A writer may still be blocked inside a single large pipe write after the
// reader has drained one full pipe. Treat that boundary as ambiguous.
func replayPipeCapacity(file interactiveOutputPipe) int {
	raw, err := file.SyscallConn()
	if err != nil {
		return 0
	}
	var capacity int
	if raw.Control(func(fd uintptr) {
		capacity, _ = unix.FcntlInt(fd, unix.F_GETPIPE_SZ, 0)
	}) != nil {
		return 0
	}
	return capacity
}

// play owns both pipe ends. Read only the recorded reply size and leave all
// surplus bytes in the kernel. Check the queue on both sides of each prompt
// write: a fast reply observed during release has ambiguous ordering.
func (t *interactiveTranscript) play(parent context.Context, toContestant io.WriteCloser, fromContestant interactiveOutputPipe, capture io.Writer) bool {
	closeInput := sync.OnceValue(toContestant.Close)
	closePipes := sync.OnceFunc(func() {
		closeInput()
		_ = fromContestant.Close()
	})
	stop := context.AfterFunc(parent, closePipes)
	defer func() {
		stop()
		closePipes()
	}()
	if !t.ready() || parent.Err() != nil {
		return false
	}
	capacity := replayPipeCapacity(fromContestant)
	if capacity <= 0 {
		return false
	}
	reader := io.TeeReader(fromContestant, capture)
	var precedingReply int
	for _, turn := range t.turns {
		if parent.Err() != nil {
			return false
		}
		if !turn.fromContestant {
			if precedingReply >= capacity || !idleReplayPipe(fromContestant, turn.eof) {
				return false
			}
			if turn.eof {
				if closeInput() != nil {
					return false
				}
			} else if n, err := toContestant.Write(turn.data); err != nil || n != len(turn.data) {
				return false
			}
			if !idleReplayPipe(fromContestant, turn.eof) {
				return false
			}
			precedingReply = 0
			continue
		}
		if turn.eof {
			var extra [1]byte
			if n, err := reader.Read(extra[:]); n != 0 || err != io.EOF {
				return false
			}
			continue
		}
		reply := make([]byte, len(turn.data))
		if _, err := io.ReadFull(reader, reply); err != nil || !bytes.Equal(reply, turn.data) {
			return false
		}
		precedingReply += len(reply)
	}
	return parent.Err() == nil
}
