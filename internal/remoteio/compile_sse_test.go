package remoteio

import (
	"io"
	"strings"
	"testing"

	"aonohako/internal/compilewire"
)

// Repeat a short wire fragment without retaining a complete hostile stream.
type compileRepeatedReader struct {
	value  string
	offset int
	left   int
}

func (r *compileRepeatedReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.value[r.offset:])
	r.offset += n
	if r.offset == len(r.value) {
		r.offset = 0
		r.left--
	}
	return n, nil
}

func TestCompileSSEReaderRejectsAggregateEventAndStreamOverflow(t *testing.T) {
	for _, tc := range []struct {
		name, fragment, want string
		count                int
	}{
		{"multiline event", "data: " + strings.Repeat("x", 1<<20) + "\n", "sse event too large", compilewire.SSEEventBytes/(1<<20) + 1},
		{"comment stream", ": " + strings.Repeat("x", 1<<20) + "\n", "sse stream too large", compilewire.SSEStreamBytes/(1<<20) + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := io.MultiReader(strings.NewReader("event: result\n"), &compileRepeatedReader{value: tc.fragment, left: tc.count})
			_, err := NewCompileSSEReader(input).Next()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Next error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestCompileSSELimitsDoNotWidenExecute(t *testing.T) {
	reader := NewSSEReader(strings.NewReader(""))
	if reader.maxLineBytes != 64<<20 || reader.maxEventBytes != 64<<20 || reader.maxStreamBytes != 128<<20 {
		t.Fatalf("execute reader limits changed: %+v", reader)
	}
}
