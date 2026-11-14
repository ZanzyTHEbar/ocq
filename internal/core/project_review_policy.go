package core

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/ZanzyTHEbar/faults-go"
)

// OpenCode v2.2.6 and v2.3.0 evaluate agent rules before session rules, check the
// last matching configured rule, and do not let saved approvals override a deny.
// v2.3.0 source: anomalyco/opencode@a6b15d33daf6ab7d35569f6d711fdb630b9f04e7,
// packages/core/src/{permission.ts,session/context.ts,tool.ts}. The permission
// evaluator is unchanged from v2.2.6; the catch-all session deny also disables
// Code Mode, leaving only the explicitly allowed read/glob/grep tools.
// Other versions require source validation before they can use this gate.
func verifyReviewerRuntime(directory, agent string, rules []permissionRule) error {
	if err := validateReviewerPermissions(rules); err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || agent == "" || strings.ContainsAny(agent, "/?#\\ \t\r\n\x00") {
		return diagnostic(faults.CodeValidationFailed, "review policy requires an absolute location and an exact agent ID")
	}
	b, err := apiCall(directory, "get", "/api/info", nil)
	if err != nil {
		return diagnosticWrap(faults.CodeRuntimeFailed, "review policy server inspection", err)
	}
	var server struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(b, &server); err != nil || (server.Version != "2.2.6" && server.Version != "2.3.0") {
		return diagnostic(faults.CodeConfigInvalid, "review policy requires source-validated OpenCode server version 2.2.6 or 2.3.0")
	}
	query := url.Values{"location[directory]": {directory}}
	b, err = apiCall(directory, "get", "/api/agent/"+url.PathEscape(agent)+"?"+query.Encode(), nil)
	if err != nil {
		return diagnosticWrap(faults.CodeRuntimeFailed, "review policy agent inspection", err)
	}
	var response struct {
		Location struct {
			Directory string `json:"directory"`
		} `json:"location"`
		Data struct {
			ID          string           `json:"id"`
			Permissions []permissionRule `json:"permissions"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &response); err != nil || response.Location.Directory != directory || response.Data.ID != agent {
		return diagnostic(faults.CodeRuntimeFailed, "review policy agent or location response mismatch")
	}
	if err := validatePermissionRules(response.Data.Permissions); err != nil {
		return diagnosticWrap(faults.CodeRuntimeFailed, "review policy agent rules unavailable", err)
	}
	return nil
}
