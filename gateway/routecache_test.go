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
	for index := range routeCacheMaxEntries + 10 {
		routes.put(fmt.Sprintf("user%d", index), "node-a")
	}
	if len(routes.entries) > routeCacheMaxEntries {
		t.Fatalf("expected the cache to stay bounded, got %d entries", len(routes.entries))
	}
}
