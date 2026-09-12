package cdp

import (
	"context"
	"errors"
	"net/netip"
	"testing"
)

type stubResolver map[string][]string

func (s stubResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addrs, ok := s[host]
	if !ok {
		return nil, errors.New("no such host")
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, netip.MustParseAddr(a))
	}
	return out, nil
}

func testGuard() *Guard {
	return NewGuard(stubResolver{
		"example.com":         {"93.184.216.34"},
		"rebind.example":      {"127.0.0.1"},
		"split.example":       {"93.184.216.34", "10.0.0.5"},
		"v6.example":          {"2606:2800:220:1::1"},
		"mapped.example":      {"::ffff:192.168.1.1"},
		"2130706433":          {"127.0.0.1"},
		"metadata.google.int": {"169.254.169.254"},
	})
}

func TestGuardBlocks(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want error
	}{
		{"metadata endpoint by literal", "http://169.254.169.254/latest/meta-data/", ErrBlockedHost},
		{"lan gateway", "http://192.168.1.1/", ErrBlockedHost},
		{"private class A", "http://10.0.0.5:8080/admin", ErrBlockedHost},
		{"private class B", "http://172.16.4.1/", ErrBlockedHost},
		{"loopback literal", "http://127.0.0.1/", ErrBlockedHost},
		{"npm admin on localhost", "http://localhost:81/", ErrBlockedHost},
		{"ipv6 loopback", "http://[::1]:8080/", ErrBlockedHost},
		{"ipv6 unique local", "http://[fd00::1]/", ErrBlockedHost},
		{"ipv6 link local", "http://[fe80::1]/", ErrBlockedHost},
		{"cgnat", "http://100.64.0.1/", ErrBlockedHost},
		{"unspecified", "http://0.0.0.0/", ErrBlockedHost},
		{"ipv4 mapped in ipv6 literal", "http://[::ffff:192.168.1.1]/", ErrBlockedHost},
		{"ipv4 mapped via dns", "http://mapped.example/", ErrBlockedHost},
		{"dns rebinding to loopback", "http://rebind.example/", ErrBlockedHost},
		{"decimal ip form", "http://2130706433/", ErrBlockedHost},
		{"metadata via hostname", "http://metadata.google.int/", ErrBlockedHost},
		{"any private address in answer blocks", "http://split.example/", ErrBlockedHost},
		{"file scheme", "file:///etc/passwd", ErrBlockedScheme},
		{"chrome scheme", "chrome://settings", ErrBlockedScheme},
		{"devtools scheme", "devtools://devtools/bundled/inspector.html", ErrBlockedScheme},
		{"view-source", "view-source:https://example.com", ErrBlockedScheme},
		{"blob", "blob:https://example.com/abc", ErrBlockedScheme},
		{"data", "data:text/html,<script>alert(1)</script>", ErrBlockedScheme},
		{"javascript", "javascript:alert(1)", ErrBlockedScheme},
		{"chrome extension", "chrome-extension://abcd/page.html", ErrBlockedScheme},
		{"uppercase scheme still blocked", "FILE:///etc/passwd", ErrBlockedScheme},
		{"scheme relative", "//example.com/x", ErrBlockedScheme},
		{"no host", "http:///path", ErrInvalidURL},
		{"empty", "", ErrInvalidURL},
		{"unresolvable host", "http://does-not-exist.invalid/", ErrBlockedHost},
	}

	g := testGuard()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := g.Check(context.Background(), tc.url)
			if !errors.Is(err, tc.want) {
				t.Errorf("Check(%q) = %v, want %v", tc.url, err, tc.want)
			}
		})
	}
}

func TestGuardAllows(t *testing.T) {
	cases := []string{
		"https://example.com/",
		"http://example.com/watch?v=abc",
		"https://example.com:8443/path#frag",
		"https://v6.example/",
		"HTTPS://example.com/",
	}

	g := testGuard()
	for _, url := range cases {
		if err := g.Check(context.Background(), url); err != nil {
			t.Errorf("Check(%q) = %v, want allowed", url, err)
		}
	}
}

func TestNormalizeAddsScheme(t *testing.T) {
	cases := map[string]string{
		"example.com":         "https://example.com",
		"example.com/a/b":     "https://example.com/a/b",
		"https://example.com": "https://example.com",
		"  example.com  ":     "https://example.com",
	}

	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeLeavesSearchTermsAlone(t *testing.T) {
	got := Normalize("how to cook rice")
	if got == "https://how to cook rice" {
		t.Errorf("Normalize turned a search phrase into a URL: %q", got)
	}
}
