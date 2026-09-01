package wire

import (
	"net/url"
	"sort"
	"strings"
)

// Host is one of the three base URLs NotebookLM is served from. The values
// are pinned to the allowlist enforced by IsAllowedHost; new hosts are not
// added without a docs update (see doc 03 "Hosts").
type Host string

const (
	// HostPersonal is the default personal NotebookLM host
	// (``notebook.google.com``). The two personal hosts dual-serve
	// ``batchexecute``; this one is the post-rebrand default.
	HostPersonal Host = "https://notebook.google.com"

	// HostPersonalLegacy is the pre-rebrand personal host
	// (``notebooklm.google.com``). Still served, still selectable via
	// NOTEBOOKLM_BASE_URL, and the rollback lever for the default flip.
	HostPersonalLegacy Host = "https://notebooklm.google.com"

	// HostEnterprise is the enterprise-only host
	// (``notebooklm.cloud.google.com``).
	HostEnterprise Host = "https://notebooklm.cloud.google.com"
)

// allowedHosts is the canonical host allowlist, populated from the Host
// constants. IsAllowedHost and the host-validation code paths consult this
// slice, so adding a fourth host is a one-line change here plus a Host
// constant above and a docs update.
var allowedHosts = []Host{HostPersonal, HostPersonalLegacy, HostEnterprise}

// AllowedHosts returns a copy of the host allowlist sorted alphabetically
// by URL. Callers should treat the result as read-only; the canonical
// slice lives in allowedHosts.
func AllowedHosts() []Host {
	out := make([]Host, len(allowedHosts))
	copy(out, allowedHosts)
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}

// IsAllowedHost reports whether rawURL has https scheme, no port, no
// userinfo, no path/query/fragment, AND its host appears in the
// three-host allowlist. The strict check is load-bearing: the value is
// used for authenticated requests, so a credential-bearing request to a
// host that is not Google's is a security regression we will not allow
// silently.
//
// The function is forgiving about trailing slashes (treated as empty
// path) so users can paste “https://notebook.google.com/“ from a
// browser address bar without us rejecting it.
func IsAllowedHost(rawURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil {
		return false
	}
	if parsed.Port() != "" {
		return false
	}
	if strings.TrimRight(parsed.Path, "/") != "" {
		return false
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	host := parsed.Hostname()
	if host == "" {
		return false
	}
	for _, allowed := range allowedHosts {
		if strings.EqualFold(host, urlHostOf(allowed)) {
			return true
		}
	}
	return false
}

// urlHostOf strips the scheme from a Host constant so we can compare
// against parsed.Hostname() (which returns just the host). Defined as a
// tiny helper so the IsAllowedHost loop does not duplicate the trim
// logic on every comparison.
func urlHostOf(h Host) string {
	s := string(h)
	if i := strings.Index(s, "://"); i >= 0 {
		return s[i+3:]
	}
	return s
}

// BatchexecutePath is the URL path for the batchexecute RPC endpoint,
// relative to the host. The leading slash is part of the path.
const BatchexecutePath = "/_/LabsTailwindUi/data/batchexecute"

// StreamedChatPath is the URL path for the streamed-chat RPC. It is NOT
// a batchexecute RPC — its body shape and error mapping differ; this is
// the single sanctioned exception to routing every call through the
// batchexecute executor (see doc 03 "Endpoints").
const StreamedChatPath = "/_/LabsTailwindUi/data/google.internal.labs.tailwind.orchestration.v1.LabsTailwindOrchestrationService/GenerateFreeFormStreamed"

// UploadPath is the URL path for the resumable upload endpoint.
const UploadPath = "/upload/_/"

// BatchexecuteURL builds the batchexecute endpoint URL for the given host.
// A non-allowlisted host yields an empty string; callers must check the
// return value before using the URL on the wire.
func BatchexecuteURL(h Host) string {
	if !hostAllowed(h) {
		return ""
	}
	return string(h) + BatchexecutePath
}

// StreamedChatURL builds the streamed-chat endpoint URL for the given
// host. Same allowlist semantics as BatchexecuteURL.
func StreamedChatURL(h Host) string {
	if !hostAllowed(h) {
		return ""
	}
	return string(h) + StreamedChatPath
}

// UploadURL builds the upload endpoint URL for the given host. Same
// allowlist semantics as BatchexecuteURL.
func UploadURL(h Host) string {
	if !hostAllowed(h) {
		return ""
	}
	return string(h) + UploadPath
}

// hostAllowed is the internal allowlist membership check. Exposed as a
// helper rather than re-deriving from IsAllowedHost (which expects a full
// URL) so the builder functions stay single-purpose.
func hostAllowed(h Host) bool {
	for _, allowed := range allowedHosts {
		if h == allowed {
			return true
		}
	}
	return false
}
