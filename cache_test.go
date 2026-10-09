package main

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResultCacheStampedeAndLRU(t *testing.T) {
	c := newResultCache(1 << 20)
	var computes atomic.Int32
	compute := func() *searchOutcome {
		computes.Add(1)
		time.Sleep(30 * time.Millisecond)
		return &searchOutcome{ids: []uint32{1, 2, 3}, scores: []float32{3, 2, 1}}
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, _ := c.getOrCompute("k", compute); len(v.ids) != 3 {
				t.Error("bad value")
			}
		}()
	}
	wg.Wait()
	if computes.Load() != 1 {
		t.Fatalf("computed %d times, want 1", computes.Load())
	}
	if _, cached := c.getOrCompute("k", compute); !cached || computes.Load() != 1 {
		t.Fatal("second lookup should hit the cache")
	}

	// Byte bound: entries of ~80 KB in a 1 MB cache -> old ones get evicted.
	big := &searchOutcome{ids: make([]uint32, 10000), scores: make([]float32, 10000)}
	for i := 0; i < 50; i++ {
		c.put("big"+strconv.Itoa(i), big)
	}
	if _, b := c.stats(); b > 1<<20 {
		t.Fatalf("cache holds %d bytes, over budget", b)
	}
	if _, ok := c.get("big0"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := c.get("big49"); !ok {
		t.Fatal("newest entry missing")
	}
}
