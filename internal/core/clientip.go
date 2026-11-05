package core

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Client-IP resolution for rate limiting and audit (issue #253). Forwarded
// headers are believed only when the immediate peer is a configured trusted
// proxy — otherwise they are client-controlled forgery. RemoteAddr is the
// accepted connection's address and cannot be forged.

// Forwarded-header choices for trusted-proxy client-IP resolution. Exactly
// one header is consulted (never a fallback chain): an edge that only
// appends X-Forwarded-For passes client-supplied X-Real-IP through
// unchanged, so consulting both makes the resolved IP forgeable.
const (
	ClientIPHeaderXFF     = "x-forwarded-for"
	ClientIPHeaderXRealIP = "x-real-ip"
)

// compileTrustedProxies parses config entries (CIDR or single IP) into nets.
// An empty list means forwarded headers are never trusted.
func compileTrustedProxies(entries []string) ([]net.IPNet, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]net.IPNet, 0, len(entries))
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("trusted_proxies: %q is not an IP or CIDR", entry)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			out = append(out, net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, cidr, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("trusted_proxies: %q: %w", entry, err)
		}
		out = append(out, *cidr)
	}
	return out, nil
}

// compileClientIPHeader validates the configured forwarded header. Empty
// defaults to X-Forwarded-For.
func compileClientIPHeader(header string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(header)) {
	case "", ClientIPHeaderXFF:
		return ClientIPHeaderXFF, nil
	case ClientIPHeaderXRealIP:
		return ClientIPHeaderXRealIP, nil
	default:
		return "", fmt.Errorf("client_ip_header: %q is not %q or %q", header, ClientIPHeaderXFF, ClientIPHeaderXRealIP)
	}
}

// clientIP returns the request's client address (host only). Without trusted
// proxies this is always the peer address. With them, exactly one forwarded
// header is read — whichever the edge is configured to overwrite — and a
// missing or invalid value falls back to the peer, never to the other
// header.
func clientIP(r *http.Request, trusted []net.IPNet, header string) string {
	if r == nil {
		return ""
	}
	peer := remoteHost(r.RemoteAddr)
	if len(trusted) == 0 || !ipInNets(peer, trusted) {
		return peer
	}
	if header == ClientIPHeaderXRealIP {
		if host := hostOf(r.Header.Get("X-Real-IP")); host != "" && net.ParseIP(host) != nil {
			return host
		}
		return peer
	}
	// X-Forwarded-For: walk right to left and stop at the first hop that is
	// not a trusted proxy — everything left of that is client-controlled
	// padding. Demo nginx sets X-Real-IP instead; operators behind such an
	// edge set client_ip_header to x-real-ip.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			host := hostOf(parts[i])
			if host == "" || net.ParseIP(host) == nil {
				continue
			}
			if !ipInNets(host, trusted) {
				return host
			}
			if i == 0 {
				return host
			}
		}
	}
	return peer
}

func remoteHost(remoteAddr string) string {
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

func hostOf(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		return strings.TrimSpace(host)
	}
	return value
}

func ipInNets(host string, nets []net.IPNet) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for i := range nets {
		if nets[i].Contains(ip) {
			return true
		}
	}
	return false
}

// ipBucket reduces a client address to its rate-limit key (issue #253).
// IPv6 rotates per-device within a /64 (privacy extensions), so per-address
// keys would make the caps meaningless — bucket the whole /64. IPv4 keys
// stay per-address.
func ipBucket(host string) string {
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if ip.To4() != nil {
		return host
	}
	masked := ip.Mask(net.CIDRMask(64, 128))
	if masked == nil {
		return host
	}
	return masked.String() + "/64"
}

// isLoopbackHost reports whether a resolved client address is loopback.
func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
