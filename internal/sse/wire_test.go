package sse

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"aonohako/internal/model"
	"aonohako/internal/remoteio"
)

func TestResultFitsWireBudgetWithEscapedStreamsAndSidecars(t *testing.T) {
	w := newDeadlineResponseWriter()
	stream, err := New(w, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	output := strings.Repeat("\x00", 6<<20)
	artifact := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xff}, 8<<20))
	response := model.RunResponse{
		Status: model.RunStatusRE, Stdout: output, Stderr: output, Reason: output,
		SidecarOutputs: []model.SidecarOutput{{Path: "artifact.bin", DataB64: artifact}},
	}
	// Both legacy steps can emit their full captures before the final result.
	// Their JSON escapes must also fit the whole-stream budget.
	for i := 0; i < 10; i++ {
		if err := stream.Event("log", map[string]any{"stream": "stdout", "chunk": output[:1<<20]}); err != nil {
			t.Fatal(err)
		}
	}
	wantImages := 0
	for offset := 0; offset < len(artifact); offset += 1 << 20 {
		chunk := artifact[offset:min(len(artifact), offset+(1<<20))]
		if err := stream.Event("image", map[string]any{"mime": "image/png", "b64": chunk}); err != nil {
			t.Fatal(err)
		}
		wantImages++
	}
	if err := stream.Event("result", response); err != nil {
		t.Fatal(err)
	}
	if w.body.Len() > remoteio.DefaultSSEStreamBytes {
		t.Fatalf("wire stream = %d, max %d", w.body.Len(), remoteio.DefaultSSEStreamBytes)
	}
	reader := remoteio.NewSSEReader(bytes.NewReader(w.body.Bytes()))
	var event remoteio.SSEEvent
	var logs, images int
	for {
		event, err = reader.Next()
		if err != nil {
			t.Fatalf("allowed result failed remote SSE transport: %v", err)
		}
		if event.Name == "result" {
			break
		}
		if event.Name == "log" {
			logs++
		}
		if event.Name == "image" {
			images++
		}
	}
	if logs == 0 || logs >= 10 || images != wantImages {
		t.Fatalf("streamed logs/images = %d/%d, want bounded logs and retained image", logs, images)
	}
	var got model.RunResponse
	if err := json.Unmarshal([]byte(event.Data), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != response.Status || !got.StdoutTruncated || !got.StderrTruncated || !got.ReasonTruncated {
		t.Fatalf("verdict/truncation = %q/%v/%v", got.Status, got.StdoutTruncated, got.StderrTruncated)
	}
	if !strings.HasPrefix(output, got.Stdout) || !strings.HasPrefix(output, got.Stderr) {
		t.Fatal("display clipping changed the output prefix")
	}
	if len(got.SidecarOutputs) != 1 || got.SidecarOutputs[0].DataB64 != artifact {
		t.Fatal("wire clipping changed the binary artifact")
	}
	if response.Stdout != output || response.Stderr != output || response.StdoutTruncated || response.StderrTruncated {
		t.Fatal("wire clipping mutated the runner's response")
	}
}

func TestRunResultWireBoundaries(t *testing.T) {
	for _, text := range []string{"ASCII", "\x00\x01\n\r\t\b\f", "\"\\", "<>&", "한글🙂\u2028\u2029"} {
		t.Run(text, func(t *testing.T) {
			output := strings.Repeat(text, 100)
			original := model.RunResponse{
				Status: model.RunStatusTLE, Stdout: output, Stderr: output, Reason: output,
				Steps:          []model.StepResult{{ID: "first", Status: model.RunStatusAccepted, Stdout: output, Stderr: "short", StderrTruncated: true, Reason: output}},
				SidecarOutputs: []model.SidecarOutput{{Path: "x", DataB64: "AAE="}},
			}
			full, err := json.Marshal(original)
			if err != nil {
				t.Fatal(err)
			}
			for _, limit := range []int{512, len(full) - 1, len(full), len(full) + 1} {
				payload, err := marshalRunResult(original, limit)
				if err != nil {
					t.Fatalf("limit %d: %v", limit, err)
				}
				if len(payload) > limit {
					t.Fatalf("wire bytes %d > limit %d", len(payload), limit)
				}
				if limit >= len(full) && !bytes.Equal(payload, full) {
					t.Fatal("result within wire budget was changed")
				}
				var got model.RunResponse
				if err := json.Unmarshal(payload, &got); err != nil {
					t.Fatal(err)
				}
				if got.Status != original.Status || got.SidecarOutputs[0] != original.SidecarOutputs[0] || !got.Steps[0].StderrTruncated {
					t.Fatal("wire clipping changed verdict, artifact or existing truncation")
				}
				for _, value := range []string{got.Stdout, got.Stderr, got.Reason, got.Steps[0].Stdout, got.Steps[0].Reason} {
					if !utf8.ValidString(value) || !strings.HasPrefix(output, value) {
						t.Fatal("wire clipping split or changed a UTF-8 character")
					}
				}
				if (got.Stdout != original.Stdout) != got.StdoutTruncated || (got.Stderr != original.Stderr) != got.StderrTruncated || (got.Reason != original.Reason) != got.ReasonTruncated || (got.Steps[0].Reason != original.Steps[0].Reason) != got.Steps[0].ReasonTruncated {
					t.Fatal("wire truncation flags do not describe display clipping")
				}
			}
			if original.Steps[0].Stdout != output || original.Steps[0].StdoutTruncated {
				t.Fatal("wire clipping mutated shared step results")
			}
		})
	}
}

func TestJSONStringWireCostMatchesEncodingJSON(t *testing.T) {
	value := string([]byte{0xff, 0xe9, 0xc0}) + "한글🙂\u2028\u2029"
	for i := 0; i < 128; i++ {
		value += string(rune(i))
	}
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonStringBytes(value); got != len(payload)-2 {
		t.Fatalf("wire cost = %d, actual JSON string bytes = %d", got, len(payload)-2)
	}
	for limit := 0; limit < len(payload); limit++ {
		clipped := clipJSONString(value, limit)
		encoded, _ := json.Marshal(clipped)
		if len(encoded)-2 > limit || !strings.HasPrefix(value, clipped) {
			t.Fatalf("invalid wire prefix at budget %d", limit)
		}
	}
}
