// Package httpdeliver provides a hardened HTTP client for webhook delivery.
package httpdeliver

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// SSRFPolicy configures SSRF protection overrides.
type SSRFPolicy struct {
	AllowPrivate bool     `json:"allow_private"`
	AllowedHosts []string `json:"allowed_hosts"`
}

// SSRFGuard validates URLs against SSRF attacks.
type SSRFGuard struct {
	blockedRanges []*net.IPNet
	cache         *dnsCache
	cacheTTL      time.Duration
}

type dnsCache struct {
	mu      sync.RWMutex
	entries map[string]*dnsCacheEntry
}

type dnsCacheEntry struct {
	ips       []net.IP
	expiresAt time.Time
	blocked   bool
}

// NewSSRFGuard creates a guard with default blocked ranges.
func NewSSRFGuard() *SSRFGuard {
	blockedCIDRs := []string{
		// IPv4
		"127.0.0.0/8",    // Loopback
		"10.0.0.0/8",     // Private Class A
		"172.16.0.0/12",  // Private Class B
		"192.168.0.0/16", // Private Class C
		"169.254.0.0/16", // Link-local
		"0.0.0.0/8",      // Current network
		// IPv6
		"::1/128",     // Loopback
		"fc00::/7",    // Unique local
		"fe80::/10",   // Link-local
		"::ffff:0:0/96", // IPv4-mapped
	}

	var blocked []*net.IPNet
	for _, cidr := range blockedCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err == nil {
			blocked = append(blocked, ipNet)
		}
	}

	return &SSRFGuard{
		blockedRanges: blocked,
		cache: &dnsCache{
			entries: make(map[string]*dnsCacheEntry),
		},
		cacheTTL: 15 * time.Second,
	}
}

// CheckURL validates a URL is safe to request.
func (g *SSRFGuard) CheckURL(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	// Validate scheme
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("scheme must be http or https")
	}

	host := parsed.Hostname()

	// Check cache first
	if entry := g.cache.get(host); entry != nil {
		if entry.blocked {
			return "", fmt.Errorf("blocked IP: %s (cached)", host)
		}
		return rawURL, nil
	}

	// Check if it's a direct IP
	if ip := net.ParseIP(host); ip != nil {
		if g.isBlocked(ip) {
			return "", fmt.Errorf("blocked IP: %s", host)
		}
		return rawURL, nil
	}

	// Resolve hostname
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return "", fmt.Errorf("DNS lookup failed: %w", err)
	}

	// Check each resolved IP
	for _, ip := range ips {
		if g.isBlocked(ip) {
			g.cache.set(host, ips, true, g.cacheTTL)
			return "", fmt.Errorf("blocked IP: %s resolves to %s", host, ip)
		}
	}

	g.cache.set(host, ips, false, g.cacheTTL)
	return rawURL, nil
}

// WithPolicy returns a guard with custom policy applied.
func (g *SSRFGuard) WithPolicy(policy SSRFPolicy) *SSRFGuard {
	if policy.AllowPrivate {
		// Return a guard with no blocked ranges
		return &SSRFGuard{
			blockedRanges: nil,
			cache:         g.cache,
			cacheTTL:      g.cacheTTL,
		}
	}

	if len(policy.AllowedHosts) > 0 {
		// Create allowlist guard
		return &allowlistGuard{
			base:         g,
			allowedHosts: policy.AllowedHosts,
		}
	}

	return g
}

func (g *SSRFGuard) isBlocked(ip net.IP) bool {
	for _, block := range g.blockedRanges {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

func (c *dnsCache) get(host string) *dnsCacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[host]
	if !ok {
		return nil
	}
	if time.Now().After(entry.expiresAt) {
		return nil
	}
	return entry
}

func (c *dnsCache) set(host string, ips []net.IP, blocked bool, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[host] = &dnsCacheEntry{
		ips:       ips,
		expiresAt: time.Now().Add(ttl),
		blocked:   blocked,
	}
}

// allowlistGuard wraps SSRFGuard with host allowlist logic.
type allowlistGuard struct {
	base         *SSRFGuard
	allowedHosts []string
}

func (g *allowlistGuard) CheckURL(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	host := parsed.Hostname()

	// Check if host is in allowlist
	for _, allowed := range g.allowedHosts {
		if host == allowed {
			return rawURL, nil
		}
	}

	// Fall back to base guard
	return g.base.CheckURL(ctx, rawURL)
}

// PolicyChecker interface for URL checking.
type PolicyChecker interface {
	CheckURL(ctx context.Context, rawURL string) (string, error)
}

// Ensure both implement PolicyChecker
var _ PolicyChecker = (*SSRFGuard)(nil)
var _ PolicyChecker = (*allowlistGuard)(nil)

// ParseSSRFPolicy parses SSRF policy from JSON.
func ParseSSRFPolicy(data json.RawMessage) *SSRFPolicy {
	if len(data) == 0 || string(data) == "{}" || string(data) == "null" {
		return nil
	}
	var policy SSRFPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return nil
	}
	if !policy.AllowPrivate && len(policy.AllowedHosts) == 0 {
		return nil
	}
	return &policy
}
