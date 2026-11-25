package core

import (
	"regexp"
	"strings"
)

var openCodeV2Release = regexp.MustCompile(`^2\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(\+[0-9A-Za-z.-]+)?$`)

// This is only a protocol-family fence. Each caller must verify the exact
// response and capability it uses; it is not a permission semantics check.
func isOpenCodeV2(version string) bool { return openCodeV2Release.MatchString(version) }

func isOpenCodeV2CLI(output string) bool {
	version, ok := strings.CutPrefix(strings.TrimSpace(output), "opencode v")
	return ok && isOpenCodeV2(version)
}
