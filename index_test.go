package main

import (
	"runtime"
	"runtime/debug"
	"sync"
	"testing"
	"time"
)

var (
	realIndexOnce sync.Once
	realIndex     *searchIndex
	realIdxStore  *hadithStore
	realIndexErr  error
)

// realDataIndex builds store + index from resource/ once per test binary.
func realDataIndex(t *testing.T) (*hadithStore, *searchIndex) {
	t.Helper()
	if testing.Short() {
		t.Skip("real-data test skipped in -short mode")
	}
	infos := loadListForTest(t)
	realIndexOnce.Do(func() {
		t0 := time.Now()
		ib := newIndexBuilder()
		realIdxStore, realIndexErr = buildStore("resource", infos, ib.add)
		if realIndexErr == nil {
			realIndex = ib.finish()
		}
		runtime.GC()
		debug.FreeOSMemory()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		t.Logf("built in %v, HeapInuse %.1f MB", time.Since(t0), float64(ms.HeapInuse)/(1<<20))
	})
	if realIndexErr != nil {
		t.Fatal(realIndexErr)
	}
	return realIdxStore, realIndex
}

func TestIndexRealData(t *testing.T) {
	s, ix := realDataIndex(t)
	if ix.numDocs != len(s.docNum) {
		t.Fatalf("numDocs %d vs store %d", ix.numDocs, len(s.docNum))
	}
	shalat := ix.dfOf("shalat")
	if shalat < 6900 || shalat > 7100 {
		t.Errorf("df(shalat) = %d, want ~6986", shalat)
	}
	if d := ix.dfOf("الصلاه"); d < 2000 {
		t.Errorf("df(الصلاه) = %d, want >= 2000", d)
	}
	// Postings ascending.
	id, _ := ix.lookup("niat")
	docs, _ := ix.postings(id)
	for i := 1; i < len(docs); i++ {
		if docs[i] <= docs[i-1] {
			t.Fatal("postings not ascending")
		}
	}
	for _, w := range []string{"menikahi", "pernikahan", "menikah"} {
		if st := ix.stemOf(w); st != "nikah" {
			t.Errorf("stem(%s) = %s", w, st)
		}
	}
	if st := ix.stemOf("kesabaran"); st != "sabar" {
		t.Errorf("stem(kesabaran) = %s", st)
	}
	t.Logf("df: shalat=%d sholat=%d الصلاه=%d terms=%d postings=%d stemGroups=%d",
		shalat, ix.dfOf("sholat"), ix.dfOf("الصلاه"), len(ix.termList), len(ix.postDocs), len(ix.stemGroups))
}
