package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// The record store keeps only byte offsets of every hadith inside
// resource/{slug}.json. Pages read exactly the records they show with a single
// ReadAt, so no request ever unmarshals a whole 2–13 MB collection file.

// collStore describes one collection file.
type collStore struct {
	Info CollectionInfo
	idx  uint8  // position in list.json
	base uint32 // global doc id of the first record
	path string

	mu      sync.RWMutex // RLock for reads, Lock while the file is rewritten
	numbers []int32
	offsets []int64
	sizes   []uint32
	mtime   time.Time
	size    int64
	gen     uint64 // bumped on every rewrite

	lastCheck atomic.Int64 // unix nanos of the last freshness check
}

type hadithStore struct {
	colls  []*collStore
	bySlug map[string]*collStore
	// Global doc id -> collection index / hadith number. Doc ids are assigned in
	// list.json order, then file order, so every collection is a contiguous range.
	docColl []uint8
	docNum  []int32
}

var (
	store   *hadithStore
	dataGen atomic.Uint64 // bumped whenever any collection file changes
)

// scanRecord is the minimal shape needed while scanning.
type scanRecord struct {
	Number int    `json:"number"`
	Arab   string `json:"arab"`
	ID     string `json:"id"`
}

// recordFunc receives each record while a file is scanned (used by the indexer).
type recordFunc func(rec *scanRecord)

type scanResult struct {
	numbers []int32
	offsets []int64
	sizes   []uint32
}

// scanFile stream-parses a JSON array file and records the exact byte range of
// every element. RawMessage holds the exact value bytes, so this works with any
// indentation or line ending.
func scanFile(path string, fn recordFunc) (*scanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, fmt.Errorf("%s: expected JSON array", path)
	}
	res := &scanResult{}
	var raw json.RawMessage
	var rec scanRecord
	for dec.More() {
		raw = raw[:0]
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		end := dec.InputOffset()
		start := end - int64(len(raw))
		rec = scanRecord{}
		if err := json.Unmarshal(raw, &rec); err != nil {
			return nil, fmt.Errorf("%s @%d: %w", path, start, err)
		}
		if n := len(res.numbers); n > 0 && int32(rec.Number) <= res.numbers[n-1] {
			return nil, fmt.Errorf("%s: hadith numbers not strictly ascending at %d", path, rec.Number)
		}
		res.numbers = append(res.numbers, int32(rec.Number))
		res.offsets = append(res.offsets, start)
		res.sizes = append(res.sizes, uint32(len(raw)))
		if fn != nil {
			fn(&rec)
		}
	}
	return res, nil
}

// buildStore scans every collection in list order. fn (optional) receives each
// record together with its global doc id, in ascending doc-id order.
func buildStore(dir string, infos []CollectionInfo, fn func(docID uint32, rec *scanRecord)) (*hadithStore, error) {
	if len(infos) > 255 {
		return nil, errors.New("too many collections")
	}
	s := &hadithStore{bySlug: make(map[string]*collStore, len(infos))}
	var next uint32
	for i, info := range infos {
		c := &collStore{Info: info, idx: uint8(i), base: next, path: filepath.Join(dir, info.Slug+".json")}
		var cb recordFunc
		if fn != nil {
			cb = func(rec *scanRecord) {
				fn(next, rec)
				next++
			}
		} else {
			cb = func(*scanRecord) { next++ }
		}
		res, err := scanFile(c.path, cb)
		if err != nil {
			return nil, err
		}
		c.numbers, c.offsets, c.sizes = res.numbers, res.offsets, res.sizes
		if st, err := os.Stat(c.path); err == nil {
			c.mtime, c.size = st.ModTime(), st.Size()
		}
		c.lastCheck.Store(time.Now().UnixNano())
		for _, n := range res.numbers {
			s.docColl = append(s.docColl, uint8(i))
			s.docNum = append(s.docNum, n)
		}
		if info.Total != len(res.numbers) {
			log.Printf("note: list.json says %s has %d hadith, file has %d", info.Slug, info.Total, len(res.numbers))
		}
		s.colls = append(s.colls, c)
		s.bySlug[info.Slug] = c
	}
	return s, nil
}

// initStore builds the global store (and feeds the search index) at startup.
func initStore() {
	t0 := time.Now()
	ib := newIndexBuilder()
	s, err := buildStore("resource", data.Info, ib.add)
	if err != nil {
		log.Fatal("Error building record store: ", err)
	}
	store = s
	searchIdx = ib.finish()
	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Printf("Store + index ready: %d hadith in %v (heap in use %.1f MB, heap alloc %.1f MB, sys %.1f MB)",
		len(s.docNum), time.Since(t0).Round(time.Millisecond),
		float64(ms.HeapInuse)/(1<<20), float64(ms.HeapAlloc)/(1<<20), float64(ms.Sys)/(1<<20))
}

// getBySlug is the only way handlers resolve a slug. Unknown slug => false.
func getBySlug(slug string) (*collStore, bool) {
	if store == nil {
		return nil, false
	}
	c, ok := store.bySlug[slug]
	return c, ok
}

func (c *collStore) count() int {
	c.refreshIfChanged()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.numbers)
}

// indexOf finds the position of a hadith number (binary search).
func (c *collStore) indexOf(number int) (int, bool) {
	c.refreshIfChanged()
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.indexOfLocked(number)
}

func (c *collStore) indexOfLocked(number int) (int, bool) {
	i := sort.Search(len(c.numbers), func(i int) bool { return int(c.numbers[i]) >= number })
	if i < len(c.numbers) && int(c.numbers[i]) == number {
		return i, true
	}
	return i, false
}

// numberAt returns the hadith number at position i.
func (c *collStore) numberAt(i int) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int(c.numbers[i])
}

// numbersCopy returns a snapshot of all hadith numbers.
func (c *collStore) numbersCopy() []int32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]int32(nil), c.numbers...)
}

func (c *collStore) modTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.mtime
}

func (c *collStore) generation() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gen
}

var readBufPool = sync.Pool{New: func() any { b := make([]byte, 0, 64<<10); return &b }}

// read returns the record at position i.
func (c *collStore) read(i int) (Collection, error) {
	recs, err := c.readRange(i, i+1)
	if err != nil {
		return Collection{}, err
	}
	return recs[0], nil
}

// readRange returns records [i, j) with a single ReadAt. The file is opened per
// call so a rewrite (rename) is never blocked by a long-lived handle on Windows.
func (c *collStore) readRange(i, j int) ([]Collection, error) {
	c.refreshIfChanged()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if i < 0 || j > len(c.numbers) || i >= j {
		return nil, fmt.Errorf("range [%d,%d) out of bounds (%d)", i, j, len(c.numbers))
	}
	start := c.offsets[i]
	end := c.offsets[j-1] + int64(c.sizes[j-1])
	n := int(end - start)

	bp := readBufPool.Get().(*[]byte)
	defer func() {
		if cap(*bp) <= 1<<20 {
			readBufPool.Put(bp)
		}
	}()
	if cap(*bp) < n {
		*bp = make([]byte, n)
	}
	buf := (*bp)[:n]

	f, err := os.Open(c.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return nil, err
	}
	out := make([]Collection, j-i)
	for k := i; k < j; k++ {
		off := c.offsets[k] - start
		if err := json.Unmarshal(buf[off:off+int64(c.sizes[k])], &out[k-i]); err != nil {
			return nil, fmt.Errorf("%s record %d: %w", c.path, k, err)
		}
	}
	return out, nil
}

// setExplanation stores an AI explanation in resource/{slug}.json, keeping the
// historical file format (json.MarshalIndent with two spaces). The write is
// atomic (temp file + fsync + rename) and happens under the collection's write
// lock, so readers never see a partial file and two writers can't lose updates.
// Offsets are re-scanned afterwards; texts are unchanged so the search index
// stays valid.
func (c *collStore) setExplanation(number int, explanation string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	raw, err := os.ReadFile(c.path)
	if err != nil {
		return err
	}
	// Records are kept as raw JSON so every other record (and any field this
	// program doesn't know about) is written back unchanged; only the target
	// record is re-encoded. MarshalIndent re-indents the raw records the same
	// way the original whole-file MarshalIndent did.
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return err
	}
	raw = nil
	found := false
	for i, e := range elems {
		var peek struct {
			Number int `json:"number"`
		}
		if json.Unmarshal(e, &peek) != nil || peek.Number != number {
			continue
		}
		updated, err := withExplanation(e, explanation)
		if err != nil {
			return err
		}
		elems[i] = updated
		found = true
		break
	}
	if !found {
		return fmt.Errorf("hadith %s/%d not found", c.Info.Slug, number)
	}
	out, err := json.MarshalIndent(elems, "", "  ")
	if err != nil {
		return err
	}
	elems = nil

	tmp := filepath.Join(filepath.Dir(c.path), "."+filepath.Base(c.path)+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(out); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return err
	}

	res, err := scanFile(c.path, nil)
	if err != nil {
		return fmt.Errorf("rescan after write: %w", err)
	}
	if len(res.numbers) != len(c.numbers) {
		return fmt.Errorf("rescan: record count changed %d -> %d", len(c.numbers), len(res.numbers))
	}
	c.numbers, c.offsets, c.sizes = res.numbers, res.offsets, res.sizes
	if st, err := os.Stat(c.path); err == nil {
		c.mtime, c.size = st.ModTime(), st.Size()
	}
	c.gen++
	dataGen.Add(1)
	return nil
}

// freshnessInterval limits how often a collection file is stat()ed.
var freshnessInterval = time.Second

// refreshIfChanged re-scans the byte offsets when the file was rewritten by
// someone else (another process, a deploy copying new data, a manual edit):
// it compares mtime and size at most once per freshnessInterval. Rewrites
// done by setExplanation already update the offsets themselves.
func (c *collStore) refreshIfChanged() {
	now := time.Now().UnixNano()
	last := c.lastCheck.Load()
	if now-last < int64(freshnessInterval) || !c.lastCheck.CompareAndSwap(last, now) {
		return
	}
	st, err := os.Stat(c.path)
	if err != nil {
		return
	}
	c.mu.RLock()
	same := st.ModTime().Equal(c.mtime) && st.Size() == c.size
	c.mu.RUnlock()
	if same {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if st2, err := os.Stat(c.path); err == nil && st2.ModTime().Equal(c.mtime) && st2.Size() == c.size {
		return // another goroutine refreshed it
	}
	res, err := scanFile(c.path, nil)
	if err != nil {
		log.Printf("store: %s changed on disk but could not be re-scanned: %v", c.path, err)
		return
	}
	if !slices.Equal(res.numbers, c.numbers) {
		// Pages keep working (lookups are by hadith number), but the search
		// index was built from the old texts: a restart rebuilds it.
		log.Printf("store: %s changed on disk and its hadith numbers changed (%d -> %d records); restart to rebuild the search index",
			c.path, len(c.numbers), len(res.numbers))
	}
	c.numbers, c.offsets, c.sizes = res.numbers, res.offsets, res.sizes
	if st2, err := os.Stat(c.path); err == nil {
		c.mtime, c.size = st2.ModTime(), st2.Size()
	}
	c.gen++
	dataGen.Add(1)
	log.Printf("store: re-scanned %s after an external change", c.path)
}

// readDoc reads a record by global doc id.
func (s *hadithStore) readDoc(doc uint32) (*collStore, Collection, error) {
	c := s.colls[s.docColl[doc]]
	i, ok := c.indexOf(int(s.docNum[doc]))
	if !ok {
		return c, Collection{}, fmt.Errorf("%s/%d no longer exists", c.Info.Slug, s.docNum[doc])
	}
	h, err := c.read(i)
	return c, h, err
}

// withExplanation re-encodes one record with the explanation set, keeping the
// usual key order (number, arab, id, explanation) followed by any extra keys.
func withExplanation(rec json.RawMessage, explanation string) (json.RawMessage, error) {
	var h Collection
	if err := json.Unmarshal(rec, &h); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(rec, &all); err != nil {
		return nil, err
	}
	h.Explanation = explanation
	base, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	var extra []string
	for k := range all {
		switch k {
		case "number", "arab", "id", "explanation":
		default:
			extra = append(extra, k)
		}
	}
	if len(extra) == 0 {
		return base, nil
	}
	sort.Strings(extra)
	buf := append([]byte(nil), base[:len(base)-1]...) // drop the closing brace
	for _, k := range extra {
		kb, _ := json.Marshal(k)
		buf = append(buf, ',')
		buf = append(buf, kb...)
		buf = append(buf, ':')
		buf = append(buf, all[k]...)
	}
	return append(buf, '}'), nil
}
