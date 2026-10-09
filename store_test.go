package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func loadListForTest(t *testing.T) []CollectionInfo {
	t.Helper()
	b, err := os.ReadFile("resource/list.json")
	if err != nil {
		t.Skip("resource/list.json not available")
	}
	var infos []CollectionInfo
	if err := json.Unmarshal(b, &infos); err != nil {
		t.Fatal(err)
	}
	return infos
}

var (
	realStoreOnce sync.Once
	realStore     *hadithStore
	realStoreErr  error
)

// realDataStore builds the store from the real resource/ directory once per
// test binary (skipped with -short).
func realDataStore(t *testing.T) *hadithStore {
	t.Helper()
	if testing.Short() {
		t.Skip("real-data test skipped in -short mode")
	}
	infos := loadListForTest(t)
	realStoreOnce.Do(func() {
		realStore, realStoreErr = buildStore("resource", infos, nil)
	})
	if realStoreErr != nil {
		t.Fatal(realStoreErr)
	}
	return realStore
}

func TestStoreRealData(t *testing.T) {
	s := realDataStore(t)
	infos := loadListForTest(t)
	total := 0
	for _, info := range infos {
		c, ok := s.bySlug[info.Slug]
		if !ok {
			t.Fatalf("missing collection %s", info.Slug)
		}
		if got := c.count(); got != info.Total {
			t.Errorf("%s: %d records, list.json says %d", info.Slug, got, info.Total)
		}
		total += c.count()
	}
	if total != len(s.docNum) || total != len(s.docColl) {
		t.Fatalf("doc arrays %d/%d, want %d", len(s.docNum), len(s.docColl), total)
	}

	b := s.bySlug["bukhari"]
	h, err := b.read(0)
	if err != nil {
		t.Fatal(err)
	}
	if h.Number != 1 || !strings.HasPrefix(h.ID, "Telah menceritakan kepada kami [Al Humaidi") {
		t.Fatalf("bukhari[0] = %d %.60q", h.Number, h.ID)
	}
	if i, ok := b.indexOf(2977); !ok || b.numberAt(i+1) != 3001 {
		t.Fatalf("gap handling: 2977 -> next %v", b.numberAt(i+1))
	}
	if _, ok := b.indexOf(2978); ok {
		t.Fatal("2978 should not exist")
	}
	last := b.count() - 1
	if b.numberAt(last) != 7008 {
		t.Fatalf("last bukhari = %d", b.numberAt(last))
	}
	recs, err := b.readRange(last-19, last+1)
	if err != nil || len(recs) != 20 || recs[19].Number != 7008 {
		t.Fatalf("readRange tail: %v %d", err, len(recs))
	}
	// Global doc id lookups round-trip.
	c, h2, err := s.readDoc(b.base)
	if err != nil || c.Info.Slug != "bukhari" || h2.Number != 1 {
		t.Fatalf("readDoc: %v %s %d", err, c.Info.Slug, h2.Number)
	}
}

// TestStoreSynthetic checks offsets with CRLF + indentation in a temp dir.
func TestStoreSynthetic(t *testing.T) {
	dir := t.TempDir()
	recs := []Collection{{Number: 1, Arab: "عَرَبِيٌّ", ID: "[A] satu"}, {Number: 3, Arab: "x", ID: "tiga", Explanation: "e"}, {Number: 7, Arab: "y", ID: "tujuh \"q\""}}
	b, _ := json.MarshalIndent(recs, "", "  ")
	b = []byte(strings.ReplaceAll(string(b), "\n", "\r\n"))
	if err := os.WriteFile(filepath.Join(dir, "x.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []int
	s, err := buildStore(dir, []CollectionInfo{{Name: "X", Slug: "x", Total: 3}}, func(doc uint32, r *scanRecord) {
		if int(doc) != len(seen) {
			t.Errorf("doc id %d out of order", doc)
		}
		seen = append(seen, r.Number)
	})
	if err != nil {
		t.Fatal(err)
	}
	c := s.bySlug["x"]
	all, err := c.readRange(0, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := range recs {
		if all[i] != recs[i] {
			t.Fatalf("rec %d = %+v want %+v", i, all[i], recs[i])
		}
	}
	if len(seen) != 3 || seen[2] != 7 {
		t.Fatalf("callback saw %v", seen)
	}
}

// TestStoreExternalRewrite covers production behaviour: records carry extra
// fields (AI explanations, unknown keys) and the file is rewritten at runtime
// in MarshalIndent format, which moves every byte offset.
func TestStoreExternalRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.json")
	write := func(v any) {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	type rec struct {
		Number      int    `json:"number"`
		Arab        string `json:"arab"`
		ID          string `json:"id"`
		Explanation string `json:"explanation,omitempty"`
		Grade       string `json:"grade,omitempty"` // unknown extra field
	}
	write([]rec{{1, "a1", "satu", "", "shahih"}, {2, "a2", "dua", "", ""}, {3, "a3", "tiga", "", ""}})
	s, err := buildStore(dir, []CollectionInfo{{Name: "X", Slug: "x", Total: 3}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := s.bySlug["x"]
	if h, _ := c.read(2); h.ID != "tiga" {
		t.Fatalf("before: %+v", h)
	}

	old := freshnessInterval
	freshnessInterval = 0
	defer func() { freshnessInterval = old }()

	// Another process rewrites the file: a long explanation on record 1 shifts
	// the offsets of every later record.
	long := strings.Repeat("penjelasan panjang ", 500)
	write([]rec{{1, "a1", "satu", long, "shahih"}, {2, "a2", "dua", "", ""}, {3, "a3", "tiga", "e3", ""}})
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(path, future, future)

	gen0 := c.generation()
	h, err := c.read(2)
	if err != nil || h.ID != "tiga" || h.Explanation != "e3" {
		t.Fatalf("after external rewrite: %v %+v", err, h)
	}
	if h1, _ := c.read(0); h1.Explanation != long {
		t.Fatal("record 1 explanation not visible")
	}
	if c.generation() == gen0 {
		t.Fatal("generation should change after a re-scan")
	}
	// Batch reads by doc id stay correct too.
	recs, err := s.readDocs([]uint32{2, 0, 1})
	if err != nil || recs[0].ID != "tiga" || recs[1].ID != "satu" || recs[2].ID != "dua" {
		t.Fatalf("readDocs after rewrite: %v %+v", err, recs)
	}

	// setExplanation (the /api/explain write-back) keeps offsets valid.
	if err := c.setExplanation(2, "baru"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"satu", "dua", "tiga"} {
		if h, err := c.read(i); err != nil || h.ID != want {
			t.Fatalf("after setExplanation rec %d: %v %+v", i, err, h)
		}
	}
	if h, _ := c.read(1); h.Explanation != "baru" {
		t.Fatal("explanation not saved")
	}
	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), `"grade": "shahih"`) || !strings.Contains(string(raw), long) {
		t.Fatal("write-back must preserve other records and unknown fields")
	}
	// Write-back keeps the MarshalIndent layout of the original files.
	var back []rec
	if err := json.Unmarshal(raw, &back); err != nil || back[1].Explanation != "baru" {
		t.Fatalf("file content: %v %+v", err, back)
	}
	if !strings.HasPrefix(string(raw), "[\n  {\n    \"number\": 1,\n    \"arab\": \"a1\",") {
		t.Fatalf("unexpected layout: %.60q", raw)
	}
}

// TestWriteBackFormatRealData: the new write-back produces exactly what the
// original implementation (unmarshal all + MarshalIndent) produced. Works on
// a temp copy, resource/ is never modified.
func TestWriteBackFormatRealData(t *testing.T) {
	if testing.Short() {
		t.Skip("real-data test skipped in -short mode")
	}
	src, err := os.ReadFile("resource/malik.json")
	if err != nil {
		t.Skip("resource/malik.json not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "malik.json"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := buildStore(dir, []CollectionInfo{{Name: "Malik", Slug: "malik", Total: 1587}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var all []Collection
	if err := json.Unmarshal(src, &all); err != nil {
		t.Fatal(err)
	}
	target := all[10].Number
	all[10].Explanation = "> Disclaimer <b>&</b>\n\n## Judul"
	want, _ := json.MarshalIndent(all, "", "  ")

	if err := s.bySlug["malik"].setExplanation(target, all[10].Explanation); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "malik.json"))
	if string(got) != string(want) {
		t.Fatalf("write-back differs from the original format (len %d vs %d)", len(got), len(want))
	}
}
