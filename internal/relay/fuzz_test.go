package relay

import (
	"net/url"
	"strings"
	"testing"
)

// Property: whatever path a client sends, a successful buildTargetURL never leaves the
// target's scheme+host, never gains userinfo, and never contains control characters.
func FuzzBuildTargetURL(f *testing.F) {
	base, _ := url.Parse("https://api.example.com")
	for _, s := range []string{"/get", "", "@evil.com/x", "//evil.com", "/..;/x", `/\evil.com`, "/%2e%2e/x",
		":80/x", "/a?b=c#d", "/\r\nHost: evil", "https://evil.com", "/%00", "/a b", "///evil.com", "/@evil.com", "\t/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		got, err := buildTargetURL(base, path)
		if err != nil {
			return
		}
		u, perr := url.Parse(got)
		if perr != nil {
			t.Fatalf("result %q does not parse: %v", got, perr)
		}
		if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil {
			t.Fatalf("path %q escaped the target: %q (scheme=%s host=%s user=%v)", path, got, u.Scheme, u.Host, u.User)
		}
		if strings.ContainsAny(got, "\r\n\x00") {
			t.Fatalf("path %q produced control characters: %q", path, got)
		}
	})
}

// canonical is deliberately independent of foldHeaderName: it models how CGI/WSGI-style
// backends fold header names ("X_Real_IP" == "X-Real-IP"), so the test isn't self-referential.
func canonical(name string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(name)), "_", "-")
}

// Property: no header name a backend could read as a denied header may pass the filter.
func FuzzHeaderFilter(f *testing.F) {
	for _, s := range []string{"Authorization", "AUTHORIZATION", "X_Forwarded_For", "x_real_ip", "Host ", " Cookie",
		"Transfer_Encoding", "Content-Type", "X-Custom", "Proxy_Authorization", "Keep_Alive"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		_, denied := deniedRequestHeaders[canonical(name)]
		if denied && isHeaderAllowed(name, false) {
			t.Fatalf("isHeaderAllowed(%q) = true but it folds to a denied header %q", name, canonical(name))
		}
		out := StripSensitiveHeaders(map[string]string{name: "v"})
		if _, kept := out[name]; kept && denied {
			t.Fatalf("StripSensitiveHeaders kept %q which folds to a denied header %q", name, canonical(name))
		}
	})
}
