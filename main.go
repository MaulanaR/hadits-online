package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	midtrans "github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/snap"
)

type Collection struct {
	Number      int    `json:"number"`
	Arab        string `json:"arab"`
	ID          string `json:"id"`
	Explanation string `json:"explanation,omitempty"`
}

type CollectionInfo struct {
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Total int    `json:"total"`
}

type HadithData struct {
	// Only keep collection info in memory (very small ~10KB)
	Info []CollectionInfo
}

type Pagination struct {
	CurrentPage int
	TotalPages  int
	PerPage     int
	TotalItems  int
	HasNext     bool
	HasPrev     bool
}

type SearchFilters struct {
	TextSearch  bool        `json:"-"` // ?text=1: run the text search even for "bukhari 1"-style queries
	Query       string      `json:"query"`
	Collections []string    `json:"collections"`
	Language    string      `json:"language"` // "ar", "id", "all"
	NumberRange NumberRange `json:"numberRange"`
	SortBy      string      `json:"sortBy"` // "relevance", "number", "collection"
}

type NumberRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

var (
	data                *HadithData
	geminiApiKey        string
	midtransServerKey   string
	midtransClientKey   string
	midtransEnvironment string
)

const (
	ItemsPerPage = 20
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}
	geminiApiKey = os.Getenv("GEMINI_API_KEY")
	midtransServerKey = os.Getenv("MIDTRANS_SERVER_KEY")
	midtransClientKey = os.Getenv("MIDTRANS_CLIENT_KEY")
	midtransEnvironment = os.Getenv("MIDTRANS_ENVIRONMENT")

	// Initialize Midtrans Snap client
	if midtransServerKey != "" {
		var environment midtrans.EnvironmentType
		if midtransEnvironment == "production" {
			environment = midtrans.Production
		} else {
			environment = midtrans.Sandbox
		}
		midtrans.ServerKey = midtransServerKey
		midtrans.Environment = environment
	}
}

func loadData() {
	data = &HadithData{
		Info: []CollectionInfo{},
	}

	// Load only collection list (very small ~10KB)
	listData, err := ioutil.ReadFile("resource/list.json")
	if err != nil {
		log.Fatal("Error loading list.json:", err)
	}
	err = json.Unmarshal(listData, &data.Info)
	if err != nil {
		log.Fatal("Error parsing list.json:", err)
	}

	log.Printf("Loaded %d collection info (disk-based mode - very low memory usage)", len(data.Info))
}

// Template functions
func add(a, b int) int {
	return a + b
}

func add1(a int) int {
	return a + 1
}

func subtract(a, b int) int {
	return a - b
}

func multiply(a, b int) int {
	return a * b
}

// reBracket matches narrator names written as [Name] in the Indonesian text.
var reBracket = regexp.MustCompile(`\[(.*?)\]`)

// formatHadith escapes the text first and only then turns [..] into <strong>..</strong>,
// so data can never inject markup.
func formatHadith(s string) template.HTML {
	return template.HTML(reBracket.ReplaceAllString(html.EscapeString(s), "<strong>$1</strong>"))
}

func pageNumbers(current, total int) []int {
	var pages []int
	start := current - 2
	if start < 1 {
		start = 1
	}
	end := start + 4
	if end > total {
		end = total
		start = end - 4
		if start < 1 {
			start = 1
		}
	}

	for page := start; page <= end; page++ {
		pages = append(pages, page)
	}
	return pages
}

func formatRupiah(amount float64) string {
	return fmt.Sprintf("Rp %.0f", amount)
}

func calculatePercentage(current float64, target float64) float64 {
	if target <= 0 {
		return 0
	}
	percent := (current / target) * 100
	if percent > 100 {
		return 100
	}
	return percent
}

func safeHTML(s string) template.HTML {
	return template.HTML(s)
}

type PageData struct {
	SEO        SEOData
	Data       interface{}
	Info       *CollectionInfo
	Hadith     *Collection
	PrevHadith *Collection
	NextHadith *Collection
	Collection string
	Pagination Pagination
	Daily      *DailyHadith
}

func favoritesHandler(w http.ResponseWriter, r *http.Request) {
	if cacheable(w, r, ccNoCache) {
		return
	}
	seo := SEOData{
		Title:       "Hadits Favorit Saya - hadits.online",
		Description: "Daftar hadits pilihan yang Anda simpan untuk dipelajari lebih lanjut.",
		Keywords:    "hadits favorit, simpan hadits, belajar islam",
		Canonical:   siteURL + "/favorites",
		Robots:      "noindex, follow",
	}
	render(w, "favorites.html", http.StatusOK, PageData{SEO: seo.withDefaults()})
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	if cacheable(w, r, ccPage, dailyKey(now), strconv.FormatUint(dataGen.Load(), 10)) {
		return
	}
	seo := SEOData{
		Title:       "hadits.online - Pusat Belajar Hadits Terlengkap Bahasa Indonesia",
		Description: "Cari dan pelajari ribuan hadits shahih dari Bukhari, Muslim, Abu Daud, dan kitab lainnya dengan terjemahan Indonesia yang akurat.",
		Keywords:    "hadits online, hadits shahih, bukhari, muslim, terjemahan hadits, belajar islam",
		Canonical:   siteURL + "/",
		JSONLD:      homeJSONLD(),
	}
	render(w, "index.html", http.StatusOK, PageData{SEO: seo.withDefaults(), Data: data.Info, Daily: dailyHadith(now)})
}

func collectionHandler(w http.ResponseWriter, r *http.Request) {
	slug := mux.Vars(r)["slug"]
	c, ok := getBySlug(slug)
	if !ok {
		notFoundHandler(w, r)
		return
	}
	info := &c.Info
	base := "/collection/" + slug
	totalItems := c.count()
	totalPages := max(1, (totalItems+ItemsPerPage-1)/ItemsPerPage)

	// ?page=N: invalid -> 301 to the first page, past the end -> 301 to the
	// last page (no duplicate content).
	page := 1
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		p, err := strconv.Atoi(pageStr)
		switch {
		case err != nil || p < 1:
			http.Redirect(w, r, base, http.StatusFound)
			return
		case p > totalPages:
			http.Redirect(w, r, base+"?page="+strconv.Itoa(totalPages), http.StatusFound)
			return
		}
		page = p
	}
	if cacheable(w, r, ccPage, strconv.FormatUint(c.generation(), 10)) {
		return
	}

	startIndex := (page - 1) * ItemsPerPage
	endIndex := min(startIndex+ItemsPerPage, totalItems)
	var paginatedHadiths []Collection
	if endIndex > startIndex {
		var err error
		paginatedHadiths, err = c.readRange(startIndex, endIndex)
		if err != nil {
			log.Printf("collection %s page %d: %v", slug, page, err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	pagination := Pagination{
		CurrentPage: page,
		TotalPages:  totalPages,
		PerPage:     ItemsPerPage,
		TotalItems:  totalItems,
		HasNext:     page < totalPages,
		HasPrev:     page > 1,
	}

	pageURL := func(p int) string {
		if p <= 1 {
			return siteURL + base
		}
		return siteURL + base + "?page=" + strconv.Itoa(p)
	}
	description := fmt.Sprintf("Daftar lengkap hadits dari kitab %s. Tersedia %d hadits dengan terjemahan Indonesia.", info.Name, info.Total)
	title := fmt.Sprintf("Koleksi Hadits %s - hadits.online", info.Name)
	if page > 1 {
		title = fmt.Sprintf("Koleksi Hadits %s – Halaman %d - hadits.online", info.Name, page)
		description = fmt.Sprintf("Hadits %s halaman %d dari %d: nomor %d sampai %d dengan teks Arab dan terjemahan Indonesia.",
			info.Name, page, totalPages, paginatedHadiths[0].Number, paginatedHadiths[len(paginatedHadiths)-1].Number)
	}
	seo := SEOData{
		Title:       title,
		Description: description,
		Keywords:    fmt.Sprintf("hadits %s, kitab %s, kumpulan hadits", info.Name, info.Name),
		Canonical:   pageURL(page),
		JSONLD:      collectionJSONLD(info, pageURL(page), description),
	}
	if page > 1 {
		seo.PrevURL = pageURL(page - 1)
	}
	if page < totalPages {
		seo.NextURL = pageURL(page + 1)
	}

	render(w, "collection.html", http.StatusOK, PageData{
		SEO:        seo.withDefaults(),
		Info:       info,
		Data:       paginatedHadiths,
		Collection: slug,
		Pagination: pagination,
	})
}

// HadithPage is the data of templates/hadith.html.
type HadithPage struct {
	PageData
	Matan string
}

func hadithHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	slug := vars["slug"]
	c, ok := getBySlug(slug)
	if !ok {
		notFoundHandler(w, r)
		return
	}
	info := &c.Info

	number, err := strconv.Atoi(vars["number"])
	if err != nil {
		notFoundHandler(w, r)
		return
	}
	idx, found := c.indexOf(number)
	if !found {
		notFoundHandler(w, r)
		return
	}
	if cacheable(w, r, ccPage, strconv.FormatUint(c.generation(), 10)) {
		return
	}
	h, err := c.read(idx)
	if err != nil {
		log.Printf("hadith %s/%d: %v", slug, number, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	hadith := &h

	// Prev/next only need the numbers (the template uses .Number only).
	var prevHadith, nextHadith *Collection
	if idx > 0 {
		prevHadith = &Collection{Number: c.numberAt(idx - 1)}
	}
	if idx < c.count()-1 {
		nextHadith = &Collection{Number: c.numberAt(idx + 1)}
	}

	matan := matanOf(hadith.ID)
	title := fmt.Sprintf("Hadits %s No. %d | hadits.online", info.Name, hadith.Number)
	if matan != "" {
		title = fmt.Sprintf("%s – HR. %s No. %d | hadits.online", truncateRunes(matan, 55), info.Name, hadith.Number)
	}
	pageURL := fmt.Sprintf("%s/collection/%s/%d", siteURL, slug, hadith.Number)
	seo := SEOData{
		Title:       title,
		Description: truncateRunes(matan, 155),
		Keywords:    fmt.Sprintf("hadits %s %d, %s no %d, HR %s", info.Name, hadith.Number, info.Name, hadith.Number, info.Name),
		Canonical:   pageURL,
		OGType:      "article",
		JSONLD:      hadithJSONLD(info, hadith, pageURL, matan),
	}

	render(w, "hadith.html", http.StatusOK, HadithPage{
		PageData: PageData{
			SEO:        seo.withDefaults(),
			Info:       info,
			Hadith:     hadith,
			PrevHadith: prevHadith,
			NextHadith: nextHadith,
		},
		Matan: matan,
	})
}

// notFoundHandler renders the 404 template with a 404 status.
func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	seo := SEOData{
		Title:       "Halaman Tidak Ditemukan - hadits.online",
		Description: "Maaf, halaman yang Anda cari tidak dapat ditemukan.",
		Robots:      "noindex, follow",
	}
	// Offer the collection when a hadith number inside it was not found.
	var back *CollectionInfo
	if slug := mux.Vars(r)["slug"]; slug != "" {
		if c, ok := getBySlug(slug); ok {
			back = &c.Info
		}
	}
	w.Header().Set("Cache-Control", "no-cache")
	render(w, "404.html", http.StatusNotFound, struct {
		SEO        SEOData
		Path       string
		Collection *CollectionInfo
	}{SEO: seo.withDefaults(), Path: r.URL.Path, Collection: back})
}

// newRouter registers every route and wraps them in the middleware chain.
func newRouter() http.Handler {
	r := mux.NewRouter()
	r.StrictSlash(true) // "/faq/" -> 301 "/faq"

	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/index.html", redirectHome)
	r.HandleFunc("/favorites", favoritesHandler)
	r.HandleFunc("/collection/{slug}", collectionHandler)
	r.HandleFunc("/collection/{slug}/{number}", hadithHandler)
	r.HandleFunc("/search", searchHandler)
	r.HandleFunc("/api/explain", explainHandler)
	r.HandleFunc("/donate", donateHandler)
	r.HandleFunc("/api/midtrans/token", withCacheControl("no-store", midtransTokenHandler))
	r.HandleFunc("/faq", faqHandler)
	r.HandleFunc("/robots.txt", withCacheControl(ccLong, robotsHandler))
	r.HandleFunc("/sitemap.xml", withCacheControl(ccLong, sitemapHandler))
	r.HandleFunc("/sitemap-{name:[a-z-]+}.xml", withCacheControl(ccLong, sitemapPartHandler))
	r.HandleFunc("/manifest.json", withCacheControl(ccLong, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/manifest.json")
	}))
	r.HandleFunc("/service-worker.js", serviceWorkerHandler)
	r.PathPrefix("/static/").Handler(staticHandler("static"))
	r.NotFoundHandler = http.HandlerFunc(notFoundHandler)
	return securityHeaders(gzipMiddleware(r))

}

func main() {
	log.Println("Loading hadith data...")
	loadAssetHashes()
	loadTemplates()
	loadData()
	initStore()
	searchCache.startStatsLogger(10 * time.Minute)
	explainLimiter.startJanitor(10 * time.Minute)
	log.Println("Data loaded successfully")

	// LISTEN_ADDR (e.g. "127.0.0.1:8082") wins; otherwise ":" + PORT (default 8082).
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		port := os.Getenv("PORT")
		if port == "" {
			port = "8082"
		}
		addr = ":" + port
	}
	// Deployment tip: set GOMEMLIMIT (e.g. GOMEMLIMIT=200MiB) so the GC keeps
	// the heap well inside the container's memory limit.
	srv := &http.Server{
		Addr:              addr,
		Handler:           newRouter(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      4 * time.Minute, // AI explanations can take up to 3 minutes
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	go func() {
		log.Println("Server starting on " + addr)
		if strings.HasPrefix(addr, ":") {
			log.Println("Access the application at: http://localhost" + addr)
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	<-ctx.Done()
	stop()
	log.Println("Shutting down (waiting up to 10s for active requests)...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Shutdown: %v", err)
	}
}

type GeminiPart struct {
	Text string `json:"text"`
}

type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
}

type GeminiRequest struct {
	Contents          []GeminiContent `json:"contents"`
	SystemInstruction GeminiContent   `json:"system_instruction,omitempty"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content GeminiContent `json:"content"`
	} `json:"candidates"`
}

var (
	explainFlight flightGroup[string]
	// 5 uncached explanations per IP per 10 minutes, 60 per hour overall.
	explainLimiter = newRateLimiter(5, 10*time.Minute, 60, time.Hour)
	geminiURL      = "https://generativelanguage.googleapis.com/v1beta/models/gemini-3-flash-preview:generateContent"
	geminiClient   = &http.Client{Timeout: 3 * time.Minute}
)

// explainSystemPrompt is the Gemini system instruction (kept verbatim).
const explainSystemPrompt = `Role: Anda adalah seorang Ahli Ilmu Hadis dan Syarah (Penjelas) Hadis Digital yang akurat, objektif, dan mendalam.

Task: Tugas Anda adalah membedah teks hadis yang diberikan pengguna ke dalam struktur penjelasan yang baku dan mudah dipahami.

Output Structure:
Setiap jawaban WAJIB mengikuti format berikut:

1. Disclaimer (WAJIB di posisi paling atas):
   - Gunakan format Blockquote atau Italic: "> ⚠️ Disclaimer: Respons ini dihasilkan oleh AI hanya sebagai bahan pertimbangan, diskusi, atau wawasan tambahan. Jawaban ini bukan merupakan fatwa hukum dan tidak bisa dijadikan rujukan utama dalam beragama. Silakan berkonsultasi dengan ulama atau ahli agama yang kredibel untuk keputusan hukum yang mengikat."

2. Identitas & Takhrij: 
   - Sebutkan perawi utama (Sahabat yang meriwayatkan).
   - Sebutkan derajat hadis (Sahih/Hasan/Dhaif) beserta sumber kitabnya (Misal: HR. Bukhari no. 123).

3. Makna Lughawi (Bahasa): 
   - Jelaskan istilah sulit atau kata kunci secara ringkas.

4. Asbabul Wurud (Konteks): 
   - Jelaskan latar belakang hadis atau konteks umum tema tersebut.

5. Istinbath (Pelajaran & Hukum): 
   - Berikan poin-poin ilmu, hukum, etika, atau hikmah yang terkandung.

6. Relevansi Kontemporer: 
   - Penerapan praktis dalam kehidupan modern.

7. Sumber Rujukan:
   - Sebutkan kitab Syarah yang digunakan (Contoh: Fathul Bari, Syarah An-Nawawi, dll).

Guiding Principles:
- Jika hadis berstatus Dhaif (lemah), berikan catatan tambahan setelah disclaimer.
- Tetap bersandar pada literatur klasik (Turats) dan pendapat ulama otoritatif.
- Gunakan Markdown untuk struktur yang rapi.`

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]string{"error": msg})
}

// explainHandler returns {"explanation": "..."}: the stored explanation when one
// exists, otherwise a freshly generated one (Gemini) that is written back to
// resource/{slug}.json. Generation is deduplicated per hadith and rate-limited.
func explainHandler(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	slug, numberStr := q.Get("slug"), q.Get("number")
	if slug == "" || numberStr == "" {
		jsonError(w, http.StatusBadRequest, "Missing parameters")
		return
	}
	// Only slugs from list.json are accepted (no path traversal).
	c, ok := getBySlug(slug)
	if !ok {
		jsonError(w, http.StatusNotFound, "Collection not found")
		return
	}
	number, err := strconv.Atoi(numberStr)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "Invalid hadith number")
		return
	}
	idx, found := c.indexOf(number)
	if !found {
		jsonError(w, http.StatusNotFound, "Hadith not found")
		return
	}
	hadith, err := c.read(idx)
	if err != nil {
		log.Printf("explain %s/%d: %v", slug, number, err)
		jsonError(w, http.StatusInternalServerError, "Error loading hadith")
		return
	}

	// Already explained: serve it (no AI call, cacheable).
	if hadith.Explanation != "" {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		writeJSON(w, http.StatusOK, map[string]string{"explanation": hadith.Explanation})
		return
	}

	if geminiApiKey == "" {
		jsonError(w, http.StatusServiceUnavailable, "AI configuration missing")
		return
	}

	key := slug + "/" + strconv.Itoa(number)
	// Requests that join an in-flight generation don't spend rate-limit tokens.
	if !explainFlight.InFlight(key) && !explainLimiter.allow(clientIP(r), time.Now()) {
		jsonError(w, http.StatusTooManyRequests, "Batas permintaan AI tercapai, coba beberapa menit lagi.")
		return
	}

	explanation, err, _ := explainFlight.Do(key, func() (string, error) {
		return generateExplanation(c, hadith)
	})
	if err != nil {
		log.Printf("explain %s: %v", key, err)
		jsonError(w, http.StatusBadGateway, "Error calling AI API")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"explanation": explanation})
}

// generateExplanation calls Gemini and persists the result. It uses its own
// timeout context (not the request's) because the result is shared by every
// deduplicated caller and is saved even if the first client disconnects.
func generateExplanation(c *collStore, hadith Collection) (string, error) {
	// Another flight may have finished between our read and now.
	if idx, ok := c.indexOf(hadith.Number); ok {
		if cur, err := c.read(idx); err == nil && cur.Explanation != "" {
			return cur.Explanation, nil
		}
	}

	userPrompt := fmt.Sprintf("Kitab: %s\nNomor: %d \n\nArab:\n%s",
		c.Info.Name, hadith.Number, hadith.Arab)

	geminiReq := GeminiRequest{
		Contents: []GeminiContent{
			{Parts: []GeminiPart{{Text: userPrompt}}},
		},
		SystemInstruction: GeminiContent{
			Parts: []GeminiPart{{Text: explainSystemPrompt}},
		},
	}
	jsonData, err := json.Marshal(geminiReq)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, geminiURL, bytes.NewReader(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("x-goog-api-key", geminiApiKey)

	resp, err := geminiClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gemini request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("gemini read: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(body)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "..."
		}
		return "", fmt.Errorf("gemini status %d: %s", resp.StatusCode, snippet)
	}

	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		return "", fmt.Errorf("gemini parse: %w", err)
	}
	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		return "", errors.New("no response from AI")
	}
	explanation := geminiResp.Candidates[0].Content.Parts[0].Text
	if strings.TrimSpace(explanation) == "" {
		return "", errors.New("empty response from AI")
	}

	if err := c.setExplanation(hadith.Number, explanation); err != nil {
		// Still return the text; it just won't be cached on disk.
		log.Printf("Error saving explanation %s/%d: %v", c.Info.Slug, hadith.Number, err)
	}
	return explanation, nil
}

// Donation page handler
func donateHandler(w http.ResponseWriter, r *http.Request) {
	seo := SEOData{
		Title:       "Donasi & Dukung Kami - hadits.online",
		Description: "Bantu kami menjaga keberlangsungan layanan Hadits.online agar tetap gratis bagi umat Islam.",
		Keywords:    "donasi islam, dukung dakwah digital, hadits online",
		Canonical:   siteURL + "/donate",
	}.withDefaults()

	pageData := struct {
		SEO         SEOData
		ClientKey   string
		Environment string
	}{
		SEO:         seo,
		ClientKey:   midtransClientKey,
		Environment: midtransEnvironment,
	}

	if cacheable(w, r, ccNoCache) {
		return
	}
	render(w, "donate.html", http.StatusOK, pageData)
}

// Midtrans Snap token handler
func midtransTokenHandler(w http.ResponseWriter, r *http.Request) {
	if midtransServerKey == "" {
		http.Error(w, "Midtrans not configured", http.StatusServiceUnavailable)
		return
	}

	var reqBody struct {
		Amount     int    `json:"amount"`
		DonorName  string `json:"donor_name"`
		DonorEmail string `json:"donor_email"`
		DonorPhone string `json:"donor_phone"`
	}

	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Validate amount
	if reqBody.Amount < 10000 {
		http.Error(w, "Minimum donation amount is Rp 10.000", http.StatusBadRequest)
		return
	}

	// Generate unique order ID
	orderID := fmt.Sprintf("DONATION-%d-%d", time.Now().Unix(), reqBody.Amount)

	// Create Snap request
	req := &snap.Request{
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  orderID,
			GrossAmt: int64(reqBody.Amount),
		},
		CustomerDetail: &midtrans.CustomerDetails{
			FName: strings.Split(reqBody.DonorName, " ")[0],
			Email: reqBody.DonorEmail,
			Phone: reqBody.DonorPhone,
		},
		Items: &[]midtrans.ItemDetails{
			{
				ID:    "DONATION",
				Price: int64(reqBody.Amount),
				Qty:   1,
				Name:  "Donasi Hadits Online",
			},
		},
	}

	// Create Snap transaction
	token, err := snap.CreateTransactionToken(req)
	if err != nil {
		log.Printf("Error creating Midtrans transaction: %v", err)
		http.Error(w, "Failed to create transaction", http.StatusInternalServerError)
		return
	}

	response := map[string]string{
		"token":    token,
		"order_id": orderID,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// FAQ page handler
func faqHandler(w http.ResponseWriter, r *http.Request) {
	seo := SEOData{
		Title:       "Pertanyaan Umum (FAQ) - hadits.online",
		Description: "Jawaban atas pertanyaan yang sering diajukan mengenai penggunaan dan fitur Hadits.online.",
		Keywords:    "faq hadits online, bantuan hadits",
		Canonical:   siteURL + "/faq",
		JSONLD:      faqJSONLD(),
	}.withDefaults()

	pageData := PageData{
		SEO: seo,
	}

	if cacheable(w, r, ccPage) {
		return
	}
	render(w, "faq.html", http.StatusOK, pageData)
}
