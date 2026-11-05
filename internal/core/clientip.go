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

// clientIP returns the request's client address (host only). Without trusted
// proxies this is always the peer address; forwarded headers count only when
// the peer is one of the configured proxies.
func clientIP(r *http.Request, trusted []net.IPNet) string {
	if r == nil {
		return ""
	}
	peer := remoteHost(r.RemoteAddr)
	if len(trusted) == 0 || !ipInNets(peer, trusted) {
		return peer
	}
	// Demo nginx sets X-Real-IP; generic edges use X-Forwarded-For. Walk XFF
	// right to left and stop at the first hop that is not a trusted proxy:
	// everything left of that is client-controlled padding.
	if host := hostOf(r.Header.Get("X-Real-IP")); host != "" && net.ParseIP(host) != nil {
		return host
	}
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
