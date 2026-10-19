package sse

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"aonohako/internal/model"
)

// Marshal a copy of the presentation fields. Grading, handoffs and sidecars
// retain their original bytes; only display text may be shortened for the wire.
func marshalRunResult(response model.RunResponse, limit int) ([]byte, error) {
	response.Steps = append([]model.StepResult(nil), response.Steps...)
	type field struct {
		key       string
		value     *string
		truncated *bool
		original  string
		size      int
	}
	fields := []field{
		{key: "stdout", value: &response.Stdout, truncated: &response.StdoutTruncated},
		{key: "stderr", value: &response.Stderr, truncated: &response.StderrTruncated},
		{key: "reason", value: &response.Reason, truncated: &response.ReasonTruncated},
	}
	for i := range response.Steps {
		step := &response.Steps[i]
		fields = append(fields,
			field{key: "stdout", value: &step.Stdout, truncated: &step.StdoutTruncated},
			field{key: "stderr", value: &step.Stderr, truncated: &step.StderrTruncated},
			field{key: "reason", value: &step.Reason, truncated: &step.ReasonTruncated})
	}
	// Reserve truncation flags and omitted string keys before assigning space.
	// Avoid first allocating a JSON value that might exceed the wire limit.
	base := response
	base.Steps = append([]model.StepResult(nil), response.Steps...)
	base.Stdout, base.Stderr, base.Reason = "", "", ""
	var keyBytes, totalBytes, largest int
	for i := range fields {
		f := &fields[i]
		f.original = *f.value
		f.size = jsonStringBytes(f.original)
		totalBytes += f.size
		largest = max(largest, f.size)
		if f.original != "" {
			keyBytes += len(f.key) + len(`,"":""`)
		}
	}
	base.StdoutTruncated = base.StdoutTruncated || response.Stdout != ""
	base.StderrTruncated = base.StderrTruncated || response.Stderr != ""
	base.ReasonTruncated = base.ReasonTruncated || response.Reason != ""
	for i := range base.Steps {
		step := &base.Steps[i]
		step.StdoutTruncated = step.StdoutTruncated || step.Stdout != ""
		step.StderrTruncated = step.StderrTruncated || step.Stderr != ""
		step.ReasonTruncated = step.ReasonTruncated || step.Reason != ""
		step.Stdout, step.Stderr, step.Reason = "", "", ""
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		return nil, err
	}
	fullBytes := len(encoded) + keyBytes + totalBytes
	for _, f := range fields {
		if f.original != "" && !*f.truncated {
			fullBytes -= len(f.key) + len(`,"_truncated":true`)
		}
	}
	if fullBytes <= limit {
		return json.Marshal(response)
	}
	available := limit - len(encoded) - keyBytes
	if available < 0 {
		return nil, fmt.Errorf("result metadata and artifacts exceed SSE wire budget: max %d bytes", limit)
	}
	if totalBytes > available {
		// Share space among fields, redistributing the unused share of short
		// fields so small diagnostics do not waste capture capacity.
		low, high := 0, largest
		for low < high {
			mid := low + (high-low+1)/2
			n := 0
			for _, f := range fields {
				n += min(f.size, mid)
			}
			if n <= available {
				low = mid
			} else {
				high = mid - 1
			}
		}
		for _, f := range fields {
			*f.value = clipJSONString(f.original, low)
			*f.truncated = *f.truncated || len(*f.value) < len(f.original)
		}
	}
	encoded, err = json.Marshal(response)
	if err == nil && len(encoded) > limit {
		return nil, fmt.Errorf("result exceeds SSE wire budget: max %d bytes", limit)
	}
	return encoded, err
}

func jsonStringBytes(value string) int {
	n := 0
	for len(value) > 0 {
		width, cost := jsonRuneBytes(value)
		n += cost
		value = value[width:]
	}
	return n
}

func clipJSONString(value string, limit int) string {
	end, used := 0, 0
	for end < len(value) {
		width, cost := jsonRuneBytes(value[end:])
		if cost > limit-used {
			break
		}
		end += width
		used += cost
	}
	return value[:end]
}

// Match encoding/json's default HTML escaping, control escapes, replacement of
// invalid UTF-8 and JavaScript line separators. Quotes are budgeted separately.
func jsonRuneBytes(value string) (width, cost int) {
	c := value[0]
	if c < utf8.RuneSelf {
		switch c {
		case '\\', '"', '\n', '\r', '\t', '\b', '\f':
			return 1, 2
		case '<', '>', '&':
			return 1, 6
		default:
			if c < 0x20 {
				return 1, 6
			}
			return 1, 1
		}
	}
	r, size := utf8.DecodeRuneInString(value)
	if (r == utf8.RuneError && size == 1) || r == '\u2028' || r == '\u2029' {
		return size, 6
	}
	return size, size
}
