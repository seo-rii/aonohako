package remoteio

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	DefaultSSELineBytes   = 256 << 10
	DefaultSSEEventBytes  = 4 << 20
	DefaultSSEStreamBytes = 64 << 20
	DefaultSSEIdleTimeout = 30 * time.Second
)

type SSEEvent struct {
	Name string
	Data string
}

type SSEReader struct {
	reader         *bufio.Reader
	maxLineBytes   int
	maxEventBytes  int
	maxStreamBytes int
	bytesRead      int
	onActivity     func()
}

func NewSSEReader(r io.Reader) *SSEReader {
	return &SSEReader{
		reader:         bufio.NewReader(r),
		maxLineBytes:   DefaultSSELineBytes,
		maxEventBytes:  DefaultSSEEventBytes,
		maxStreamBytes: DefaultSSEStreamBytes,
	}
}

func (r *SSEReader) SetActivityCallback(fn func()) {
	r.onActivity = fn
}

func (r *SSEReader) Next() (SSEEvent, error) {
	eventName := ""
	var data strings.Builder
	for {
		line, err := r.readLine()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if eventName == "" {
					return SSEEvent{}, io.EOF
				}
				return SSEEvent{Name: eventName, Data: data.String()}, nil
			}
			return SSEEvent{}, err
		}
		if line == "" {
			if eventName == "" {
				data.Reset()
				continue
			}
			return SSEEvent{Name: eventName, Data: data.String()}, nil
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			value := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			nextLen := data.Len() + len(value)
			if data.Len() > 0 {
				nextLen++
			}
			if nextLen > r.maxEventBytes {
				return SSEEvent{}, fmt.Errorf("sse event too large: max %d bytes", r.maxEventBytes)
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
	}
}

func (r *SSEReader) readLine() (string, error) {
	var buf []byte
	for {
		part, err := r.reader.ReadSlice('\n')
		if len(part) > 0 {
			if r.onActivity != nil {
				r.onActivity()
			}
			r.bytesRead += len(part)
			if r.bytesRead > r.maxStreamBytes {
				return "", fmt.Errorf("sse stream too large: max %d bytes", r.maxStreamBytes)
			}
			buf = append(buf, part...)
			if len(buf) > r.maxLineBytes {
				return "", fmt.Errorf("sse line too large: max %d bytes", r.maxLineBytes)
			}
		}
		switch {
		case err == nil:
			return strings.TrimRight(string(buf), "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return "", io.EOF
			}
			return strings.TrimRight(string(buf), "\r\n"), nil
		default:
			return "", err
		}
	}
}
