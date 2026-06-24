package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// respCache is a bounded LRU cache of render results keyed on the request
// content. Renders are a pure function of (gsx, invoke) given the fixed module
// and gsx version, so caching is safe — identical requests (the presets every
// visitor loads, undo/redo, re-runs) return instantly without touching the
// toolchain. It is per-instance and in-memory; a fresh Cloud Run instance warms
// its own cache (see seedPresets).
type respCache struct {
	mu  sync.Mutex
	cap int
	ll  *list.List               // front = most recently used
	m   map[string]*list.Element // key -> *list.Element holding *cacheEntry
}

type cacheEntry struct {
	key  string
	resp renderResp
}

func newRespCache(capacity int) *respCache {
	return &respCache{cap: capacity, ll: list.New(), m: map[string]*list.Element{}}
}

// cacheKey is the content hash of a request.
func cacheKey(in renderReq) string {
	h := sha256.New()
	h.Write([]byte(in.GSX))
	h.Write([]byte{0})
	h.Write([]byte(in.Invoke))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *respCache) get(key string) (renderResp, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheEntry).resp, true
	}
	return renderResp{}, false
}

func (c *respCache) put(key string, resp renderResp) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		el.Value.(*cacheEntry).resp = resp
		c.ll.MoveToFront(el)
		return
	}
	c.m[key] = c.ll.PushFront(&cacheEntry{key: key, resp: resp})
	for c.ll.Len() > c.cap {
		back := c.ll.Back()
		if back == nil {
			break
		}
		c.ll.Remove(back)
		delete(c.m, back.Value.(*cacheEntry).key)
	}
}
