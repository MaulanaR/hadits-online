package main

import (
	"container/list"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// flightGroup: a tiny in-house singleflight. Concurrent callers with the same
// key share one execution of fn.
// ---------------------------------------------------------------------------

type flightCall[V any] struct {
	wg  sync.WaitGroup
	val V
	err error
}

type flightGroup[V any] struct {
	mu sync.Mutex
	m  map[string]*flightCall[V]
}

// Do runs fn once per key at a time; shared reports whether the result was
// produced by another caller.
func (g *flightGroup[V]) Do(key string, fn func() (V, error)) (v V, err error, shared bool) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*flightCall[V])
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err, true
	}
	c := new(flightCall[V])
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
		c.wg.Done()
	}()
	c.val, c.err = fn()
	return c.val, c.err, false
}

// InFlight reports whether a call for key is currently running.
func (g *flightGroup[V]) InFlight(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.m[key]
	return ok
}

// ---------------------------------------------------------------------------
// Token-bucket rate limiter (per client IP plus one global bucket).
// ---------------------------------------------------------------------------

type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) take(now time.Time, capacity, perSec float64) bool {
	if b.last.IsZero() {
		b.tokens = capacity
	} else {
		b.tokens += now.Sub(b.last).Seconds() * perSec
		if b.tokens > capacity {
			b.tokens = capacity
		}
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type rateLimiter struct {
	mu        sync.Mutex
	perIP     map[string]*tokenBucket
	ipCap     float64
	ipRate    float64 // tokens per second
	global    tokenBucket
	globalCap float64
	gRate     float64
}

func newRateLimiter(ipCap int, ipWindow time.Duration, globalCap int, globalWindow time.Duration) *rateLimiter {
	return &rateLimiter{
		perIP:     make(map[string]*tokenBucket),
		ipCap:     float64(ipCap),
		ipRate:    float64(ipCap) / ipWindow.Seconds(),
		globalCap: float64(globalCap),
		gRate:     float64(globalCap) / globalWindow.Seconds(),
	}
}

// allow consumes one token from the IP bucket and the global bucket.
func (l *rateLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.perIP[ip]
	if b == nil {
		b = &tokenBucket{}
		l.perIP[ip] = b
	}
	// Check both before consuming either, so a global refusal doesn't burn the IP token.
	ipProbe, gProbe := *b, l.global
	if !ipProbe.take(now, l.ipCap, l.ipRate) || !gProbe.take(now, l.globalCap, l.gRate) {
		return false
	}
	*b, l.global = ipProbe, gProbe
	return true
}

// sweep drops buckets that have refilled completely (idle clients).
func (l *rateLimiter) sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for ip, b := range l.perIP {
		if b.tokens+now.Sub(b.last).Seconds()*l.ipRate >= l.ipCap {
			delete(l.perIP, ip)
		}
	}
}

func (l *rateLimiter) startJanitor(every time.Duration) {
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for now := range t.C {
			l.sweep(now)
		}
	}()
}

// clientIP returns the visitor address used for rate limiting. X-Real-IP is
// trusted only when the connection comes from loopback (the local nginx sets
// it from CF-Connecting-IP); direct connections always use RemoteAddr, and
// X-Forwarded-For is ignored because clients can spoof it.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if real := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); real != nil {
			return real.String()
		}
	}
	return host
}

// ---------------------------------------------------------------------------
// Search result cache: LRU bounded by bytes (ranked doc ids + scores only;
// snippets are built per page). Misses are deduplicated with flightGroup so a
// burst of identical queries computes once.
// ---------------------------------------------------------------------------

type lruEntry struct {
	key  string
	val  *searchOutcome
	size int
}

type resultCache struct {
	mu       sync.Mutex
	ll       *list.List
	m        map[string]*list.Element
	bytes    int
	maxBytes int
	maxEntry int
	hits     atomic.Uint64
	misses   atomic.Uint64
	flight   flightGroup[*searchOutcome]
}

func newResultCache(maxBytes int) *resultCache {
	return &resultCache{
		ll:       list.New(),
		m:        make(map[string]*list.Element),
		maxBytes: maxBytes,
		maxEntry: 4 << 20,
	}
}

func outcomeSize(o *searchOutcome) int { return len(o.ids)*8 + 128 }

func (c *resultCache) get(key string) (*searchOutcome, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*lruEntry).val, true
	}
	return nil, false
}

func (c *resultCache) put(key string, v *searchOutcome) {
	size := outcomeSize(v) + len(key)
	if size > c.maxEntry || size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		e := el.Value.(*lruEntry)
		c.bytes += size - e.size
		e.val, e.size = v, size
		c.ll.MoveToFront(el)
	} else {
		c.m[key] = c.ll.PushFront(&lruEntry{key: key, val: v, size: size})
		c.bytes += size
	}
	for c.bytes > c.maxBytes {
		el := c.ll.Back()
		if el == nil {
			break
		}
		e := el.Value.(*lruEntry)
		c.ll.Remove(el)
		delete(c.m, e.key)
		c.bytes -= e.size
	}
}

// getOrCompute returns the cached value or computes it once for concurrent callers.
func (c *resultCache) getOrCompute(key string, compute func() *searchOutcome) (v *searchOutcome, cached bool) {
	if v, ok := c.get(key); ok {
		c.hits.Add(1)
		return v, true
	}
	v, _, shared := c.flight.Do(key, func() (*searchOutcome, error) {
		if v, ok := c.get(key); ok { // filled while we were waiting for the lock
			return v, nil
		}
		v := compute()
		c.put(key, v)
		return v, nil
	})
	if shared {
		c.hits.Add(1)
	} else {
		c.misses.Add(1)
	}
	return v, shared
}

func (c *resultCache) stats() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len(), c.bytes
}

func (c *resultCache) startStatsLogger(every time.Duration) {
	go func() {
		var lastH, lastM uint64
		for range time.Tick(every) {
			h, m := c.hits.Load(), c.misses.Load()
			if h == lastH && m == lastM {
				continue
			}
			n, b := c.stats()
			log.Printf("search cache: %d hits, %d misses (total), %d entries, %.1f MB", h, m, n, float64(b)/(1<<20))
			lastH, lastM = h, m
		}
	}()
}

// searchCacheBytes reads SEARCH_CACHE_MB (default 16).
func searchCacheBytes() int {
	mb := 16
	if v, err := strconv.Atoi(os.Getenv("SEARCH_CACHE_MB")); err == nil && v >= 0 {
		mb = v
	}
	return mb << 20
}

var searchCache = newResultCache(searchCacheBytes())

// searchCacheKey identifies a query outcome. dataGen is included so any data
// rewrite invalidates old entries (texts don't change on explain today, but
// the hook keeps the cache safe if that ever changes).
func searchCacheKey(q *parsedQuery, f SearchFilters) string {
	cols := append([]string(nil), f.Collections...)
	slices.Sort(cols)
	var b strings.Builder
	fmt.Fprintf(&b, "%d|%s|%s|%s|%d|%d|", dataGen.Load(), f.Language, f.SortBy, strings.Join(cols, ","), f.NumberRange.Min, f.NumberRange.Max)
	b.WriteString(strings.Join(q.seq, " "))
	for _, ph := range q.phrases {
		b.WriteString("|\"")
		b.WriteString(strings.Join(ph, " "))
	}
	return b.String()
}
