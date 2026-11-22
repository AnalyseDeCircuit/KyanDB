// Package tunnel owns SSH transports for preconfigured database routes.
package tunnel

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os/user"
	"strconv"
	"strings"
	"unicode"

	"github.com/AnalyseDeCircuit/kyandb/internal/domain"
)

type Hop struct {
	User, Host string
	Port       int
	Auth       Auth
}

// Chain is ordered from the local machine towards the database.
type Chain []Hop

func (Hop) String() string     { return "[REDACTED]" }
func (Hop) GoString() string   { return "[REDACTED]" }
func (Chain) String() string   { return "[REDACTED]" }
func (Chain) GoString() string { return "[REDACTED]" }

var errHop = errors.New("invalid SSH hop")

func (h Hop) normalized() (Hop, error) {
	if h.Port == 0 {
		h.Port = 22
	}
	if h.Port < 1 || h.Port > 65535 || h.Host == "" ||
		strings.ContainsAny(h.User, "@:,/") ||
		strings.IndexFunc(h.User+h.Host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return Hop{}, errHop
	}
	if ip, err := netip.ParseAddr(h.Host); err == nil {
		h.Host = ip.String()
	} else {
		for _, r := range h.Host {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_", r)) {
				return Hop{}, errHop
			}
		}
		h.Host = strings.ToLower(h.Host)
	}
	return h, nil
}

func ParseProxyJump(path string) (Chain, error) {
	var chain Chain
	for i, item := range strings.Split(path, ",") {
		h, err := parseHop(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("ProxyJump hop %d: %w", i+1, err)
		}
		chain = append(chain, h)
	}
	return chain, nil
}

func parseHop(item string) (Hop, error) {
	h := Hop{Port: 22}
	if u, rest, ok := strings.Cut(item, "@"); ok {
		if u == "" {
			return Hop{}, errHop
		}
		h.User, item = u, rest
	}
	var port string
	if strings.HasPrefix(item, "[") {
		end := strings.IndexByte(item, ']')
		if end < 0 {
			return Hop{}, errHop
		}
		h.Host = item[1:end]
		ip, err := netip.ParseAddr(h.Host)
		if err != nil || !ip.Is6() {
			return Hop{}, errHop
		}
		if suffix := item[end+1:]; suffix != "" {
			if len(suffix) < 2 || suffix[0] != ':' {
				return Hop{}, errHop
			}
			port = suffix[1:]
		}
	} else {
		var explicit bool
		h.Host, port, explicit = strings.Cut(item, ":")
		if explicit && port == "" {
			return Hop{}, errHop
		}
	}
	if port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return Hop{}, errHop
		}
		h.Port = int(n)
	}
	return h.normalized()
}

// ChainFromProfile appends the SSH endpoint, not the database endpoint. A
// caller with per-hop credentials may replace individual Auth values before Open.
func ChainFromProfile(p domain.Profile, auth Auth) (Chain, error) {
	if !p.SSHEnabled {
		return nil, fmt.Errorf("%w: SSH is disabled", domain.ErrInvalidProfile)
	}
	var chain Chain
	if path := strings.TrimSpace(p.SSHJumpPath); path != "" {
		var err error
		chain, err = ParseProxyJump(path)
		if err != nil {
			return nil, err
		}
	}
	if p.SSHHost != "" {
		host := p.SSHHost
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			ip, err := netip.ParseAddr(host[1 : len(host)-1])
			if err != nil || !ip.Is6() {
				return nil, errHop
			}
			host = ip.String()
		}
		chain = append(chain, Hop{Host: host, Port: p.SSHPort})
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("%w: missing SSH route", domain.ErrInvalidProfile)
	}
	for i, h := range chain {
		if h.User == "" {
			h.User = p.SSHUser
		}
		h.Auth = auth
		var err error
		chain[i], err = h.normalized()
		if err != nil {
			return nil, err
		}
	}
	return chain, nil
}

func (h Hop) endpoint() (Hop, string, error) {
	if h.User == "" {
		u, err := user.Current()
		if err != nil {
			return Hop{}, "", errors.New("cannot determine local SSH user")
		}
		h.User = u.Username
	}
	h, err := h.normalized()
	if err != nil {
		return Hop{}, "", err
	}
	return h, net.JoinHostPort(h.Host, strconv.Itoa(h.Port)), nil
}
