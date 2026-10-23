package execute

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"testing"

	"aonohako/internal/model"
)

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
// the reply must not turn the interactor's successful exit into a stream_io RE.
func TestRunInteractiveIOAcceptsFinalReplyAfterContestantExit(t *testing.T) {
	requireSandboxSupport(t)
	contestant := "import sys\n" +
		"if sys.stdin.readline().strip() != 'ready':\n" +
		"    raise SystemExit(2)\n" +
		"print('! 0', flush=True)\n"
	interactor := "import sys\nimport time\n" +
		"print('ready', flush=True)\n" +
		"if sys.stdin.readline().strip() != '! 0':\n" +
		"    raise SystemExit(1)\n" +
		"if sys.stdin.read():\n" +
		"    raise SystemExit(1)\n" +
		"time.sleep(0.1)\n" +
		"print('correct', flush=True)\n"

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
	if resp.Status != model.RunStatusAccepted {
		t.Fatalf("final reply to exited contestant must not cause RE: %+v", resp)
	}
	if resp.Score == nil || *resp.Score != 1 {
		t.Fatalf("expected interactor score 1, got %+v", resp.Score)
	}
}
