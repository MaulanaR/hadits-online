package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Templates: parsed once at startup (set DEV_TEMPLATES=1 to re-parse per request
// while editing HTML).
// ---------------------------------------------------------------------------

var pageFiles = map[string]string{
	"index.html":      "templates/index.html",
	"favorites.html":  "templates/favorites.html",
	"collection.html": "templates/collection.html",
	"hadith.html":     "templates/hadith.html",
	"search.html":     "templates/search.html",
	"donate.html":     "templates/donate.html",
	"faq.html":        "templates/faq.html",
	"404.html":        "templates/404.html",
}

var sharedTemplateFiles = []string{
	"templates/components/navbar.html",
	"templates/components/footer.html",
	"templates/components/seo.html",
}

var (
	pagesMu      sync.RWMutex
	pages        map[string]*template.Template
	devTemplates = os.Getenv("DEV_TEMPLATES") == "1"
)

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"add":                 add,
		"add1":                add1,
		"subtract":            subtract,
		"multiply":            multiply,
		"pageNumbers":         pageNumbers,
		"formatHadith":        formatHadith,
		"formatRupiah":        formatRupiah,
		"calculatePercentage": calculatePercentage,
		"safeHTML":            safeHTML,
		"urlEncode":           urlEncode,
		"hasFilter":           hasFilter,
		"buildFilterURL":      buildFilterURL,
		"emptyFilters":        func() SearchFilters { return SearchFilters{} },
		"popularQueries":      func() []string { return popularQueries },
		"asset":               asset,
	}
}

func parseTemplates() (map[string]*template.Template, error) {
	out := make(map[string]*template.Template, len(pageFiles))
	funcs := templateFuncs()
	for name, file := range pageFiles {
		files := append([]string{file}, sharedTemplateFiles...)
		t, err := template.New(name).Funcs(funcs).ParseFiles(files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		out[name] = t
	}
	return out, nil
}

func loadTemplates() {
	p, err := parseTemplates()
	if err != nil {
		log.Fatal("Template error: ", err)
	}
	pagesMu.Lock()
	pages = p
	pagesMu.Unlock()
}

func getTemplate(name string) (*template.Template, error) {
	if devTemplates {
		p, err := parseTemplates()
		if err != nil {
			return nil, err
		}
		return p[name], nil
	}
	pagesMu.RLock()
	defer pagesMu.RUnlock()
	t, ok := pages[name]
	if !ok {
		return nil, fmt.Errorf("unknown template %q", name)
	}
	return t, nil
}

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// render executes a page template into a pooled buffer and only writes the
// response once execution succeeded, so a template error never produces a
// half-written page with a 200 status.
func render(w http.ResponseWriter, name string, status int, data any) {
	t, err := getTemplate(name)
	if err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer func() {
		if buf.Cap() <= 1<<20 { // don't keep huge buffers around
			bufPool.Put(buf)
		}
	}()
	if err := t.Execute(buf, data); err != nil {
		log.Printf("render %s: %v", name, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", "text/html; charset=utf-8")
	}
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

// popularQueries are suggested on the empty search state.
var popularQueries = []string{"shalat", "puasa ramadhan", "sedekah", "sabar", "niat", "ilmu"}

// ---------------------------------------------------------------------------
// Search URL helpers used by templates.
// ---------------------------------------------------------------------------

func urlEncode(s string) string { return url.QueryEscape(s) }

func hasFilter(slice []string, item string) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// buildFilterURL builds a /search URL that preserves every active filter.
// Collections are comma-joined (parseSearchFilters accepts both the comma and
// the repeated-parameter form).
func buildFilterURL(query string, filters SearchFilters, page int) string {
	v := url.Values{}
	if query != "" {
		v.Set("q", query)
	}
	if filters.Language != "" && filters.Language != "all" {
		v.Set("lang", filters.Language)
	}
	if filters.SortBy != "" && filters.SortBy != "relevance" {
		v.Set("sort", filters.SortBy)
	}
	if len(filters.Collections) > 0 {
		v.Set("collections", strings.Join(filters.Collections, ","))
	}
	if filters.NumberRange.Min > 0 {
		v.Set("min", strconv.Itoa(filters.NumberRange.Min))
	}
	if filters.NumberRange.Max > 0 {
		v.Set("max", strconv.Itoa(filters.NumberRange.Max))
	}
	if filters.TextSearch {
		v.Set("text", "1")
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return "/search"
	}
	return "/search?" + v.Encode()
}

// ---------------------------------------------------------------------------
// gzip middleware (stdlib only). Brotli is not in the standard library; a
// reverse proxy or Cloudflare in front of the app can add br if wanted.
// ---------------------------------------------------------------------------

var gzipPool = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, 5)
	return w
}}

var compressibleTypes = []string{
	"text/html", "text/css", "application/javascript", "text/javascript",
	"application/json", "application/xml", "text/xml", "text/plain",
	"application/manifest+json",
}

func isCompressible(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	for _, t := range compressibleTypes {
		if ct == t {
			return true
		}
	}
	return false
}

const (
	gzipSniffLen = 1024 // bytes buffered before deciding
	gzipMinLen   = 256  // smaller complete responses are sent as-is
)

type gzipResponseWriter struct {
	http.ResponseWriter
	gz       *gzip.Writer
	buf      []byte
	status   int
	decided  bool
	compress bool
}

func (g *gzipResponseWriter) WriteHeader(code int) {
	if g.decided || g.status != 0 {
		return
	}
	g.status = code
	if code == http.StatusNoContent || code == http.StatusNotModified || code < 200 {
		g.decide(false)
	}
}

func (g *gzipResponseWriter) decide(final bool) {
	if g.decided {
		return
	}
	g.decided = true
	if g.status == 0 {
		g.status = http.StatusOK
	}
	h := g.Header()
	ct := h.Get("Content-Type")
	if ct == "" && len(g.buf) > 0 {
		ct = http.DetectContentType(g.buf)
		h.Set("Content-Type", ct)
	}
	g.compress = g.status != http.StatusNoContent && g.status != http.StatusNotModified && g.status >= 200 &&
		h.Get("Content-Encoding") == "" && isCompressible(ct) && !(final && len(g.buf) < gzipMinLen)
	if g.compress {
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		g.gz = gzipPool.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(g.status)
	if len(g.buf) > 0 {
		if g.compress {
			g.gz.Write(g.buf)
		} else {
			g.ResponseWriter.Write(g.buf)
		}
		g.buf = nil
	}
}

func (g *gzipResponseWriter) Write(p []byte) (int, error) {
	if !g.decided {
		g.buf = append(g.buf, p...)
		if len(g.buf) >= gzipSniffLen {
			g.decide(false)
		}
		return len(p), nil
	}
	if g.compress {
		return g.gz.Write(p)
	}
	return g.ResponseWriter.Write(p)
}

// Flush passes through http.Flusher.
func (g *gzipResponseWriter) Flush() {
	g.decide(false)
	if g.gz != nil {
		g.gz.Flush()
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (g *gzipResponseWriter) close() {
	g.decide(true)
	if g.gz != nil {
		g.gz.Close()
		g.gz.Reset(io.Discard)
		gzipPool.Put(g.gz)
		g.gz = nil
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *gzipResponseWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

func gzipMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Accept-Encoding")
		if r.Method == http.MethodHead || r.Header.Get("Range") != "" ||
			!strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponseWriter{ResponseWriter: w}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// securityHeaders adds safe defaults. No CSP on purpose: it would break
// AdSense, Midtrans Snap and the CDN scripts.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Static asset versioning: /static/x?v=<hash> is cached for a year (immutable).
// ---------------------------------------------------------------------------

var (
	assetHashes   = map[string]string{}
	assetsVersion string // hash over all assets (part of HTML ETags)
	templatesHash string
	bootID        = strconv.FormatInt(time.Now().UnixNano(), 36)
)

// hashDir returns a combined hash of every file in dir and, if into is not
// nil, records each file's sha256 (first 10 hex chars) by relative path.
func hashDir(dir string, into map[string]string) string {
	all := fnv.New64a()
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		h := hex.EncodeToString(sum[:])[:10]
		if into != nil {
			rel, _ := filepath.Rel(dir, path)
			into[filepath.ToSlash(rel)] = h
		}
		all.Write([]byte(path + h))
		return nil
	})
	return strconv.FormatUint(all.Sum64(), 36)
}

func loadAssetHashes() {
	m := map[string]string{}
	assetsVersion = hashDir("static", m)
	assetHashes = m
	templatesHash = hashDir("templates", nil)
}

// asset returns a versioned URL for a file in static/.
func asset(name string) string {
	if h, ok := assetHashes[name]; ok {
		return "/static/" + name + "?v=" + h
	}
	return "/static/" + name
}

func staticHandler(dir string) http.Handler {
	fileServer := http.StripPrefix("/static/", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/static/")
		if name == "" || strings.HasSuffix(name, "/") {
			notFoundHandler(w, r) // no directory listings
			return
		}
		if v := r.URL.Query().Get("v"); v != "" && v == assetHashes[name] {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=86400")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// HTML caching: Cache-Control + weak ETag, 304 on If-None-Match.
// ---------------------------------------------------------------------------

const (
	ccPage    = "public, max-age=300, stale-while-revalidate=3600"
	ccNoCache = "no-cache"
	ccSearch  = "public, max-age=120"
	ccLong    = "public, max-age=86400"
)

// cacheable sets Cache-Control and an ETag derived from the templates, the
// static assets, this process (so a deploy always invalidates), the request
// URL and any extra parts (e.g. the collection's data generation). It returns
// true after writing 304 Not Modified.
func cacheable(w http.ResponseWriter, r *http.Request, cacheControl string, parts ...string) bool {
	h := fnv.New64a()
	for _, p := range append([]string{templatesHash, assetsVersion, bootID, r.URL.RequestURI()}, parts...) {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	etag := `W/"` + strconv.FormatUint(h.Sum64(), 36) + `"`
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("ETag", etag)
	if devTemplates {
		return false
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, t := range strings.Split(inm, ",") {
			t = strings.TrimSpace(t)
			if t == "*" || strings.TrimPrefix(t, "W/") == strings.TrimPrefix(etag, "W/") {
				w.WriteHeader(http.StatusNotModified)
				return true
			}
		}
	}
	return false
}

// withCacheControl wraps a handler with a fixed Cache-Control header.
func withCacheControl(cc string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", cc)
		h(w, r)
	}
}

// serviceWorkerHandler serves static/service-worker.js with the current asset
// version and the versioned precache list filled in.
func serviceWorkerHandler(w http.ResponseWriter, r *http.Request) {
	b, err := os.ReadFile("static/service-worker.js")
	if err != nil {
		notFoundHandler(w, r)
		return
	}
	var urls []string
	for _, name := range []string{"style.css", "script.js", "favorites.js", "logo.png", "patter_ketupat.png"} {
		urls = append(urls, strconv.Quote(asset(name)))
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "const ASSET_VERSION ="):
			lines[i] = "const ASSET_VERSION = " + strconv.Quote(assetsVersion) + ";"
		case strings.HasPrefix(l, "const PRECACHE_ASSETS ="):
			lines[i] = "const PRECACHE_ASSETS = [" + strings.Join(urls, ", ") + "];"
		}
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", ccNoCache)
	w.Write([]byte(strings.Join(lines, "\n")))
}
