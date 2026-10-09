package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// TestRoutesSmoke renders every page against the real data (skipped in -short).
func TestRoutesSmoke(t *testing.T) {
	setupRealSearch(t)
	loadAssetHashes()
	loadTemplates()
	oldKey := geminiApiKey
	geminiApiKey = "" // never call Gemini from tests
	defer func() { geminiApiKey = oldKey }()
	srv := httptest.NewServer(newRouter())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	reH1 := regexp.MustCompile(`<h1\b`)
	cases := []struct {
		path   string
		status int
	}{
		{"/", 200}, {"/index.html", 301}, {"/favorites", 200}, {"/faq", 200}, {"/faq/", 301}, {"/donate", 200},
		{"/collection/bukhari", 200}, {"/collection/bukhari?page=2", 200}, {"/collection/bukhari?page=0", 302},
		{"/collection/bukhari?page=99999", 302}, {"/collection/bukhari/1", 200}, {"/collection/bukhari/2977", 200},
		{"/collection/bukhari/2978", 404}, {"/collection/bukhari/x", 404}, {"/collection/nope", 404}, {"/collection/nope/1", 404},
		{"/search?q=niat", 200}, {"/search?q=%28", 200}, {"/search?q=a%26b%23c", 200}, {"/search?q=", 307},
		{"/search?q=zzzqqq", 200}, {"/search?q=bukhari+1", 200}, {"/search?q=bukhari+1&text=1", 200}, {"/search?q=niat&page=9999", 200},
		{"/robots.txt", 200}, {"/sitemap.xml", 200}, {"/sitemap-pages.xml", 200}, {"/sitemap-bukhari.xml", 200}, {"/sitemap-nope.xml", 404},
		{"/manifest.json", 200}, {"/service-worker.js", 200}, {"/static/style.css", 200}, {"/static/", 404}, {"/nope", 404},
		{"/api/explain?slug=bukhari&number=1", 200}, {"/api/explain?slug=../go&number=1", 404},
	}
	for _, c := range cases {
		req, _ := http.NewRequest("GET", srv.URL+c.path, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		var body io.Reader = resp.Body
		if resp.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(resp.Body)
			if err != nil {
				t.Fatalf("%s: bad gzip: %v", c.path, err)
			}
			body = zr
		}
		b, _ := io.ReadAll(body)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s: status %d, want %d", c.path, resp.StatusCode, c.status)
			continue
		}
		if c.path == "/search?q=bukhari+1" && (!strings.Contains(string(b), "data-direct-only") || strings.Contains(string(b), "<article")) {
			t.Errorf("%s: expected direct hit only", c.path)
		}
		if c.path == "/search?q=bukhari+1&text=1" && (!strings.Contains(string(b), "<article") || !strings.Contains(string(b), "text=1")) {
			t.Errorf("%s: expected text results with text=1 kept in links", c.path)
		}
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && resp.StatusCode/100 != 3 {
			s := string(b)
			if n := len(reH1.FindAllString(s, -1)); n != 1 {
				t.Errorf("%s: %d <h1>", c.path, n)
			}
			if i := strings.LastIndex(s, "</html>"); i < 0 || strings.TrimSpace(s[i+len("</html>"):]) != "" {
				t.Errorf("%s: content after </html>", c.path)
			}
			if strings.Count(s, "<body") != 1 || strings.Count(s, "</body>") != 1 {
				t.Errorf("%s: body tags %d/%d", c.path, strings.Count(s, "<body"), strings.Count(s, "</body>"))
			}
		}
	}
}
