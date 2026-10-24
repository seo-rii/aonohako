package execute

import "aonohako/internal/model"

// applyStdoutLimitVerdict finalizes execution-output overflow after resource and
// exit-code classification. An existing failure keeps its status, reason, and
// source; only successful execution becomes an output-limit runtime error.
// exceeded must describe the execution buffer, never a display capture limit.
func applyStdoutLimitVerdict(status, reason, source string, exceeded bool) (string, string, string) {
	if exceeded && (status == "OK" || status == model.RunStatusAccepted) {
		return model.RunStatusRE, "stdout exceeded output limit", "stdout_limit"
	}
	return status, reason, source
}
