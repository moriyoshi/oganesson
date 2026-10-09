// Package kv is Oganesson's built-in key-value store, shared by all requests of
// one process (each request's JS engine is otherwise fully isolated). The
// JS API (module "og:kv") follows Deno KV.
package kv

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Value is a stored value: either opaque serialized bytes (from the JS
// serializer) or a native unsigned 64-bit integer (Deno.KvU64).
type Value struct {
	Data  []byte
	U64   uint64
	IsU64 bool
}

// Entry is a key with its value and versionstamp.
type Entry struct {
	Key          []KeyPart
	Value        Value
	Versionstamp string
}

type record struct {
	key     []KeyPart
	value   Value
	version uint64
	expires time.Time // zero: never
}

// Store is an in-memory KV store. It is safe for concurrent use.
type Store struct {
	mu      sync.Mutex
	data    map[string]*record // by encoded key
	version uint64
	now     func() time.Time
}

// NewMemory returns an empty in-memory store.
func NewMemory() *Store {
	return &Store{data: map[string]*record{}, now: time.Now}
}

func versionstamp(v uint64) string { return fmt.Sprintf("%020x", v) }

func (s *Store) live(r *record) bool {
	return r != nil && (r.expires.IsZero() || s.now().Before(r.expires))
}

// Get returns the entry for key; ok is false when it is absent or expired.
func (s *Store) Get(key []KeyPart) (Entry, bool, error) {
	k, err := EncodeKey(key)
	if err != nil {
		return Entry{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.data[string(k)]
	if !s.live(r) {
		return Entry{}, false, nil
	}
	return Entry{Key: r.key, Value: r.value, Versionstamp: versionstamp(r.version)}, true, nil
}

// Selector picks a key range for List: everything under Prefix, narrowed
// by Start (inclusive) and End (exclusive) when given.
type Selector struct {
	Prefix     []KeyPart
	Start, End []KeyPart
}

// ListOptions bound a List.
type ListOptions struct {
	Limit   int // 0 = unlimited
	Reverse bool
	// Cursor resumes after the entry with this encoded key.
	Cursor []byte
}

// List returns entries in key order (or reverse) with their encoded keys,
// which callers use as cursors.
func (s *Store) List(sel Selector, opts ListOptions) ([]Entry, [][]byte, error) {
	var prefix, start, end []byte
	var err error
	if sel.Prefix != nil {
		if prefix, err = EncodeKey(sel.Prefix); err != nil {
			return nil, nil, err
		}
	}
	if sel.Start != nil {
		if start, err = EncodeKey(sel.Start); err != nil {
			return nil, nil, err
		}
	}
	if sel.End != nil {
		if end, err = EncodeKey(sel.End); err != nil {
			return nil, nil, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k, r := range s.data {
		kb := []byte(k)
		if !s.live(r) {
			delete(s.data, k) // lazy expiry
			continue
		}
		if prefix != nil && (!bytes.HasPrefix(kb, prefix) || bytes.Equal(kb, prefix)) {
			continue
		}
		if start != nil && bytes.Compare(kb, start) < 0 {
			continue
		}
		if end != nil && bytes.Compare(kb, end) >= 0 {
			continue
		}
		if opts.Cursor != nil {
			c := bytes.Compare(kb, opts.Cursor)
			if (!opts.Reverse && c <= 0) || (opts.Reverse && c >= 0) {
				continue
			}
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if opts.Reverse {
		for i, j := 0, len(keys)-1; i < j; i, j = i+1, j-1 {
			keys[i], keys[j] = keys[j], keys[i]
		}
	}
	if opts.Limit > 0 && len(keys) > opts.Limit {
		keys = keys[:opts.Limit]
	}
	entries := make([]Entry, len(keys))
	encoded := make([][]byte, len(keys))
	for i, k := range keys {
		r := s.data[k]
		entries[i] = Entry{Key: r.key, Value: r.value, Versionstamp: versionstamp(r.version)}
		encoded[i] = []byte(k)
	}
	return entries, encoded, nil
}

// Check asserts that key currently has the given versionstamp ("" means
// the key must be absent).
type Check struct {
	Key          []KeyPart
	Versionstamp string
}

// MutationKind is the kind of an atomic mutation.
type MutationKind int

const (
	MutSet MutationKind = iota
	MutDelete
	MutSum
	MutMin
	MutMax
)

// Mutation is one write in an atomic operation.
type Mutation struct {
	Kind     MutationKind
	Key      []KeyPart
	Value    Value         // for set, sum, min, max
	ExpireIn time.Duration // for set; 0 = never
}

// ErrTypeMismatch reports sum/min/max on a non-KvU64 value.
var ErrTypeMismatch = errors.New("kv: sum/min/max require KvU64 values")

// Commit applies mutations atomically if every check holds. ok is false
// when a check failed; nothing is written then.
func (s *Store) Commit(checks []Check, muts []Mutation) (ok bool, stamp string, err error) {
	type encMut struct {
		Mutation
		k string
	}
	enc := make([]encMut, len(muts))
	for i, m := range muts {
		k, err := EncodeKey(m.Key)
		if err != nil {
			return false, "", err
		}
		if (m.Kind == MutSum || m.Kind == MutMin || m.Kind == MutMax) && !m.Value.IsU64 {
			return false, "", ErrTypeMismatch
		}
		enc[i] = encMut{m, string(k)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range checks {
		k, err := EncodeKey(c.Key)
		if err != nil {
			return false, "", err
		}
		r := s.data[string(k)]
		current := ""
		if s.live(r) {
			current = versionstamp(r.version)
		}
		if current != c.Versionstamp {
			return false, "", nil
		}
	}
	// Validate numeric mutations against current values before writing.
	for _, m := range enc {
		if m.Kind == MutSum || m.Kind == MutMin || m.Kind == MutMax {
			if r := s.data[m.k]; s.live(r) && !r.value.IsU64 {
				return false, "", ErrTypeMismatch
			}
		}
	}
	s.version++
	v := s.version
	now := s.now()
	for _, m := range enc {
		switch m.Kind {
		case MutSet:
			r := &record{key: m.Key, value: m.Value, version: v}
			if m.ExpireIn > 0 {
				r.expires = now.Add(m.ExpireIn)
			}
			s.data[m.k] = r
		case MutDelete:
			delete(s.data, m.k)
		case MutSum, MutMin, MutMax:
			r := s.data[m.k]
			n := m.Value.U64
			if s.live(r) {
				cur := r.value.U64
				switch m.Kind {
				case MutSum:
					n = cur + n // wraps modulo 2^64, as in Deno
				case MutMin:
					n = min(cur, n)
				case MutMax:
					n = max(cur, n)
				}
			}
			s.data[m.k] = &record{key: m.Key, value: Value{U64: n, IsU64: true}, version: v}
		}
	}
	return true, versionstamp(v), nil
}
