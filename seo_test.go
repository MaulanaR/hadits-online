package main

import (
	"encoding/json"
	"html/template"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMatanOf(t *testing.T) {
	cases := map[string]string{
		`Telah menceritakan kepada kami [Al Humaidi] dia berkata; saya mendengar [Umar bin Al Khaththab] diatas mimbar berkata; saya mendengar Rasulullah shallallahu 'alaihi wasallam bersabda: "Semua perbuatan tergantung niatnya, dan (balasan) bagi tiap-tiap orang"`: "Semua perbuatan tergantung niatnya",
		`dari [Abu Hurairah] radliallahu 'anhu berkata; Nabi shallallahu 'alaihi wasallam bersabda: Ada dua kalimat yang disukai Ar Rahman`:                                                                                                                                "Ada dua kalimat",
		`dari [Abu Hurairah] dia berkata, "Rasulullah shallallahu 'alaihi wasallam bersabda: "Akan ada di akhir zaman para Dajjal"`:                                                                                                                                        "Akan ada di akhir zaman",
	}
	for in, want := range cases {
		if got := matanOf(in); !strings.HasPrefix(got, want) {
			t.Errorf("matanOf(...) = %q, want prefix %q", got, want)
		}
	}
	if got := truncateRunes("Semua perbuatan tergantung niatnya, dan balasan", 20); got != "Semua perbuatan…" {
		t.Errorf("truncateRunes = %q", got)
	}
}

func TestJSONLDValid(t *testing.T) {
	info := &CollectionInfo{Name: "Bukhari", Slug: "bukhari", Total: 1}
	h := &Collection{Number: 1, ID: `x [a] "<script>alert(1)</script>"`}
	for _, js := range []string{string(homeJSONLD()), string(faqJSONLD()), string(collectionJSONLD(info, "u", "d")), string(hadithJSONLD(info, h, "u", matanOf(h.ID)))} {
		var v any
		if err := json.Unmarshal([]byte(js), &v); err != nil {
			t.Fatalf("invalid JSON-LD: %v", err)
		}
		if strings.Contains(js, "<script") {
			t.Fatal("JSON-LD must escape <")
		}
	}
}

// seoHead renders templates/components/seo.html for the given data.
func seoHead(t *testing.T, s SEOData) string {
	t.Helper()
	tpl, err := template.New("x").ParseFiles("templates/components/seo.html")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tpl.ExecuteTemplate(&b, "seo", struct{ SEO SEOData }{s.withDefaults()}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestSiteVerificationMeta(t *testing.T) {
	t.Setenv("GOOGLE_SITE_VERIFICATION", "")
	t.Setenv("BING_SITE_VERIFICATION", "")
	s := SEOData{Title: "t", Canonical: siteURL + "/"}
	s.GoogleVerification, s.BingVerification = siteVerification()
	if h := seoHead(t, s); strings.Contains(h, "google-site-verification") || strings.Contains(h, "msvalidate.01") {
		t.Fatalf("verification meta rendered without env vars:\n%s", h)
	}

	t.Setenv("GOOGLE_SITE_VERIFICATION", " g-token_123 ")
	t.Setenv("BING_SITE_VERIFICATION", "B1NG\"<x>")
	s.GoogleVerification, s.BingVerification = siteVerification()
	h := seoHead(t, s)
	if !strings.Contains(h, `<meta name="google-site-verification" content="g-token_123">`) {
		t.Errorf("missing google verification meta:\n%s", h)
	}
	if !strings.Contains(h, `<meta name="msvalidate.01" content="B1NG&#34;&lt;x&gt;">`) {
		t.Errorf("missing/unescaped bing verification meta:\n%s", h)
	}
}

func TestOGImageDefaults(t *testing.T) {
	h := seoHead(t, SEOData{Title: "t"})
	for _, want := range []string{
		`<meta property="og:image" content="` + siteURL + `/static/og-image.png">`,
		`<meta property="og:image:width" content="1200">`,
		`<meta property="og:image:height" content="630">`,
		`<meta name="twitter:card" content="summary_large_image">`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("missing %s", want)
		}
	}
	f, err := os.Open("static/og-image.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := png.DecodeConfig(f)
	if err != nil || cfg.Width != 1200 || cfg.Height != 630 {
		t.Fatalf("static/og-image.png: %dx%d, %v; want 1200x630", cfg.Width, cfg.Height, err)
	}
}

// get performs a request against the full router (no data needed).
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestRobotsAndIndexNowKey(t *testing.T) {
	loadAssetHashes()
	loadTemplates()
	h := newRouter()

	rec := get(t, h, "/robots.txt")
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "\nSitemap: https://hadits.online/sitemap.xml\n") ||
		!strings.Contains(body, "Disallow: /api/") || strings.Contains(body, "Disallow: /\n") {
		t.Fatalf("robots.txt %d:\n%s", rec.Code, body)
	}

	const key = "0123456789abcdef0123456789abcdef"
	t.Setenv("INDEXNOW_KEY", "")
	if rec := get(t, h, "/"+key+".txt"); rec.Code != 404 {
		t.Errorf("key file without INDEXNOW_KEY: %d, want 404", rec.Code)
	}
	t.Setenv("INDEXNOW_KEY", key)
	rec = get(t, h, "/"+key+".txt")
	if rec.Code != 200 || rec.Body.String() != key || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Errorf("key file: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec := get(t, h, "/fedcba9876543210.txt"); rec.Code != 404 {
		t.Errorf("other key: %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/robots.txt"); rec.Body.String() != body {
		t.Error("robots.txt must not be shadowed by the key route")
	}
	for _, bad := range []string{"bad key!", "short", "../etc/passwd"} {
		t.Setenv("INDEXNOW_KEY", bad)
		if k := indexNowKey(); k != "" {
			t.Errorf("invalid INDEXNOW_KEY %q accepted", bad)
		}
	}
}

func TestHadithTitleAndDescription(t *testing.T) {
	long := "Semua perbuatan tergantung niatnya, dan (balasan) bagi tiap-tiap orang (tergantung) apa yang diniatkan; Barangsiapa niat hijrahnya karena dunia yang ingin digapainya"
	for _, n := range []int{1, 26363} {
		if got := hadithTitle("Ibnu Majah", n, long); utf8.RuneCountInString(got) > maxTitleRunes || !strings.HasSuffix(got, " – HR. Ibnu Majah No. "+strconv.Itoa(n)) {
			t.Errorf("title %q (%d runes)", got, utf8.RuneCountInString(got))
		}
	}
	if got := hadithTitle("Bukhari", 1, ""); got != "Hadits Bukhari No. 1 | hadits.online" {
		t.Errorf("empty matan title %q", got)
	}
	if got := hadithDescription("Bukhari", 1, long); utf8.RuneCountInString(got) > maxDescRunes {
		t.Errorf("description too long: %q", got)
	}
	short := hadithDescription("Tirmidzi", 50, "Abu Idris tidak mendengar sesuatu pun dari Umar.")
	if want := "Abu Idris tidak mendengar sesuatu pun dari Umar. HR. Tirmidzi No. 50, lengkap dengan teks Arab dan terjemahan Indonesia."; short != want {
		t.Errorf("short description %q", short)
	}
	if got := hadithDescription("Tirmidzi", 50, ""); !strings.HasPrefix(got, "Hadits riwayat Tirmidzi No. 50") {
		t.Errorf("empty matan description %q", got)
	}
}
