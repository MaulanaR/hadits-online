package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestExplainHandler exercises /api/explain against a temp copy of a small
// collection and a fake Gemini server: no real API call, no change to resource/.
func TestExplainHandler(t *testing.T) {
	dir := t.TempDir()
	recs := []Collection{
		{Number: 1, Arab: "a1", ID: "satu", Explanation: "sudah ada"},
		{Number: 2, Arab: "a2", ID: "dua"},
		{Number: 5, Arab: "a5", ID: "lima"},
	}
	b, _ := json.MarshalIndent(recs, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "tst.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := buildStore(dir, []CollectionInfo{{Name: "Test", Slug: "tst", Total: 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	release := make(chan struct{})
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("api key header missing")
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"# Penjelasan\nhasil AI"}]}}]}`))
	}))
	defer fake.Close()

	oldStore, oldURL, oldKey, oldLim := store, geminiURL, geminiApiKey, explainLimiter
	store, geminiURL, geminiApiKey = s, fake.URL, "test-key"
	explainLimiter = newRateLimiter(5, 10*time.Minute, 60, time.Hour)
	defer func() { store, geminiURL, geminiApiKey, explainLimiter = oldStore, oldURL, oldKey, oldLim }()

	do := func(q string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		explainHandler(rr, httptest.NewRequest("GET", "/api/explain?"+q, nil))
		return rr
	}

	for q, want := range map[string]int{
		"slug=../go&number=1": 404,
		"slug=tst&number=abc": 400,
		"slug=tst":            400,
		"slug=tst&number=3":   404,
	} {
		if rr := do(q); rr.Code != want {
			t.Errorf("%s: got %d want %d", q, rr.Code, want)
		}
	}

	rr := do("slug=tst&number=1")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "sudah ada") || calls.Load() != 0 {
		t.Fatalf("cached: %d %s calls=%d", rr.Code, rr.Body.String(), calls.Load())
	}
	if cc := rr.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age=86400") {
		t.Errorf("cached Cache-Control = %q", cc)
	}

	// 4 concurrent requests for the same uncached hadith -> one Gemini call.
	var wg sync.WaitGroup
	codes := make([]int, 4)
	bodies := make([]string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := do("slug=tst&number=2")
			codes[i], bodies[i] = r.Code, r.Body.String()
		}(i)
	}
	for explainFlight.InFlight("tst/2") == false {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("gemini calls = %d, want 1", calls.Load())
	}
	for i := range codes {
		if codes[i] != 200 || !strings.Contains(bodies[i], "hasil AI") {
			t.Fatalf("req %d: %d %s", i, codes[i], bodies[i])
		}
	}

	// Persisted in MarshalIndent format and visible through the store.
	raw, _ := os.ReadFile(filepath.Join(dir, "tst.json"))
	var back []Collection
	if err := json.Unmarshal(raw, &back); err != nil || back[1].Explanation != "# Penjelasan\nhasil AI" {
		t.Fatalf("file not updated: %v %+v", err, back)
	}
	if !strings.HasPrefix(string(raw), "[\n  {\n    \"number\": 1,") {
		t.Fatalf("unexpected file format: %.40q", raw)
	}
	c, _ := getBySlug("tst")
	if h, _ := c.read(1); h.Explanation == "" || c.generation() != 1 {
		t.Fatalf("store not refreshed: %+v gen=%d", h, c.generation())
	}
	if _, err := os.Stat(filepath.Join(dir, ".tst.json.tmp")); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
	if rr := do("slug=tst&number=2"); rr.Code != 200 || calls.Load() != 1 {
		t.Fatalf("second fetch should be cached: %d calls=%d", rr.Code, calls.Load())
	}

	// Rate limit: per-IP bucket of 5 for uncached hadith.
	explainLimiter = newRateLimiter(1, 10*time.Minute, 60, time.Hour)
	geminiURL = "http://127.0.0.1:1/unreachable" // first call fails fast (502), no persistence
	if rr := do("slug=tst&number=5"); rr.Code != http.StatusBadGateway {
		t.Fatalf("unreachable gemini: %d", rr.Code)
	}
	if rr := do("slug=tst&number=5"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("rate limit: %d %s", rr.Code, rr.Body.String())
	}
}

func TestRateLimiter(t *testing.T) {
	l := newRateLimiter(2, time.Minute, 3, time.Hour)
	now := time.Now()
	if !l.allow("a", now) || !l.allow("a", now) || l.allow("a", now) {
		t.Fatal("per-ip cap")
	}
	if !l.allow("b", now) || l.allow("c", now) {
		t.Fatal("global cap")
	}
	if !l.allow("a", now.Add(time.Hour)) {
		t.Fatal("refill")
	}
}

func TestClientIP(t *testing.T) {
	mk := func(remote string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("GET", "/api/explain", nil)
		r.RemoteAddr = remote
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		remote string
		hdr    map[string]string
		want   string
	}{
		{"127.0.0.1:5555", map[string]string{"X-Real-IP": "203.0.113.9"}, "203.0.113.9"},
		{"[::1]:5555", map[string]string{"X-Real-IP": "2001:db8::7"}, "2001:db8::7"},
		{"127.0.0.1:5555", map[string]string{"X-Real-IP": "not-an-ip"}, "127.0.0.1"},
		{"127.0.0.1:5555", nil, "127.0.0.1"},
		{"198.51.100.4:4444", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "198.51.100.4"},
		{"198.51.100.4:4444", map[string]string{"X-Real-IP": "1.2.3.4"}, "198.51.100.4"},
		{"127.0.0.1:5555", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "127.0.0.1"},
	}
	for _, c := range cases {
		if got := clientIP(mk(c.remote, c.hdr)); got != c.want {
			t.Errorf("clientIP(%s, %v) = %s, want %s", c.remote, c.hdr, got, c.want)
		}
	}
}
