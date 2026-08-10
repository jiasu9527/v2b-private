package user

import (
	"context"
	"hash/fnv"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const (
	clientEntryDNSLookupTimeout = 2 * time.Second
	clientEntryDNSSuccessTTL    = 5 * time.Minute
	clientEntryDNSFailureTTL    = 30 * time.Second
)

type clientEntryDNSLookupFunc func(context.Context, string, string) ([]netip.Addr, error)

type clientEntryDNSCacheValue struct {
	addresses []netip.Addr
	expiresAt time.Time
}

// clientEntryHostResolver resolves configured entry-rule hostnames on the
// server. Successful and failed lookups are both cached so subscription bursts
// do not turn into DNS bursts. singleflight also collapses concurrent cache
// misses for the same hostname into one lookup.
type clientEntryHostResolver struct {
	lookup     clientEntryDNSLookupFunc
	timeout    time.Duration
	successTTL time.Duration
	failureTTL time.Duration
	now        func() time.Time

	mu    sync.Mutex
	cache map[string]clientEntryDNSCacheValue
	group singleflight.Group
}

func newClientEntryHostResolver() *clientEntryHostResolver {
	return &clientEntryHostResolver{
		lookup: func(ctx context.Context, network, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, network, host)
		},
		timeout:    clientEntryDNSLookupTimeout,
		successTTL: clientEntryDNSSuccessTTL,
		failureTTL: clientEntryDNSFailureTTL,
		now:        time.Now,
		cache:      make(map[string]clientEntryDNSCacheValue),
	}
}

// Resolve returns the original hostname when DNS fails. selectionKey keeps the
// selected address stable when a hostname has multiple records while allowing
// different users/rules to spread across those records. IPv4 is preferred when
// both address families are available for compatibility with older clients.
func (r *clientEntryHostResolver) Resolve(ctx context.Context, host, selectionKey string) string {
	originalHost := strings.TrimSpace(host)
	if originalHost == "" {
		return originalHost
	}
	if address, err := netip.ParseAddr(originalHost); err == nil && address.Zone() == "" {
		return address.Unmap().String()
	}
	if r == nil || r.lookup == nil {
		return originalHost
	}

	lookupHost := strings.TrimSuffix(strings.ToLower(originalHost), ".")
	if lookupHost == "" {
		return originalHost
	}
	addresses, ok := r.cached(lookupHost)
	if !ok {
		addresses = r.lookupAddresses(ctx, lookupHost)
	}
	if len(addresses) == 0 {
		return originalHost
	}
	return selectClientEntryAddress(addresses, selectionKey).String()
}

func (r *clientEntryHostResolver) cached(host string) ([]netip.Addr, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	value, ok := r.cache[host]
	if !ok {
		return nil, false
	}
	if !r.now().Before(value.expiresAt) {
		delete(r.cache, host)
		return nil, false
	}
	return value.addresses, true
}

func (r *clientEntryHostResolver) lookupAddresses(ctx context.Context, host string) []netip.Addr {
	resultChannel := r.group.DoChan(host, func() (any, error) {
		// The cache may have been populated while this caller was waiting to
		// enter singleflight.
		if addresses, ok := r.cached(host); ok {
			return addresses, nil
		}

		lookupContext, cancel := context.WithTimeout(context.Background(), r.timeout)
		defer cancel()
		addresses, err := r.lookup(lookupContext, "ip", host)
		addresses = normalizeClientEntryAddresses(addresses)
		ttl := r.successTTL
		if err != nil || len(addresses) == 0 {
			addresses = nil
			ttl = r.failureTTL
		}
		r.mu.Lock()
		r.cache[host] = clientEntryDNSCacheValue{
			addresses: addresses,
			expiresAt: r.now().Add(ttl),
		}
		r.mu.Unlock()
		return addresses, err
	})

	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case result := <-resultChannel:
		addresses, _ := result.Val.([]netip.Addr)
		return addresses
	case <-ctx.Done():
		return nil
	}
}

func normalizeClientEntryAddresses(values []netip.Addr) []netip.Addr {
	result := make([]netip.Addr, 0, len(values))
	seen := make(map[netip.Addr]struct{}, len(values))
	for _, value := range values {
		if !value.IsValid() || value.Zone() != "" {
			continue
		}
		value = value.Unmap()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	slices.SortFunc(result, func(left, right netip.Addr) int {
		return left.Compare(right)
	})
	return result
}

func selectClientEntryAddress(values []netip.Addr, selectionKey string) netip.Addr {
	pool := values
	firstIPv6 := len(values)
	for index, value := range values {
		if value.Is6() {
			firstIPv6 = index
			break
		}
	}
	if firstIPv6 > 0 {
		pool = values[:firstIPv6]
	}
	if len(pool) == 1 || selectionKey == "" {
		return pool[0]
	}
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(selectionKey))
	return pool[hash.Sum64()%uint64(len(pool))]
}
