package isr

import (
	"encoding/json"

	"github.com/grafana/sobek"
)

// The "use cache" store (Next's Cache Components): what Next's default
// cache handler keeps in its module's memory, kept here instead, so every
// request's engine (each a fresh VM) shares it. The JS side is the handler
// the Next entry installs as Symbol.for("@next/cache-handlers").DefaultCache
// (internal/frameworks/next), which reads and writes it through og.cache:
// entries (their metadata as JSON, their RSC stream's bytes) and the tags
// manifest (per tag: when it went stale and when it expires, in ms).

// useCacheMaxBytes bounds the entries' bytes, as Next's default
// cacheMaxMemorySize (50 MB) bounds its in-memory handler.
const useCacheMaxBytes = 50 << 20

type useCacheEntry struct {
	meta string // the entry without its value: tags, timestamp, stale, expire, revalidate
	body []byte
}

// UseCacheTag is a tag's state in the manifest (Next's tags-manifest).
type UseCacheTag struct {
	Stale   *int64 `json:"stale,omitempty"`
	Expired *int64 `json:"expired,omitempty"`
}

type useCacheState struct {
	entries map[string]*useCacheEntry
	order   []string // insertion order, for eviction
	bytes   int
	tags    map[string]*UseCacheTag
}

func (c *Cache) useCache() *useCacheState {
	if c.uc == nil {
		c.uc = &useCacheState{entries: map[string]*useCacheEntry{}, tags: map[string]*UseCacheTag{}}
	}
	return c.uc
}

// installUseCache adds the "use cache" store's functions to og.cache.
func (c *Cache) installUseCache(vm *sobek.Runtime, o *sobek.Object) {
	_ = o.Set("useCacheGet", func(key string) sobek.Value {
		c.mu.Lock()
		defer c.mu.Unlock()
		e := c.useCache().entries[key]
		if e == nil {
			return sobek.Null()
		}
		r := vm.NewObject()
		_ = r.Set("meta", e.meta)
		_ = r.Set("body", vm.NewArrayBuffer(append([]byte(nil), e.body...)))
		return r
	})
	_ = o.Set("useCacheSet", func(key, meta string, body sobek.ArrayBuffer) {
		b := append([]byte(nil), body.Bytes()...)
		c.mu.Lock()
		defer c.mu.Unlock()
		uc := c.useCache()
		if old := uc.entries[key]; old != nil {
			uc.bytes -= len(old.body)
		} else {
			uc.order = append(uc.order, key)
		}
		uc.entries[key] = &useCacheEntry{meta: meta, body: b}
		uc.bytes += len(b)
		for uc.bytes > useCacheMaxBytes && len(uc.order) > 1 {
			oldest := uc.order[0]
			uc.order = uc.order[1:]
			if e := uc.entries[oldest]; e != nil && oldest != key {
				uc.bytes -= len(e.body)
				delete(uc.entries, oldest)
			}
		}
	})
	_ = o.Set("useCacheTags", func(tags []string) string {
		c.mu.Lock()
		defer c.mu.Unlock()
		out := map[string]*UseCacheTag{}
		for _, t := range tags {
			if s := c.useCache().tags[t]; s != nil {
				out[t] = s
			}
		}
		b, _ := json.Marshal(out)
		return string(b)
	})
	// useCacheUpdateTags is the default handler's updateTags: with
	// durations, the tags go stale now and expire after expire seconds
	// (when given); without, they expire now.
	_ = o.Set("useCacheUpdateTags", func(tags []string, withDurations bool, expire sobek.Value) {
		c.mu.Lock()
		defer c.mu.Unlock()
		now := c.now().UnixMilli()
		uc := c.useCache()
		for _, t := range tags {
			s := uc.tags[t]
			if s == nil {
				s = &UseCacheTag{}
				uc.tags[t] = s
			}
			if withDurations {
				stale := now
				s.Stale = &stale
				if expire != nil && !sobek.IsUndefined(expire) && !sobek.IsNull(expire) {
					at := now + int64(expire.ToFloat()*1000)
					s.Expired = &at
				}
			} else {
				at := now
				s.Expired = &at
			}
		}
	})
}
