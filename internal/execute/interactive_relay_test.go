package execute

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"testing"

	"aonohako/internal/model"
)

func TestExpectedPostExitStdinRelayError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"pipe closed by cleanup", io.ErrClosedPipe, true},
		{"fd closed by cleanup", os.ErrClosed, true},
		{"child stdin pipe closed", syscall.EPIPE, true},
		{"wrapped closed pipe", fmt.Errorf("copy stopped: %w", io.ErrClosedPipe), true},
		{"unrelated I/O error", io.ErrUnexpectedEOF, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExpectedPostExitStdinRelayError(tc.err); got != tc.want {
				t.Fatalf("post-exit stdin error %v expected=%t, got=%t", tc.err, tc.want, got)
			}
		})
	}
}

func TestReconcileInteractiveRelayResults(t *testing.T) {
	ok, wrong := 0, 1
	closedRelay := func(code *int, err error) execResult {
		return execResult{
			Status:         model.RunStatusRE,
			ExitCode:       code,
			Reason:         "sandbox stream relay failed: " + err.Error(),
			VerdictSource:  "stream_io",
			streamRelayErr: err,
		}
	}
	cases := []struct {
		name                  string
		contestant            execResult
		interactor            execResult
		toInteractorErr       error
		toContestantErr       error
		wantContestant        string
		wantInteractor        string
		wantInteractorVerdict string
	}{
		{
			name:                  "accept final reply to exited contestant",
			contestant:            execResult{Status: "OK", ExitCode: &ok},
			interactor:            closedRelay(&ok, io.ErrClosedPipe),
			toContestantErr:       io.ErrClosedPipe,
			wantContestant:        "OK",
			wantInteractor:        "OK",
			wantInteractorVerdict: model.RunStatusAccepted,
		},
		{
			name:                  "native pipe EPIPE after final reply",
			contestant:            execResult{Status: "OK", ExitCode: &ok},
			interactor:            closedRelay(&ok, syscall.EPIPE),
			toContestantErr:       syscall.EPIPE,
			wantContestant:        "OK",
			wantInteractor:        "OK",
			wantInteractorVerdict: model.RunStatusAccepted,
		},
		{
			name:                  "wrapped closed pipe",
			contestant:            execResult{Status: "OK", ExitCode: &ok},
			interactor:            closedRelay(&ok, fmt.Errorf("copy failed: %w", io.ErrClosedPipe)),
			toContestantErr:       fmt.Errorf("peer exited: %w", io.ErrClosedPipe),
			wantContestant:        "OK",
			wantInteractor:        "OK",
			wantInteractorVerdict: model.RunStatusAccepted,
		},
		{
			name:                  "wrong answer remains WA after final reply",
			contestant:            execResult{Status: "OK", ExitCode: &ok},
			interactor:            execResult{Status: "OK", ExitCode: &wrong},
			toContestantErr:       io.ErrClosedPipe,
			wantContestant:        "OK",
			wantInteractor:        "OK",
			wantInteractorVerdict: model.RunStatusWA,
		},
		{
			name:                  "contestant stdout closed after interactor WA",
			contestant:            closedRelay(&ok, io.ErrClosedPipe),
			interactor:            execResult{Status: "OK", ExitCode: &wrong},
			toInteractorErr:       io.ErrClosedPipe,
			wantContestant:        "OK",
			wantInteractor:        "OK",
			wantInteractorVerdict: model.RunStatusWA,
		},
		{
			name:           "contestant nonzero exit is not masked",
			contestant:     execResult{Status: "OK", ExitCode: &wrong},
			interactor:     closedRelay(&ok, io.ErrClosedPipe),
			toContestantErr: io.ErrClosedPipe,
			wantContestant: "OK",
			wantInteractor: model.RunStatusRE,
		},
		{
			name:           "unknown interactor exit is not masked",
			contestant:     execResult{Status: "OK", ExitCode: &ok},
			interactor:     closedRelay(nil, io.ErrClosedPipe),
			toContestantErr: io.ErrClosedPipe,
			wantContestant: "OK",
			wantInteractor: model.RunStatusRE,
		},
		{
			name:           "other command stdout copy errors remain RE",
			contestant:     execResult{Status: "OK", ExitCode: &ok},
			interactor:     closedRelay(&ok, io.ErrUnexpectedEOF),
			toContestantErr: io.ErrClosedPipe,
			wantContestant: "OK",
			wantInteractor: model.RunStatusRE,
		},
		{
			name:           "other pipe errors remain RE",
			contestant:     execResult{Status: "OK", ExitCode: &ok},
			interactor:     execResult{Status: "OK", ExitCode: &ok},
			toContestantErr: io.ErrUnexpectedEOF,
			wantContestant: "OK",
			wantInteractor: model.RunStatusRE,
		},
		{
			name:           "stdin errors are not discounted",
			contestant:     execResult{Status: "OK", ExitCode: &ok},
			interactor:     execResult{Status: model.RunStatusRE, ExitCode: &ok, Reason: "sandbox stdin relay failed: io: read/write on closed pipe", VerdictSource: "stream_io"},
			toContestantErr: io.ErrClosedPipe,
			wantContestant: "OK",
			wantInteractor: model.RunStatusRE,
		},
		{
			name:           "timeout is not hidden",
			contestant:     execResult{Status: model.RunStatusTLE},
			interactor:     closedRelay(&ok, io.ErrClosedPipe),
			toContestantErr: io.ErrClosedPipe,
			wantContestant: model.RunStatusTLE,
			wantInteractor: model.RunStatusRE,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			contestant, interactor := tc.contestant, tc.interactor
			reconcileInteractiveRelayResults(&contestant, &interactor, tc.toInteractorErr, tc.toContestantErr)
			if contestant.Status != tc.wantContestant || interactor.Status != tc.wantInteractor {
				t.Fatalf("statuses = (%q, %q), want (%q, %q); contestant=%+v interactor=%+v",
					contestant.Status, interactor.Status, tc.wantContestant, tc.wantInteractor, contestant, interactor)
			}
			if tc.wantInteractorVerdict != "" {
				status, _, _ := classifyInteractorStatus(&model.RunRequest{}, interactor)
				if status != tc.wantInteractorVerdict {
					t.Fatalf("interactor verdict = %q, want %q", status, tc.wantInteractorVerdict)
				}
			}
		})
	}
}

// A terminal correct/wrong reply is optional for the contestant to read after
// emitting its final answer. When the contestant has already exited, writing
// the reply must not turn the interactor's exit-code verdict into a relay RE.
func TestRunInteractiveIOFinalReplyAfterContestantExit(t *testing.T) {
	requireSandboxSupport(t)
	contestant := "import sys\n" +
		"if sys.stdin.readline().strip() != 'ready':\n" +
		"    raise SystemExit(2)\n" +
		"print('! 0', flush=True)\n"
	interactorPrefix := "import sys\nimport time\n" +
		"print('ready', flush=True)\n" +
		"if sys.stdin.readline().strip() != '! 0':\n" +
		"    raise SystemExit(3)\n" +
		"if sys.stdin.read():\n" +
		"    raise SystemExit(3)\n" +
		"time.sleep(0.1)\n"

	for _, tc := range []struct {
		name       string
		reply      string
		exitCode   int
		wantStatus string
		wantScore  float64
	}{
		{"correct", "correct", 0, model.RunStatusAccepted, 1},
		{"wrong", "wrong", 1, model.RunStatusWA, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			interactor := interactorPrefix + fmt.Sprintf("print('%s', flush=True)\nraise SystemExit(%d)\n", tc.reply, tc.exitCode)
			resp := New().Run(context.Background(), &model.RunRequest{
				Lang: "python",
				Binaries: []model.Binary{{
					Name:    "main.py",
					DataB64: base64.StdEncoding.EncodeToString([]byte(contestant)),
				}},
				Interactor: &model.InteractorSpec{
					Lang: "python",
					Binaries: []model.Binary{{
						Name:    "interactor.py",
						DataB64: base64.StdEncoding.EncodeToString([]byte(interactor)),
					}},
				},
				Limits: model.Limits{TimeMs: 3000, MemoryMB: 128},
			}, Hooks{})
			if resp.Status != tc.wantStatus {
				t.Fatalf("final reply to exited contestant = %q, want %q: %+v", resp.Status, tc.wantStatus, resp)
			}
			if resp.Score == nil || *resp.Score != tc.wantScore {
				t.Fatalf("interactor score = %+v, want %v", resp.Score, tc.wantScore)
			}
		})
	}
}

// Kernel-pipe relays must propagate bytes and EOF without a Go io.Pipe copy.
func TestInteractiveOSPipeWriter(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	writer := &interactivePipeWriter{w: writeEnd}
	const message = "? 123 7\n"
	if _, err := writer.Write([]byte(message)); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(message))
	if _, err := io.ReadFull(readEnd, got); err != nil || string(got) != message {
		t.Fatalf("read = %q, err = %v", got, err)
	}
	writer.Close()
	if rest, err := io.ReadAll(readEnd); err != nil || len(rest) != 0 {
		t.Fatalf("after close = %q, err = %v", rest, err)
	}
	if writer.Err() != nil {
		t.Fatalf("unexpected close error: %v", writer.Err())
	}
}

func TestInteractiveOSPipePeerClose(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	writer := &interactivePipeWriter{w: writeEnd}
	defer writer.Close()
	if err := readEnd.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = writer.Write([]byte("correct\n"))
	if !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("expected EPIPE from exited peer, got %v", err)
	}
	if !errors.Is(writer.Err(), syscall.EPIPE) {
		t.Fatalf("expected recorded EPIPE, got %v", writer.Err())
	}
}
