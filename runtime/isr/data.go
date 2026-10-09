package isr

import (
	"context"
	_ "embed"
	"encoding/json"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// CacheHandlerModule is the import specifier of Next's cache handler
// implementation; templates import it as incrementalCacheHandler.
const CacheHandlerModule = "og:cache-handler"

//go:embed js/cache-handler.js
var cacheHandlerSource string

type dataEntry struct {
	value        string
	tags         []string
	lastModified time.Time
}

// data cache and tag state live on Cache alongside pages.
type dataState struct {
	data        map[string]*dataEntry
	tagsRevised map[string]time.Time
}

func (c *Cache) dataState() *dataState {
	if c.ds == nil {
		c.ds = &dataState{data: map[string]*dataEntry{}, tagsRevised: map[string]time.Time{}}
	}
	return c.ds
}

// Plugin resolves CacheHandlerModule for esbuild.
func Plugin() api.Plugin {
	return api.Plugin{
		Name: "og-cache-handler",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: "^og:cache-handler$"}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "cache-handler", Namespace: "og-cache"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "og-cache"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
				src := cacheHandlerSource
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
		},
	}
}

// Install exposes the cache to the cache handler module in e.
func Install(e *engine.Engine, c *Cache) error {
	vm := e.VM
	o := vm.NewObject()
	_ = o.Set("get", func(key string, tags []string) sobek.Value {
		c.mu.Lock()
		defer c.mu.Unlock()
		ds := c.dataState()
		d, ok := ds.data[key]
		if !ok {
			return sobek.Null()
		}
		for _, t := range append(append([]string(nil), d.tags...), tags...) {
			if at, ok := ds.tagsRevised[t]; ok && !at.Before(d.lastModified) {
				delete(ds.data, key)
				return sobek.Null()
			}
		}
		r := vm.NewObject()
		_ = r.Set("value", d.value)
		_ = r.Set("lastModified", d.lastModified.UnixMilli())
		return r
	})
	_ = o.Set("set", func(key, value string, tags []string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.dataState().data[key] = &dataEntry{value: value, tags: tags, lastModified: c.now()}
	})
	_ = o.Set("delete", func(key string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.dataState().data, key)
	})
	_ = o.Set("revalidateTags", func(tags []string) {
		c.mu.Lock()
		now := c.now()
		ds := c.dataState()
		for _, t := range tags {
			ds.tagsRevised[t] = now
		}
		c.mu.Unlock()
		c.RevalidateTags(tags)
	})
	// staticPaths(page): the build's StaticPaths of a dynamic page, as JSON
	// (null when the build recorded none).
	_ = o.Set("staticPaths", func(page string) sobek.Value {
		c.mu.Lock()
		sp := c.staticPaths[page]
		c.mu.Unlock()
		if sp == nil {
			return sobek.Null()
		}
		b, err := json.Marshal(sp)
		if err != nil {
			return sobek.Null()
		}
		return vm.ToValue(string(b))
	})
	// revalidatePath(path, onlyGenerated): regenerate a page now (Next's
	// res.revalidate); resolves to { status, revalidated }.
	_ = o.Set("revalidatePath", func(path string, onlyGenerated bool) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		c.mu.Lock()
		rv := c.revalidator
		c.mu.Unlock()
		if rv == nil {
			_ = reject(vm.NewTypeError("og: on-demand revalidation is not available here"))
			return vm.ToValue(promise)
		}
		complete := e.Loop.Async()
		go func() {
			status, revalidated, err := rv(context.Background(), path, onlyGenerated)
			complete(func(vm *sobek.Runtime) error {
				if err != nil {
					return reject(vm.NewGoError(err))
				}
				r := vm.NewObject()
				_ = r.Set("status", status)
				_ = r.Set("revalidated", revalidated)
				return resolve(r)
			})
		}()
		return vm.ToValue(promise)
	})
	c.installUseCache(vm, o)
	install, err := vm.RunString(`(function (o) { Object.defineProperty(globalThis, Symbol.for("og.cache"), { value: o }); })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}
