package kv

import (
	"bytes"
	"math"
	"math/big"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestKeyOrdering(t *testing.T) {
	keys := [][]KeyPart{
		{[]byte{0}}, {[]byte{0, 1}}, {[]byte{1}},
		{""}, {"a"}, {"a\x00"}, {"a\x00b"}, {"b"},
		{math.Inf(-1)}, {-2.5}, {-0.0}, {0.0}, {1.0}, {math.Inf(1)},
		{big.NewInt(-300)}, {big.NewInt(-1)}, {big.NewInt(0)}, {big.NewInt(1)}, {big.NewInt(300)},
		{false}, {true},
		{"user", 1.0}, {"user", 2.0}, {"user", "a"},
	}
	enc := make([][]byte, len(keys))
	for i, k := range keys {
		b, err := EncodeKey(k)
		if err != nil {
			t.Fatal(err)
		}
		enc[i] = b
	}
	// The single-part keys must already be in order.
	for i := 1; i < 21; i++ {
		if bytes.Compare(enc[i-1], enc[i]) > 0 {
			t.Errorf("key %v sorts after %v", keys[i-1], keys[i])
		}
	}
	// Prefix property: ["user"] is a byte prefix of ["user", x].
	p, _ := EncodeKey([]KeyPart{"user"})
	for _, k := range enc[21:] {
		if !bytes.HasPrefix(k, p) {
			t.Errorf("%x lacks prefix %x", k, p)
		}
	}
	sorted := append([][]byte(nil), enc[21:]...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	// Deno orders string parts before number parts: ["user","a"] < ["user",1] < ["user",2].
	if !bytes.Equal(sorted[0], enc[23]) || !bytes.Equal(sorted[1], enc[21]) || !bytes.Equal(sorted[2], enc[22]) {
		t.Error("compound keys out of order")
	}
}

func set(t *testing.T, s *Store, key []KeyPart, data string, ttl time.Duration) string {
	t.Helper()
	ok, stamp, err := s.Commit(nil, []Mutation{{Kind: MutSet, Key: key, Value: Value{Data: []byte(data)}, ExpireIn: ttl}})
	if err != nil || !ok {
		t.Fatalf("set: %v %v", ok, err)
	}
	return stamp
}

func TestGetSetDeleteListExpiry(t *testing.T) {
	s := NewMemory()
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	set(t, s, []KeyPart{"users", "ann"}, "A", 0)
	set(t, s, []KeyPart{"users", "bob"}, "B", 0)
	set(t, s, []KeyPart{"users", "cid"}, "C", time.Second)
	set(t, s, []KeyPart{"other"}, "X", 0)

	e, ok, _ := s.Get([]KeyPart{"users", "bob"})
	if !ok || string(e.Value.Data) != "B" || len(e.Versionstamp) != 20 {
		t.Fatalf("get = %+v %v", e, ok)
	}
	list := func(sel Selector, opts ListOptions) string {
		es, _, err := s.List(sel, opts)
		if err != nil {
			t.Fatal(err)
		}
		var out string
		for _, e := range es {
			out += string(e.Value.Data)
		}
		return out
	}
	if got := list(Selector{Prefix: []KeyPart{"users"}}, ListOptions{}); got != "ABC" {
		t.Errorf("list = %q", got)
	}
	if got := list(Selector{Prefix: []KeyPart{"users"}}, ListOptions{Reverse: true, Limit: 2}); got != "CB" {
		t.Errorf("reverse list = %q", got)
	}
	if got := list(Selector{Prefix: []KeyPart{"users"}, Start: []KeyPart{"users", "bob"}}, ListOptions{}); got != "BC" {
		t.Errorf("start list = %q", got)
	}
	_, cursors, _ := s.List(Selector{Prefix: []KeyPart{"users"}}, ListOptions{Limit: 1})
	if got := list(Selector{Prefix: []KeyPart{"users"}}, ListOptions{Cursor: cursors[0]}); got != "BC" {
		t.Errorf("cursor list = %q", got)
	}
	now = now.Add(2 * time.Second)
	if _, ok, _ := s.Get([]KeyPart{"users", "cid"}); ok {
		t.Error("expired entry still readable")
	}
	if got := list(Selector{Prefix: []KeyPart{"users"}}, ListOptions{}); got != "AB" {
		t.Errorf("list after expiry = %q", got)
	}
	s.Commit(nil, []Mutation{{Kind: MutDelete, Key: []KeyPart{"users", "ann"}}})
	if _, ok, _ := s.Get([]KeyPart{"users", "ann"}); ok {
		t.Error("deleted entry still readable")
	}
}

func TestAtomicChecksAndSum(t *testing.T) {
	s := NewMemory()
	key := []KeyPart{"counter"}
	ok, _, _ := s.Commit([]Check{{Key: key, Versionstamp: ""}}, []Mutation{{Kind: MutSum, Key: key, Value: Value{U64: 5, IsU64: true}}})
	if !ok {
		t.Fatal("check on absent key failed")
	}
	e, _, _ := s.Get(key)
	if ok, _, _ := s.Commit([]Check{{Key: key, Versionstamp: ""}}, []Mutation{{Kind: MutDelete, Key: key}}); ok {
		t.Error("check for absence passed on a present key")
	}
	if ok, _, _ := s.Commit([]Check{{Key: key, Versionstamp: e.Versionstamp}}, []Mutation{{Kind: MutMax, Key: key, Value: Value{U64: 3, IsU64: true}}}); !ok {
		t.Error("check with the current versionstamp failed")
	}
	if e, _, _ := s.Get(key); e.Value.U64 != 5 {
		t.Errorf("max(5, 3) = %d", e.Value.U64)
	}
	set(t, s, []KeyPart{"text"}, "x", 0)
	if _, _, err := s.Commit(nil, []Mutation{{Kind: MutSum, Key: []KeyPart{"text"}, Value: Value{U64: 1, IsU64: true}}}); err != ErrTypeMismatch {
		t.Errorf("sum on bytes: err = %v", err)
	}

	// Concurrent sums never lose updates.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Commit(nil, []Mutation{{Kind: MutSum, Key: []KeyPart{"hits"}, Value: Value{U64: 1, IsU64: true}}})
		}()
	}
	wg.Wait()
	if e, _, _ := s.Get([]KeyPart{"hits"}); e.Value.U64 != 100 {
		t.Errorf("hits = %d", e.Value.U64)
	}
}
