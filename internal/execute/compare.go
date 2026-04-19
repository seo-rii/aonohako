package execute

import "bytes"

type lineSpan struct{ s, e int }

func compareOutputs(expected, actual []byte) bool {
	ee := splitLinesTrim(expected)
	aa := splitLinesTrim(actual)
	ee = dropTrailingBlank(ee)
	aa = dropTrailingBlank(aa)
	if len(ee) != len(aa) {
		return false
	}
	for i := range ee {
		e := expected[ee[i].s:ee[i].e]
		a := actual[aa[i].s:aa[i].e]
		if !bytes.Equal(e, a) {
			return false
		}
	}
	return true
}

func splitLinesTrim(b []byte) []lineSpan {
	var out []lineSpan
	i := 0
	for i <= len(b) {
		j := bytes.IndexByte(b[i:], '\n')
		var end int
		if j < 0 {
			end = len(b)
		} else {
			end = i + j
		}
		t := end
		for t > i && (b[t-1] == ' ' || b[t-1] == '\t' || b[t-1] == '\r') {
			t--
		}
		out = append(out, lineSpan{s: i, e: t})
		if j < 0 {
			break
		}
		i = end + 1
	}
	return out
}

func dropTrailingBlank(spans []lineSpan) []lineSpan {
	i := len(spans) - 1
	for i >= 0 {
		if spans[i].e-spans[i].s == 0 {
			i--
			continue
		}
		break
	}
	return spans[:i+1]
}
