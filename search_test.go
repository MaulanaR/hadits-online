package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// setupRealSearch installs the real-data store/index as globals for the
// search code (skipped in -short mode).
func setupRealSearch(t *testing.T) {
	t.Helper()
	s, ix := realDataIndex(t)
	store, searchIdx = s, ix
	if data == nil {
		data = &HadithData{Info: loadListForTest(t)}
	}
}

type hit struct {
	ref   string
	score float32
}

func doSearch(t *testing.T, f SearchFilters) ([]hit, *searchOutcome, time.Duration) {
	t.Helper()
	if f.Language == "" {
		f.Language = "all"
	}
	if f.SortBy == "" {
		f.SortBy = "relevance"
	}
	t0 := time.Now()
	q := parseQuery(f.Query, searchIdx)
	out := executeSearch(q, f, searchIdx, store)
	d := time.Since(t0)
	var hits []hit
	for i, id := range out.ids {
		c := store.colls[store.docColl[id]]
		hits = append(hits, hit{fmt.Sprintf("%s/%d", c.Info.Slug, store.docNum[id]), out.scores[i]})
	}
	return hits, out, d
}

func refs(h []hit, n int) []string {
	var out []string
	for i := 0; i < len(h) && i < n; i++ {
		out = append(out, h[i].ref)
	}
	return out
}

func rankOf(h []hit, ref string) int {
	for i, x := range h {
		if x.ref == ref {
			return i + 1
		}
	}
	return -1
}

// matanOf returns the normalized matan tokens of a result (for assertions).
func textOf(t *testing.T, ref string) (matan, all, ar []string) {
	t.Helper()
	var slug string
	var num int
	i := strings.LastIndex(ref, "/")
	slug = ref[:i]
	fmt.Sscan(ref[i+1:], &num)
	c, _ := getBySlug(slug)
	idx, _ := c.indexOf(num)
	h, err := c.read(idx)
	if err != nil {
		t.Fatal(err)
	}
	return docStreams(&h)
}

func TestSearchCases(t *testing.T) {
	setupRealSearch(t)
	verbose := os.Getenv("SEARCH_VERBOSE") == "1"
	show := func(name string, h []hit, out *searchOutcome, d time.Duration) {
		if verbose {
			var b strings.Builder
			for i := 0; i < len(h) && i < 15; i++ {
				fmt.Fprintf(&b, " %s(%.2f)", h[i].ref, h[i].score)
			}
			t.Logf("%-32s total=%d partial=%v %v\n   %s", name, len(h), out.partial, d.Round(time.Microsecond), b.String())
		}
		if d > 100*time.Millisecond {
			t.Errorf("%s: took %v", name, d)
		}
	}

	// 1. niat
	h, out, d := doSearch(t, SearchFilters{Query: "niat"})
	show("niat", h, out, d)
	if r := rankOf(h, "bukhari/1"); r < 1 || r > 3 {
		t.Errorf("niat: bukhari/1 at rank %d, want top 3", r)
	}
	for _, ref := range []string{"bukhari/52", "bukhari/6195", "nasai/74"} {
		if r := rankOf(h, ref); r < 1 || r > 15 {
			t.Errorf("niat: %s at rank %d, want top 15", ref, r)
		}
	}

	// 2. quoted phrase
	h, out, d = doSearch(t, SearchFilters{Query: `"semua perbuatan tergantung niatnya"`})
	show("phrase", h, out, d)
	if len(h) == 0 || len(h) > 30 || h[0].ref != "bukhari/1" || out.partial {
		t.Errorf("phrase: total %d first %v", len(h), refs(h, 1))
	}

	// 3/4. puasa ramadhan / ramadan
	h3, out, d := doSearch(t, SearchFilters{Query: "puasa ramadhan"})
	show("puasa ramadhan", h3, out, d)
	if len(h3) < 120 || len(h3) > 300 || out.partial {
		t.Errorf("puasa ramadhan total %d", len(h3))
	}
	bm := 0
	for _, r := range refs(h3, 10) {
		if strings.HasPrefix(r, "bukhari/") || strings.HasPrefix(r, "muslim/") {
			bm++
		}
	}
	if bm == 0 {
		t.Errorf("puasa ramadhan: no bukhari/muslim in top 10: %v", refs(h3, 10))
	}
	h4, _, _ := doSearch(t, SearchFilters{Query: "puasa ramadan"})
	if !slices.Equal(refs(h3, 5), refs(h4, 5)) && len(h3) != len(h4) {
		t.Errorf("ramadan variant differs: %v vs %v", refs(h3, 5), refs(h4, 5))
	}

	// 5. sholat / salat == shalat
	hs, _, _ := doSearch(t, SearchFilters{Query: "shalat"})
	for _, v := range []string{"sholat", "salat"} {
		hv, out, d := doSearch(t, SearchFilters{Query: v})
		show(v, hv, out, d)
		if len(hv) < 1000 || !slices.Equal(refs(hv, 10), refs(hs, 10)) {
			t.Errorf("%s: total %d top %v vs shalat %v", v, len(hv), refs(hv, 5), refs(hs, 5))
		}
	}

	// 6. shalat jumat == shalat jum'at; top-10 contain both words
	h6, out, d := doSearch(t, SearchFilters{Query: "shalat jumat"})
	show("shalat jumat", h6, out, d)
	h6b, _, _ := doSearch(t, SearchFilters{Query: "shalat jum'at"})
	if !slices.Equal(refs(h6, 50), refs(h6b, 50)) {
		t.Error("jumat vs jum'at differ")
	}
	for _, r := range refs(h6, 10) {
		_, all, _ := textOf(t, r)
		if !slices.Contains(all, "jumat") && !slices.Contains(all, "jumuah") {
			t.Errorf("shalat jumat: %s lacks jumat", r)
		}
	}

	// 7. zakat fitrah
	h7, out, d := doSearch(t, SearchFilters{Query: "zakat fitrah"})
	show("zakat fitrah", h7, out, d)
	for _, r := range refs(h7, 10) {
		_, all, _ := textOf(t, r)
		if !containsSeq(all, []string{"zakat", "fitrah"}) && !containsSeq(all, []string{"zakat", "fithri"}) && !containsSeq(all, []string{"zakat", "fitri"}) {
			t.Errorf("zakat fitrah: %s lacks the phrase", r)
		}
	}

	// 8. ali: token match only; matan Ali above isnad-only
	h8, out, d := doSearch(t, SearchFilters{Query: "ali"})
	show("ali", h8, out, d)
	for _, r := range refs(h8, 20) {
		matan, _, _ := textOf(t, r)
		if !slices.Contains(matan, "ali") {
			t.Errorf("ali: %s has Ali only in the isnad but ranks in top 20", r)
		}
	}

	// 9. menikahi: stem family, exact first
	h9, out, d := doSearch(t, SearchFilters{Query: "menikahi"})
	show("menikahi", h9, out, d)
	foundNikahOnly := false
	for _, x := range h9 {
		_, all, _ := textOf(t, x.ref)
		if !slices.Contains(all, "menikahi") && slices.Contains(all, "nikah") {
			foundNikahOnly = true
			break
		}
	}
	if !foundNikahOnly {
		t.Error("menikahi: stem family (nikah) not matched")
	}
	for _, r := range refs(h9, 5) {
		_, all, _ := textOf(t, r)
		if !slices.Contains(all, "menikahi") {
			t.Errorf("menikahi: top-5 %s lacks exact form", r)
		}
	}

	// 10. kesabaran
	h10, out, d := doSearch(t, SearchFilters{Query: "kesabaran"})
	show("kesabaran", h10, out, d)
	for _, r := range refs(h10, 5) {
		_, all, _ := textOf(t, r)
		if !slices.Contains(all, "kesabaran") {
			t.Errorf("kesabaran: top-5 %s lacks exact form", r)
		}
	}

	// 11. menuntut ilmu: phrase docs on top
	h11, out, d := doSearch(t, SearchFilters{Query: "menuntut ilmu"})
	show("menuntut ilmu", h11, out, d)
	for _, r := range refs(h11, 5) {
		matan, _, _ := textOf(t, r)
		if !containsSeq(matan, []string{"menuntut", "ilmu"}) {
			t.Errorf("menuntut ilmu: top-5 %s lacks the phrase", r)
		}
	}

	// 12. kebersihan sebagian dari iman
	h12, out, d := doSearch(t, SearchFilters{Query: "kebersihan sebagian dari iman"})
	show("kebersihan sebagian dari iman", h12, out, d)

	// 13. abu hurairah deterministic
	a, out, d := doSearch(t, SearchFilters{Query: "abu hurairah"})
	show("abu hurairah", a, out, d)
	b, _, _ := doSearch(t, SearchFilters{Query: "abu hurairah"})
	if len(a) < 4000 || !slices.Equal(refs(a, 100), refs(b, 100)) {
		t.Errorf("abu hurairah: %d results, deterministic=%v", len(a), slices.Equal(refs(a, 100), refs(b, 100)))
	}

	// 14/17. Arabic with and without harakat
	h14, out, d := doSearch(t, SearchFilters{Query: "الصلاة"})
	show("الصلاة", h14, out, d)
	h17, _, _ := doSearch(t, SearchFilters{Query: "الصَّلَاة"})
	if len(h14) < 2000 || !slices.Equal(refs(h14, 20), refs(h17, 20)) {
		t.Errorf("الصلاة: %d results; harakat-insensitive=%v", len(h14), slices.Equal(refs(h14, 20), refs(h17, 20)))
	}

	// 15. إنما الأعمال بالنيات
	h15, out, d := doSearch(t, SearchFilters{Query: "إنما الأعمال بالنيات"})
	show("إنما الأعمال بالنيات", h15, out, d)
	top3 := refs(h15, 3)
	for _, r := range []string{"bukhari/1", "abu-dawud/1882", "ibnu-majah/4217"} {
		if !slices.Contains(top3, r) {
			t.Errorf("إنما الأعمال بالنيات: %s not in top 3 %v", r, top3)
		}
	}

	// 16. صيام رمضان lang=ar
	h16, out, d := doSearch(t, SearchFilters{Query: "صيام رمضان", Language: "ar"})
	show("صيام رمضان ar", h16, out, d)
	for _, r := range []string{"abu-dawud/2054", "ahmad/1318", "ahmad/14929"} {
		if rk := rankOf(h16, r); rk < 1 || rk > 15 {
			t.Errorf("صيام رمضان: %s at rank %d", r, rk)
		}
	}

	// 18/19 direct hits
	for q, want := range map[string]string{"bukhari 1": "bukhari/1", "HR Bukhari no 1": "bukhari/1", "abu daud 1882": "abu-dawud/1882", "HR. Muslim No. 8": "muslim/8", "bukhari 99999": ""} {
		p := parseQuery(q, searchIdx)
		got := ""
		if p.direct != nil {
			got = fmt.Sprintf("%s/%d", p.direct.slug, p.direct.number)
		}
		if got != want {
			t.Errorf("direct %q = %q want %q", q, got, want)
		}
	}

	// 20. collections filter
	h20, _, _ := doSearch(t, SearchFilters{Query: "riba", Collections: []string{"bukhari", "muslim"}})
	for _, x := range h20 {
		if !strings.HasPrefix(x.ref, "bukhari/") && !strings.HasPrefix(x.ref, "muslim/") {
			t.Fatalf("riba filter leaked %s", x.ref)
		}
	}

	// 21. number range + sort=number
	h21, _, _ := doSearch(t, SearchFilters{Query: "shalat", Collections: []string{"muslim"}, NumberRange: NumberRange{Min: 100, Max: 200}, SortBy: "number"})
	prev := 0
	for _, x := range h21 {
		var n int
		fmt.Sscanf(strings.TrimPrefix(x.ref, "muslim/"), "%d", &n)
		if n < 100 || n > 200 || n < prev {
			t.Fatalf("range/sort broken at %s", x.ref)
		}
		prev = n
	}

	// 22. senyum lang=id includes tersenyum
	h22, _, _ := doSearch(t, SearchFilters{Query: "senyum", Language: "id"})
	if len(h22) < 100 {
		t.Errorf("senyum: %d results", len(h22))
	}

	// 23. latin query on arabic field
	if h23, _, _ := doSearch(t, SearchFilters{Query: "shalat", Language: "ar"}); len(h23) != 0 {
		t.Errorf("shalat lang=ar: %d results", len(h23))
	}

	// 24. edge cases don't panic
	for _, q := range []string{"(", "[", "a&b#c", "%", `"`, "<script>", strings.Repeat("shalat ", 50), "dan yang dari", "zzzqqq"} {
		doSearch(t, SearchFilters{Query: q})
	}
	if h, _, _ := doSearch(t, SearchFilters{Query: "dan yang dari"}); len(h) == 0 {
		t.Error("stopword-only query should still search")
	}
}
