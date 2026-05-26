package protocols

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// dnsCacheDialer wraps net.Dialer with a simple TTL-based DNS cache.
// This prevents repeated DNS lookups from inflating per-request latency measurements.
type dnsCacheDialer struct {
	mu         sync.RWMutex
	cache      map[string]dnsCacheEntry
	order      []string // FIFO insertion order for size-bounded eviction
	maxEntries int
	ttl        time.Duration
	dialer     net.Dialer
}

type dnsCacheEntry struct {
	addrs  []string
	expiry time.Time
}

func newDNSCacheDialer(ttl time.Duration) *dnsCacheDialer {
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return &dnsCacheDialer{
		cache:      make(map[string]dnsCacheEntry),
		maxEntries: 1024,
		ttl:        ttl,
	}
}

func (d *dnsCacheDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return d.dialer.DialContext(ctx, network, addr)
	}

	// IP literals don't need caching.
	if net.ParseIP(host) != nil {
		return d.dialer.DialContext(ctx, network, addr)
	}

	addrs, err := d.resolve(ctx, host)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for _, ip := range addrs {
		conn, err := d.dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no addresses resolved for %s", host)
}

func (d *dnsCacheDialer) resolve(ctx context.Context, host string) ([]string, error) {
	d.mu.RLock()
	if e, ok := d.cache[host]; ok && time.Now().Before(e.expiry) {
		addrs := e.addrs
		d.mu.RUnlock()
		return addrs, nil
	}
	d.mu.RUnlock()

	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("DNS lookup %s: %w", host, err)
	}

	d.mu.Lock()
	if _, exists := d.cache[host]; !exists {
		if len(d.cache) >= d.maxEntries && len(d.order) > 0 {
			oldest := d.order[0]
			d.order = d.order[1:]
			delete(d.cache, oldest)
		}
		d.order = append(d.order, host)
	}
	d.cache[host] = dnsCacheEntry{addrs: addrs, expiry: time.Now().Add(d.ttl)}
	d.mu.Unlock()

	return addrs, nil
}
