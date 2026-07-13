package gateway

import (
	"fmt"
	"testing"
	"time"
)

func TestRouteCacheHitAndMiss(t *testing.T) {
	routes := newRouteCache()

	if _, ok := routes.get("alice"); ok {
		t.Fatal("expected miss on empty cache")
	}

	routes.put("alice", "node-a")
	if nodeId, ok := routes.get("alice"); !ok || nodeId != "node-a" {
		t.Fatalf("expected node-a hit, got %q %v", nodeId, ok)
	}

	// invalidation forgets the entry, e.g. after a peer rejected a tunnel
	routes.invalidate("alice")
	if _, ok := routes.get("alice"); ok {
		t.Fatal("expected miss after invalidation")
	}
}

func TestRouteCacheExpires(t *testing.T) {
	routes := newRouteCache()
	routes.put("alice", "node-a")

	// age the entry past its expiry
	entry := routes.entries["alice"]
	entry.expiresAt = time.Now().Add(-time.Second)
	routes.entries["alice"] = entry

	if _, ok := routes.get("alice"); ok {
		t.Fatal("expected expired entry to miss")
	}
}

func TestRouteCacheBoundsItsSize(t *testing.T) {
	routes := newRouteCache()

	// filling with expired entries only evicts them on overflow
	for index := range routeCacheMaxEntries {
		user := fmt.Sprintf("user%d", index)
		routes.put(user, "node-a")
		entry := routes.entries[user]
		entry.expiresAt = time.Now().Add(-time.Second)
		routes.entries[user] = entry
	}
	routes.put("alice", "node-a")
	if len(routes.entries) != 1 {
		t.Fatalf("expected expired entries to be evicted, got %d", len(routes.entries))
	}

	// filling with fresh entries resets the cache wholesale on overflow
	for index := range routeCacheMaxEntries + 10 {
		routes.put(fmt.Sprintf("user%d", index), "node-a")
	}
	if len(routes.entries) > routeCacheMaxEntries {
		t.Fatalf("expected the cache to stay bounded, got %d entries", len(routes.entries))
	}
}

func TestGatewayInvalidateRoutes(t *testing.T) {
	instance := &gateway{routes: newRouteCache(), settings: &Settings{}}
	instance.routes.put("alice", "node-a")
	instance.routes.put("controller1.alice", "node-a")
	instance.routes.put("bob", "node-b")

	// every user suffix candidate of the host is forgotten, others survive
	instance.invalidateRoutes("service.controller1.alice")
	if _, ok := instance.routes.get("alice"); ok {
		t.Fatal("expected alice to be invalidated")
	}
	if _, ok := instance.routes.get("controller1.alice"); ok {
		t.Fatal("expected controller1.alice to be invalidated")
	}
	if _, ok := instance.routes.get("bob"); !ok {
		t.Fatal("expected bob to survive")
	}
}
