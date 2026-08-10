package user

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"forest/go-api/internal/cliententry"
)

func TestClientEntryHostResolverDoesNotLookupLiteralIP(t *testing.T) {
	var calls atomic.Int64
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{netip.MustParseAddr("192.0.2.1")}, nil
	})

	if got := resolver.Resolve(context.Background(), "203.0.113.7", "user:1"); got != "203.0.113.7" {
		t.Fatalf("literal IPv4 changed to %q", got)
	}
	if got := resolver.Resolve(context.Background(), "2001:0db8::7", "user:1"); got != "2001:db8::7" {
		t.Fatalf("literal IPv6 was not normalized: %q", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("literal IP triggered %d DNS lookups", calls.Load())
	}
}

func TestClientEntryHostResolverSortsDeduplicatesAndSelectsStably(t *testing.T) {
	var calls atomic.Int64
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{
			netip.MustParseAddr("2001:db8::20"),
			netip.MustParseAddr("192.0.2.20"),
			netip.MustParseAddr("192.0.2.10"),
			netip.MustParseAddr("::ffff:192.0.2.10"),
		}, nil
	})

	// An empty selection key chooses the first sorted IPv4 address.
	if got := resolver.Resolve(context.Background(), "Entry.Example.com.", ""); got != "192.0.2.10" {
		t.Fatalf("first stable address = %q, want 192.0.2.10", got)
	}
	first := resolver.Resolve(context.Background(), "entry.example.com", "user:42:policy:7")
	for index := 0; index < 10; index++ {
		if got := resolver.Resolve(context.Background(), "ENTRY.EXAMPLE.COM", "user:42:policy:7"); got != first {
			t.Fatalf("multi-address selection changed from %q to %q", first, got)
		}
	}
	if first != "192.0.2.10" && first != "192.0.2.20" {
		t.Fatalf("IPv4 should be preferred when A and AAAA both exist, got %q", first)
	}
	if calls.Load() != 1 {
		t.Fatalf("cached hostname triggered %d DNS lookups, want 1", calls.Load())
	}
}

func TestClientEntryHostResolverFailureFallsBackAndIsNegativelyCached(t *testing.T) {
	var calls atomic.Int64
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		return nil, errors.New("temporary DNS failure")
	})

	for index := 0; index < 3; index++ {
		if got := resolver.Resolve(context.Background(), "unavailable.example.com", "user:1"); got != "unavailable.example.com" {
			t.Fatalf("DNS failure should retain configured hostname, got %q", got)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("negative cache triggered %d DNS lookups, want 1", calls.Load())
	}
}

func TestClientEntryHostResolverCollapsesConcurrentLookups(t *testing.T) {
	var calls atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-release
		return []netip.Addr{netip.MustParseAddr("198.51.100.8")}, nil
	})

	const workers = 32
	results := make(chan string, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for index := 0; index < workers; index++ {
		go func() {
			defer wait.Done()
			results <- resolver.Resolve(context.Background(), "burst.example.com", "same-user")
		}()
	}
	<-started
	close(release)
	wait.Wait()
	close(results)

	for result := range results {
		if result != "198.51.100.8" {
			t.Fatalf("concurrent resolution returned %q", result)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("concurrent cache miss executed %d lookups, want 1", calls.Load())
	}
}

func TestApplyClientEntryUserPoliciesResolvesOnlyWhenEnabled(t *testing.T) {
	var calls atomic.Int64
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		calls.Add(1)
		return []netip.Addr{netip.MustParseAddr("198.51.100.21")}, nil
	})
	server := []map[string]any{{"id": int64(11), "type": "vmess", "host": "original.example.com"}}

	disabled := []clientEntryUserPolicy{{
		ID: 1, Action: cliententry.ActionOverride, EntryHost: "entry.example.com",
		Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
	}}
	result := applyClientEntryUserPoliciesWithResolver(context.Background(), cloneServerMapsForTest(server), cliententry.Subject{UserID: 100}, disabled, resolver)
	if got := result[0]["host"]; got != "entry.example.com" {
		t.Fatalf("disabled resolver changed entry host to %#v", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("disabled rule performed %d lookups", calls.Load())
	}

	enabled := []clientEntryUserPolicy{{
		ID: 2, Action: cliententry.ActionOverride, EntryHost: "entry.example.com", ResolveEntryHost: true,
		Members: []ClientEntryGroupMember{{ServerType: "vmess", ServerID: 11}},
	}}
	result = applyClientEntryUserPoliciesWithResolver(context.Background(), cloneServerMapsForTest(server), cliententry.Subject{UserID: 100}, enabled, resolver)
	if got := result[0]["host"]; got != "198.51.100.21" {
		t.Fatalf("enabled resolver host = %#v, want resolved IP", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("enabled rule performed %d lookups, want 1", calls.Load())
	}
}

func TestApplyClientEntryUserPoliciesResolutionFailureRetainsDomain(t *testing.T) {
	resolver := newClientEntryHostResolverForTest(func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("DNS unavailable")
	})
	servers := []map[string]any{{"id": int64(11), "type": "trojan", "host": "original.example.com"}}
	policies := []clientEntryUserPolicy{{
		ID: 3, Action: cliententry.ActionOverride, EntryHost: "fallback.example.com", ResolveEntryHost: true,
		Members: []ClientEntryGroupMember{{ServerType: "trojan", ServerID: 11}},
	}}

	result := applyClientEntryUserPoliciesWithResolver(context.Background(), servers, cliententry.Subject{UserID: 101}, policies, resolver)
	if got := result[0]["host"]; got != "fallback.example.com" {
		t.Fatalf("DNS failure should retain configured domain, got %#v", got)
	}
}

func newClientEntryHostResolverForTest(lookup clientEntryDNSLookupFunc) *clientEntryHostResolver {
	return &clientEntryHostResolver{
		lookup:     lookup,
		timeout:    time.Second,
		successTTL: time.Minute,
		failureTTL: time.Minute,
		now:        time.Now,
		cache:      make(map[string]clientEntryDNSCacheValue),
	}
}
