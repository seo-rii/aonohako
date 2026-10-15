package execute

import "bytes"

func compareOutputs(expected, actual []byte) bool {
	// Remove trailing blank lines and whitespace on the last content line before
	// walking the remaining lines. All slices borrow the inputs, so even output
	// consisting of millions of newlines needs only constant auxiliary memory.
	expected = bytes.TrimRight(expected, " \t\r\n")
	actual = bytes.TrimRight(actual, " \t\r\n")
	for len(expected) > 0 && len(actual) > 0 {
		var expectedLine, actualLine []byte
		expectedLine, expected, _ = bytes.Cut(expected, []byte{'\n'})
		actualLine, actual, _ = bytes.Cut(actual, []byte{'\n'})
		expectedLine = bytes.TrimRight(expectedLine, " \t\r")
		actualLine = bytes.TrimRight(actualLine, " \t\r")
		if !bytes.Equal(expectedLine, actualLine) {
			return false
		}
	}
	return len(expected) == 0 && len(actual) == 0
}
