package websec

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ParsePrefixes parses a comma-separated list of CIDRs or bare IPs
// ("10.42.0.0/16, 192.168.1.5"); a bare IP becomes a /32 or /128.
func ParsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if p, err := netip.ParsePrefix(f); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(f)
		if err != nil {
			return nil, fmt.Errorf("websec: 無法解析信任的代理位址 %q", f)
		}
		a = a.Unmap().WithZone("")
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}

func isTrusted(a netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ClientIP returns the client address: the connection peer, or — when the peer is trusted —
// the right-most X-Forwarded-For entry that is not trusted.
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap().WithZone("")
	if !isTrusted(peer, trusted) {
		return peer.String()
	}
	var chain []netip.Addr
	for _, h := range r.Header.Values("X-Forwarded-For") {
		for _, e := range strings.Split(h, ",") {
			if a, err := netip.ParseAddr(strings.TrimSpace(e)); err == nil {
				chain = append(chain, a.Unmap().WithZone(""))
			}
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if !isTrusted(chain[i], trusted) {
			return chain[i].String()
		}
	}
	if len(chain) > 0 {
		return chain[0].String()
	}
	return peer.String()
}
