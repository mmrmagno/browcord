package cdp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

var (
	ErrInvalidURL    = errors.New("cdp: url is not navigable")
	ErrBlockedScheme = errors.New("cdp: scheme is not permitted")
	ErrBlockedHost   = errors.New("cdp: host resolves into blocked address space")
	ErrUnresolved    = fmt.Errorf("%w: name could not be resolved", ErrBlockedHost)
)

const SearchPrefix = "https://duckduckgo.com/?q="

var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type Guard struct {
	resolver Resolver
}

func NewGuard(r Resolver) *Guard {
	if r == nil {
		r = net.DefaultResolver
	}
	return &Guard{resolver: r}
}

func (g *Guard) Check(ctx context.Context, rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return fmt.Errorf("%w: empty", ErrInvalidURL)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: %q", ErrBlockedScheme, u.Scheme)
	}

	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: no host", ErrInvalidURL)
	}

	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: %q", ErrBlockedHost, host)
	}

	if addr, err := netip.ParseAddr(host); err == nil {
		if isBlocked(addr) {
			return fmt.Errorf("%w: %s", ErrBlockedHost, addr)
		}
		return nil
	}

	addrs, err := g.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return fmt.Errorf("%w: %q", ErrUnresolved, host)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: %q", ErrUnresolved, host)
	}

	for _, addr := range addrs {
		if isBlocked(addr) {
			return fmt.Errorf("%w: %q resolves to %s", ErrBlockedHost, host, addr)
		}
	}

	return nil
}

func isBlocked(addr netip.Addr) bool {
	addr = addr.Unmap()

	if !addr.IsValid() ||
		addr.IsLoopback() ||
		addr.IsPrivate() ||
		addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsInterfaceLocalMulticast() ||
		addr.IsMulticast() {
		return true
	}

	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}

	return false
}

func Normalize(input string) string {
	s := strings.TrimSpace(input)
	if s == "" {
		return s
	}

	if u, err := url.Parse(s); err == nil && u.Scheme != "" && !strings.Contains(u.Scheme, ".") {
		return s
	}

	if strings.ContainsAny(s, " \t\n") || !strings.Contains(s, ".") {
		return SearchPrefix + url.QueryEscape(s)
	}

	return "https://" + s
}
