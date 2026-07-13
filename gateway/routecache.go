package gateway

import (
	"sync"
	"time"
)

const (
	// how long a mesh route (user to node) lookup is remembered; node_id only
	// changes on the heartbeat cadence, so this adds no staleness beyond what
	// the heartbeat already has, while repeated tunnel opens skip the
	// cross-region query
	routeCacheTimeToLive = 15 * time.Second

	// looked-up hostnames are chosen by clients, so the cache size is bounded
	routeCacheMaxEntries = 4096
)

type routeCacheEntry struct {
	nodeId    string
	expiresAt time.Time
}

// routeCache remembers which remote node a user is on, so repeated mesh
// tunnel opens cost no database roundtrip. Callers only store routable
// results, a user without a node must be noticed the moment they connect.
type routeCache struct {
	entries map[string]routeCacheEntry
	lock    sync.Mutex
}

func newRouteCache() *routeCache {
	return &routeCache{
		entries: make(map[string]routeCacheEntry),
	}
}

func (self *routeCache) get(userId string) (string, bool) {
	self.lock.Lock()
	defer self.lock.Unlock()

	entry, ok := self.entries[userId]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.nodeId, true
}

func (self *routeCache) put(userId string, nodeId string) {
	self.lock.Lock()
	defer self.lock.Unlock()

	if len(self.entries) >= routeCacheMaxEntries {
		// drop expired entries, and reset wholesale if everything is fresh
		now := time.Now()
		for key, entry := range self.entries {
			if now.After(entry.expiresAt) {
				delete(self.entries, key)
			}
		}
		if len(self.entries) >= routeCacheMaxEntries {
			self.entries = make(map[string]routeCacheEntry)
		}
	}
	self.entries[userId] = routeCacheEntry{
		nodeId:    nodeId,
		expiresAt: time.Now().Add(routeCacheTimeToLive),
	}
}
