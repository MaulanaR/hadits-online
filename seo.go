package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/mux"
)

// siteURL is the canonical origin used in canonical/OG URLs, JSON-LD and
// sitemaps (override with SITE_URL).
var siteURL = func() string {
	if v := strings.TrimRight(os.Getenv("SITE_URL"), "/"); v != "" {
		return v
	}
	return "https://hadits.online"
}()

const siteName = "hadits.online"

// defaultOGImage is the 1200x630 share image (static/og-image.png: logo on
// the emerald ketupat background).
func defaultOGImage() string { return siteURL + "/static/og-image.png" }

const ogImageWidth, ogImageHeight = 1200, 630

// SEOData is rendered by templates/components/seo.html.
type SEOData struct {
	Title       string
	Description string
	Keywords    string
	OGImage     string
	OGImageAlt  string
	OGImageW    int
	OGImageH    int
	OGUrl       string
	OGType      string
	Canonical   string
	Robots      string
	PrevURL     string
	NextURL     string
	JSONLD      template.JS
	// Search-engine ownership tokens (home page only; see siteVerification).
	GoogleVerification string
	BingVerification   string
}

func (s SEOData) withDefaults() SEOData {
	if s.OGImage == "" {
		s.OGImage = defaultOGImage()
		s.OGImageAlt = "hadits.online - Pusat Belajar Hadits Bahasa Indonesia"
		s.OGImageW, s.OGImageH = ogImageWidth, ogImageHeight
	}
	if s.OGType == "" {
		s.OGType = "website"
	}
	if s.OGUrl == "" {
		s.OGUrl = s.Canonical
	}
	return s
}

// siteVerification returns the Google Search Console and Bing Webmaster
// verification tokens from GOOGLE_SITE_VERIFICATION / BING_SITE_VERIFICATION
// (read per call so values from .env and test overrides apply).
func siteVerification() (google, bing string) {
	return strings.TrimSpace(os.Getenv("GOOGLE_SITE_VERIFICATION")), strings.TrimSpace(os.Getenv("BING_SITE_VERIFICATION"))
}

// reIndexNowKey is the key format allowed by the IndexNow protocol.
var reIndexNowKey = regexp.MustCompile(`^[A-Za-z0-9-]{8,128}$`)

// indexNowKey returns INDEXNOW_KEY when it is a valid IndexNow key.
func indexNowKey() string {
	k := strings.TrimSpace(os.Getenv("INDEXNOW_KEY"))
	if !reIndexNowKey.MatchString(k) {
		return ""
	}
	return k
}

// indexNowKeyHandler serves /{key}.txt with the key as body (IndexNow
// ownership proof) and 404s for any other name or when no key is set.
func indexNowKeyHandler(w http.ResponseWriter, r *http.Request) {
	key := indexNowKey()
	if key == "" || mux.Vars(r)["key"] != key {
		notFoundHandler(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	io.WriteString(w, key)
}

// jsonLD marshals structured data (json.Marshal escapes <, > and &, so the
// output is safe inside <script type="application/ld+json">).
func jsonLD(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("json-ld: %v", err)
		return ""
	}
	return template.JS(b)
}

type ldMap = map[string]any

func ldOrganization() ldMap {
	return ldMap{
		"@type": "Organization",
		"@id":   siteURL + "/#organization",
		"name":  siteName,
		"url":   siteURL + "/",
		"logo":  ldMap{"@type": "ImageObject", "url": siteURL + "/static/logo.png"},
	}
}

type crumb struct{ name, url string }

func ldBreadcrumbs(items ...crumb) ldMap {
	list := make([]ldMap, 0, len(items))
	for i, c := range items {
		list = append(list, ldMap{"@type": "ListItem", "position": i + 1, "name": c.name, "item": c.url})
	}
	return ldMap{"@type": "BreadcrumbList", "itemListElement": list}
}

func homeJSONLD() template.JS {
	return jsonLD(ldMap{
		"@context": "https://schema.org",
		"@graph": []ldMap{
			{
				"@type":       "WebSite",
				"@id":         siteURL + "/#website",
				"name":        siteName,
				"url":         siteURL + "/",
				"inLanguage":  "id",
				"publisher":   ldMap{"@id": siteURL + "/#organization"},
				"description": "Cari dan pelajari hadits dari Bukhari, Muslim, Abu Daud dan kitab lainnya dengan terjemahan Indonesia.",
				"potentialAction": ldMap{
					"@type":       "SearchAction",
					"target":      ldMap{"@type": "EntryPoint", "urlTemplate": siteURL + "/search?q={search_term_string}"},
					"query-input": "required name=search_term_string",
				},
			},
			ldOrganization(),
		},
	})
}

func collectionJSONLD(info *CollectionInfo, pageURL, description string) template.JS {
	return jsonLD(ldMap{
		"@context": "https://schema.org",
		"@graph": []ldMap{
			{
				"@type":       "CollectionPage",
				"name":        "Kitab Hadits " + info.Name,
				"url":         pageURL,
				"description": description,
				"inLanguage":  []string{"ar", "id"},
				"isPartOf":    ldMap{"@type": "WebSite", "@id": siteURL + "/#website", "url": siteURL + "/", "name": siteName},
			},
			ldBreadcrumbs(crumb{"Beranda", siteURL + "/"}, crumb{"Kitab " + info.Name, siteURL + "/collection/" + info.Slug}),
		},
	})
}

func hadithJSONLD(info *CollectionInfo, h *Collection, pageURL, matan string) template.JS {
	return jsonLD(ldMap{
		"@context": "https://schema.org",
		"@graph": []ldMap{
			ldBreadcrumbs(
				crumb{"Beranda", siteURL + "/"},
				crumb{"Kitab " + info.Name, siteURL + "/collection/" + info.Slug},
				crumb{fmt.Sprintf("No. %d", h.Number), pageURL},
			),
			{
				"@type":      "CreativeWork",
				"name":       fmt.Sprintf("Hadits %s No. %d", info.Name, h.Number),
				"url":        pageURL,
				"inLanguage": []string{"ar", "id"},
				"text":       truncateRunes(matan, 500),
				"isPartOf":   ldMap{"@type": "Book", "name": "Kitab Hadits " + info.Name},
				"publisher":  ldOrganization(),
			},
		},
	})
}

// faqItems mirrors the questions and answers rendered in templates/faq.html
// (the FAQPage JSON-LD must match the visible text).
var faqItems = []struct{ Q, A string }{
	{"Apakah ini gratis?", "Ya, Hadits.online sepenuhnya gratis. Kami tidak memungut biaya atau menyisipkan fitur berbayar dalam aplikasi ini."},
	{"Cara simpan favorit?", "Klik ikon hati pada detail hadits. Data disimpan secara lokal di browser Anda, sehingga privasi Anda tetap terjaga."},
	{"Akurasi data?", "Data bersumber dari kitab-kitab induk yang telah divalidasi kebenarannya oleh para ulama melalui sumber digital terbuka yang kredibel."},
	{"Cara mencari hadits?", "Gunakan kolom pencarian di beranda atau navbar. Anda bisa mencari berdasarkan kata kunci atau filter per kitab tertentu."},
	{"Dukungan browser?", "Optimal pada browser modern seperti Chrome, Safari, dan Firefox. Pastikan browser Anda diperbarui ke versi terbaru."},
	{"Aplikasi mobile?", "Aplikasi ini dirancang dengan prinsip 'Mobile-First' agar nyaman digunakan langsung melalui browser ponsel Anda."},
	{"Privasi data?", "Kami tidak menyimpan data pribadi di server. Semua riwayat pencarian dan favorit tetap berada di perangkat Anda."},
}

func faqJSONLD() template.JS {
	var qs []ldMap
	for _, f := range faqItems {
		qs = append(qs, ldMap{
			"@type":          "Question",
			"name":           f.Q,
			"acceptedAnswer": ldMap{"@type": "Answer", "text": f.A},
		})
	}
	return jsonLD(ldMap{"@context": "https://schema.org", "@type": "FAQPage", "mainEntity": qs})
}

// ---------------------------------------------------------------------------
// Matan extraction for titles and descriptions
// ---------------------------------------------------------------------------

var (
	reSpaces = regexp.MustCompile(`\s+`)
	// Leading narration formulas before the actual words of the hadith.
	reMatanPrefix = regexp.MustCompile(`(?i)^[\s,.;:!?'"“”‘’()\-–—]*(?:(?:(?:dari\s+)?(?:rasulullah|nabi|beliau)(?:\s+shallallahu\s*'?\s*alaihi\s*wa\s*sallam)?(?:\s+(?:pernah\s+)?(?:bersabda|berkata|menjawab)(?:\s+kepada\s+\w+)?)?|(?:radhiyallahu|radliallahu|radhiallahu|radliyallahu)\s*'?\s*(?:anhu|anha|anhuma|anhum)|(?:dia|ia|beliau)\s+(?:berkata|bersabda)|(?:saya|aku)\s+(?:pernah\s+)?mendengar|bahwa|bahwasanya|berkata|bersabda|menuturkan|mengatakan)\b)?[\s,.;:'"“”]*`)
)

// matanOf returns the body of the hadith (text after the isnad): the text
// after the last [narrator]; leading narration formulas ("Rasulullah
// shallallahu 'alaihi wasallam bersabda:") and an opening quotation mark are
// skipped repeatedly; remaining quotes are removed.
func matanOf(id string) string {
	rest := id
	if i := strings.LastIndex(id, "]"); i >= 0 && i+1 < len(id) {
		rest = id[i+1:]
	}
	for iter := 0; iter < 4; iter++ {
		prev := rest
		// Jump into a quotation that starts near the beginning.
		if q := strings.IndexAny(rest, "\"“"); q >= 0 && q < 160 {
			_, size := utf8.DecodeRuneInString(rest[q:])
			if quoted := strings.TrimSpace(rest[q+size:]); utf8.RuneCountInString(quoted) >= 20 {
				rest = quoted
			}
		}
		rest = reMatanPrefix.ReplaceAllString(rest, "")
		if rest == prev {
			break
		}
	}
	rest = strings.NewReplacer(`"`, "", "“", "", "”", "").Replace(rest)
	rest = strings.TrimSpace(reSpaces.ReplaceAllString(rest, " "))
	if utf8.RuneCountInString(rest) < 40 {
		// Short tail (e.g. "dengan isnad seperti ini."): prefer the first
		// quotation in the whole text, else the whole text without brackets.
		if q := strings.IndexAny(id, "\"“"); q >= 0 {
			_, size := utf8.DecodeRuneInString(id[q:])
			quoted := id[q+size:]
			if e := strings.IndexAny(quoted, "\"”"); e > 0 {
				quoted = quoted[:e]
			}
			quoted = reMatanPrefix.ReplaceAllString(strings.NewReplacer("[", "", "]", "").Replace(quoted), "")
			quoted = strings.TrimSpace(reSpaces.ReplaceAllString(quoted, " "))
			if n := utf8.RuneCountInString(quoted); n >= 40 && n > utf8.RuneCountInString(rest) {
				rest = quoted
			}
		}
		if utf8.RuneCountInString(rest) < 15 {
			rest = strings.TrimSpace(reSpaces.ReplaceAllString(strings.NewReplacer("[", "", "]", "", `"`, "").Replace(id), " "))
		}
	}
	// Capitalise the first letter for titles/descriptions.
	if r, size := utf8.DecodeRuneInString(rest); size > 0 && unicode.IsLower(r) {
		rest = string(unicode.ToUpper(r)) + rest[size:]
	}
	return rest
}

// Search results show about 60 characters of a title and ~155 of a
// description.
const (
	maxTitleRunes = 65
	maxDescRunes  = 158
)

// hadithTitle is "<matan…> – HR. <Kitab> No. <n>" within maxTitleRunes (the
// site name is shown by search engines from the WebSite structured data).
func hadithTitle(collection string, number int, matan string) string {
	ref := fmt.Sprintf("HR. %s No. %d", collection, number)
	if matan == "" {
		return fmt.Sprintf("Hadits %s No. %d | %s", collection, number, siteName)
	}
	budget := max(30, maxTitleRunes-utf8.RuneCountInString(ref)-3)
	return truncateRunes(matan, budget) + " – " + ref
}

// hadithDescription is the matan, completed with the source when it is short
// so that no hadith page gets a thin description.
func hadithDescription(collection string, number int, matan string) string {
	tail := fmt.Sprintf("HR. %s No. %d, lengkap dengan teks Arab dan terjemahan Indonesia.", collection, number)
	switch {
	case matan == "":
		return "Hadits riwayat " + strings.TrimPrefix(tail, "HR. ")
	case utf8.RuneCountInString(matan) < 100:
		return truncateRunes(strings.TrimRight(matan, " .,;:")+". "+tail, maxDescRunes)
	}
	return truncateRunes(matan, maxDescRunes)
}

// truncateRunes cuts s to at most n runes at a word boundary, adding "…".
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	cut := capRunes(s, n-1)
	if i := strings.LastIndexFunc(cut, unicode.IsSpace); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,.;:-–—") + "…"
}

// ---------------------------------------------------------------------------
// robots.txt and sitemaps (generated on demand from the in-memory store; each
// file takes ~1 ms and is cached by clients/CDNs for a day).
// ---------------------------------------------------------------------------

func robotsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /api/\n\nSitemap: %s/sitemap.xml\n", siteURL)
}

func writeXML(w http.ResponseWriter, b *bytes.Buffer) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write(b.Bytes())
}

func lastmod(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01-02")
}

// sitemapHandler serves the sitemap index.
func sitemapHandler(w http.ResponseWriter, r *http.Request) {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	var newest time.Time
	for _, c := range store.colls {
		if m := c.modTime(); m.After(newest) {
			newest = m
		}
	}
	fmt.Fprintf(&b, "  <sitemap><loc>%s/sitemap-pages.xml</loc><lastmod>%s</lastmod></sitemap>\n", siteURL, lastmod(newest))
	for _, c := range store.colls {
		fmt.Fprintf(&b, "  <sitemap><loc>%s/sitemap-%s.xml</loc><lastmod>%s</lastmod></sitemap>\n", siteURL, c.Info.Slug, lastmod(c.modTime()))
	}
	b.WriteString("</sitemapindex>\n")
	writeXML(w, &b)
}

// sitemapPartHandler serves /sitemap-pages.xml and /sitemap-{slug}.xml.
func sitemapPartHandler(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	if name == "pages" {
		for _, p := range []struct {
			path, prio string
		}{{"/", "1.0"}, {"/faq", "0.5"}, {"/donate", "0.5"}} {
			fmt.Fprintf(&b, "  <url><loc>%s%s</loc><priority>%s</priority></url>\n", siteURL, p.path, p.prio)
		}
		for _, c := range store.colls {
			fmt.Fprintf(&b, "  <url><loc>%s/collection/%s</loc><lastmod>%s</lastmod><priority>0.8</priority></url>\n", siteURL, c.Info.Slug, lastmod(c.modTime()))
		}
	} else {
		c, ok := getBySlug(name)
		if !ok {
			notFoundHandler(w, r)
			return
		}
		lm := lastmod(c.modTime())
		for _, n := range c.numbersCopy() {
			fmt.Fprintf(&b, "  <url><loc>%s/collection/%s/%d</loc><lastmod>%s</lastmod></url>\n", siteURL, c.Info.Slug, n, lm)
		}
	}
	b.WriteString("</urlset>\n")
	writeXML(w, &b)
}

// redirectHome sends /index.html to / permanently.
func redirectHome(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}
