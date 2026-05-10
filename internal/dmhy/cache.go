package dmhy

import (
	"container/list"
	"sync"
)

// magnetCache is a bounded LRU keyed by infohash → magnet (and dn / title for
// reconstruction). On eviction the oldest entry by access order is dropped.
// Safe for concurrent use.
type magnetCache struct {
	mu    sync.Mutex
	items map[string]*list.Element
	order *list.List
	cap   int
}

type magnetEntry struct {
	infoHash string
	magnet   string
	title    string
}

func newMagnetCache(capacity int) *magnetCache {
	if capacity <= 0 {
		capacity = 5000
	}
	return &magnetCache{
		items: make(map[string]*list.Element, capacity),
		order: list.New(),
		cap:   capacity,
	}
}

// put inserts or refreshes an entry. Title and magnet are stored verbatim.
// Empty infohash is a no-op.
func (c *magnetCache) put(infoHash, magnet, title string) {
	if infoHash == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[infoHash]; ok {
		entry := el.Value.(*magnetEntry)
		entry.magnet = magnet
		entry.title = title
		c.order.MoveToFront(el)
		return
	}
	entry := &magnetEntry{infoHash: infoHash, magnet: magnet, title: title}
	el := c.order.PushFront(entry)
	c.items[infoHash] = el
	for c.order.Len() > c.cap {
		oldest := c.order.Back()
		if oldest == nil {
			break
		}
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*magnetEntry).infoHash)
	}
}

// get returns the cached entry and bumps it to MRU. ok=false on miss.
func (c *magnetCache) get(infoHash string) (magnet, title string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, hit := c.items[infoHash]
	if !hit {
		return "", "", false
	}
	c.order.MoveToFront(el)
	entry := el.Value.(*magnetEntry)
	return entry.magnet, entry.title, true
}
