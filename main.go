package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
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
	Collections map[string][]Collection
	Info        []CollectionInfo
	mu          sync.RWMutex
}

type Pagination struct {
	CurrentPage int
	TotalPages  int
	PerPage     int
	TotalItems  int
	HasNext     bool
	HasPrev     bool
}

type SearchResult struct {
	Collection string     `json:"collection"`
	Slug       string     `json:"slug"`
	Hadith     Collection `json:"hadith"`
	Context    string     `json:"context"`
	Score      int        `json:"score"` // For relevance scoring
}

type SearchFilters struct {
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

type FilteredResults struct {
	Query       string
	Filters     SearchFilters
	Results     []SearchResult
	Pagination  Pagination
	TotalItems  int
	Collections []CollectionInfo
}

type MayarProductResponse struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		ID          string  `json:"id"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		Amount      float64 `json:"amount"`
		Link        string  `json:"link"`
		Image       string  `json:"image"`
		TotalSales  float64 `json:"totalSales"`
		TotalOrders int     `json:"totalOrders"`
	} `json:"data"`
}

var (
	data           *HadithData
	tmpl           *template.Template
	mayarApiKey    string
	mayarProductId string
	geminiApiKey   string
)

const (
	ItemsPerPage = 20
)

func init() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found")
	}
	mayarApiKey = os.Getenv("MAYAR_API_KEY")
	mayarProductId = os.Getenv("MAYAR_PRODUCT_ID")
	geminiApiKey = os.Getenv("GEMINI_API_KEY")
}

func getMayarProductDetail() (*MayarProductResponse, error) {
	if mayarApiKey == "" || mayarProductId == "" {
		return nil, fmt.Errorf("Mayar API credentials missing")
	}

	url := fmt.Sprintf("https://api.mayar.id/hl/v1/product/%s", mayarProductId)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Add("Authorization", "Bearer "+mayarApiKey)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Mayar API returned status: %d", resp.StatusCode)
	}

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var mayarResp MayarProductResponse
	if err := json.Unmarshal(body, &mayarResp); err != nil {
		return nil, err
	}

	return &mayarResp, nil
}

func loadData() {
	data = &HadithData{
		Collections: make(map[string][]Collection),
	}

	// Load collection list
	listData, err := ioutil.ReadFile("resource/list.json")
	if err != nil {
		log.Fatal("Error loading list.json:", err)
	}
	err = json.Unmarshal(listData, &data.Info)
	if err != nil {
		log.Fatal("Error parsing list.json:", err)
	}

	// Load all collections
	for _, info := range data.Info {
		filename := "resource/" + info.Slug + ".json"
		collectionData, err := ioutil.ReadFile(filename)
		if err != nil {
			log.Printf("Error loading %s: %v", filename, err)
			continue
		}

		var collection []Collection
		err = json.Unmarshal(collectionData, &collection)
		if err != nil {
			log.Printf("Error parsing %s: %v", filename, err)
			continue
		}

		data.Collections[info.Slug] = collection
		log.Printf("Loaded %d hadith from %s", len(collection), info.Name)
	}
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

func formatHadith(s string) template.HTML {
	re := regexp.MustCompile(`\[(.*?)\]`)
	result := re.ReplaceAllString(s, "<strong>$1</strong>")
	return template.HTML(result)
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

type SEOData struct {
	Title       string
	Description string
	Keywords    string
	OGImage     string
	OGUrl       string
	Canonical   string
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
}

func favoritesHandler(w http.ResponseWriter, r *http.Request) {
	seo := SEOData{
		Title:       "Hadits Favorit Saya - hadits.online",
		Description: "Daftar hadits pilihan yang Anda simpan untuk dipelajari lebih lanjut.",
		Keywords:    "hadits favorit, simpan hadits, belajar islam",
		OGUrl:       "https://hadits.online/favorites",
		Canonical:   "https://hadits.online/favorites",
	}

	pageData := PageData{
		SEO: seo,
	}

	tmpl := template.Must(template.ParseFiles("templates/favorites.html", "templates/components/navbar.html", "templates/components/footer.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	// Create a template with custom functions
	funcMap := template.FuncMap{
		"add":          add,
		"add1":         add1,
		"subtract":     subtract,
		"multiply":     multiply,
		"pageNumbers":  pageNumbers,
		"formatHadith": formatHadith,
	}

	seo := SEOData{
		Title:       "hadits.online - Pusat Belajar Hadits Terlengkap Bahasa Indonesia",
		Description: "Cari dan pelajari ribuan hadits shahih dari Bukhari, Muslim, Abu Daud, dan kitab lainnya dengan terjemahan Indonesia yang akurat.",
		Keywords:    "hadits online, hadits shahih, bukhari, muslim, terjemahan hadits, belajar islam",
		OGUrl:       "https://hadits.online/",
		Canonical:   "https://hadits.online/",
	}

	pageData := PageData{
		SEO:  seo,
		Data: data.Info,
	}

	tmpl := template.Must(template.New("index.html").Funcs(funcMap).ParseFiles("templates/index.html", "templates/components/navbar.html", "templates/components/footer.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func collectionHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	slug := vars["slug"]

	// Get pagination parameters
	page := 1
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	data.mu.RLock()
	collection, exists := data.Collections[slug]
	data.mu.RUnlock()

	if !exists {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}

	// Get collection info
	var info *CollectionInfo
	for _, i := range data.Info {
		if i.Slug == slug {
			info = &i
			break
		}
	}

	if info == nil {
		http.Error(w, "Collection info not found", http.StatusNotFound)
		return
	}

	// Calculate pagination
	totalItems := len(collection)
	totalPages := (totalItems + ItemsPerPage - 1) / ItemsPerPage
	startIndex := (page - 1) * ItemsPerPage
	endIndex := startIndex + ItemsPerPage

	if startIndex >= totalItems {
		startIndex = 0
		endIndex = ItemsPerPage
		page = 1
	}

	if endIndex > totalItems {
		endIndex = totalItems
	}

	paginatedHadiths := collection[startIndex:endIndex]

	pagination := Pagination{
		CurrentPage: page,
		TotalPages:  totalPages,
		PerPage:     ItemsPerPage,
		TotalItems:  totalItems,
		HasNext:     page < totalPages,
		HasPrev:     page > 1,
	}

	seo := SEOData{
		Title:       fmt.Sprintf("Koleksi Hadits %s - hadits.online", info.Name),
		Description: fmt.Sprintf("Daftar lengkap hadits dari kitab %s. Tersedia %d hadits dengan terjemahan Indonesia.", info.Name, info.Total),
		Keywords:    fmt.Sprintf("hadits %s, kitab %s, kumpulan hadits", info.Name, info.Name),
		OGUrl:       fmt.Sprintf("https://hadits.online/collection/%s", slug),
		Canonical:   fmt.Sprintf("https://hadits.online/collection/%s", slug),
	}

	pageData := PageData{
		SEO:        seo,
		Info:       info,
		Data:       paginatedHadiths,
		Collection: slug,
		Pagination: pagination,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":          add,
		"add1":         add1,
		"subtract":     subtract,
		"multiply":     multiply,
		"pageNumbers":  pageNumbers,
		"formatHadith": formatHadith,
	}

	tmpl := template.Must(template.New("collection.html").Funcs(funcMap).ParseFiles("templates/collection.html", "templates/components/navbar.html", "templates/components/footer.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func hadithHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	slug := vars["slug"]
	numberStr := vars["number"]

	data.mu.RLock()
	collection, exists := data.Collections[slug]
	data.mu.RUnlock()

	if !exists {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}

	// Find hadith by number
	number, err := strconv.Atoi(numberStr)
	if err != nil {
		http.Error(w, "Invalid hadith number", http.StatusBadRequest)
		return
	}

	var hadith *Collection
	var hadithIndex int
	for i, h := range collection {
		if h.Number == number {
			hadith = &h
			hadithIndex = i
			break
		}
	}

	if hadith == nil {
		http.Error(w, "Hadith not found", http.StatusNotFound)
		return
	}

	// Get collection info and find previous/next
	var info *CollectionInfo
	for _, i := range data.Info {
		if i.Slug == slug {
			info = &i
			break
		}
	}

	var prevHadith, nextHadith *Collection
	if hadithIndex > 0 {
		prevHadith = &collection[hadithIndex-1]
	}
	if hadithIndex < len(collection)-1 {
		nextHadith = &collection[hadithIndex+1]
	}

	description := hadith.ID
	if len(description) > 160 {
		description = description[:157] + "..."
	}

	seo := SEOData{
		Title:       fmt.Sprintf("Hadits %s No. %d - hadits.online", info.Name, hadith.Number),
		Description: description,
		Keywords:    fmt.Sprintf("hadits %s %d, %s no %d", info.Name, hadith.Number, info.Name, hadith.Number),
		OGUrl:       fmt.Sprintf("https://hadits.online/collection/%s/%d", slug, hadith.Number),
		Canonical:   fmt.Sprintf("https://hadits.online/collection/%s/%d", slug, hadith.Number),
	}

	pageData := PageData{
		SEO:        seo,
		Info:       info,
		Hadith:     hadith,
		PrevHadith: prevHadith,
		NextHadith: nextHadith,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":          add,
		"add1":         add1,
		"subtract":     subtract,
		"multiply":     multiply,
		"pageNumbers":  pageNumbers,
		"formatHadith": formatHadith,
	}

	tmpl := template.Must(template.New("hadith.html").Funcs(funcMap).ParseFiles("templates/hadith.html", "templates/components/navbar.html", "templates/components/footer.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	// Parse filters
	filters := parseSearchFilters(r)

	// Validate query
	if filters.Query == "" {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	// Get pagination parameters
	page := 1
	if pageStr := r.URL.Query().Get("page"); pageStr != "" {
		if p, err := strconv.Atoi(pageStr); err == nil && p > 0 {
			page = p
		}
	}

	// Perform advanced search
	allResults := performAdvancedSearch(filters)
	totalItems := len(allResults)

	// Calculate pagination
	totalPages := (totalItems + ItemsPerPage - 1) / ItemsPerPage
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}
	if page < 1 {
		page = 1
	}

	startIndex := (page - 1) * ItemsPerPage
	endIndex := startIndex + ItemsPerPage

	if startIndex < 0 {
		startIndex = 0
	}
	if startIndex > totalItems {
		startIndex = totalItems
	}
	if endIndex > totalItems {
		endIndex = totalItems
	}

	var paginatedResults []SearchResult
	if totalItems > 0 {
		paginatedResults = allResults[startIndex:endIndex]
	}

	pagination := Pagination{
		CurrentPage: page,
		TotalPages:  totalPages,
		PerPage:     ItemsPerPage,
		TotalItems:  totalItems,
		HasNext:     page < totalPages,
		HasPrev:     page > 1,
	}

	seo := SEOData{
		Title:       fmt.Sprintf("Hasil Pencarian: %s - hadits.online", filters.Query),
		Description: fmt.Sprintf("Temukan %d hadits terkait %s di hadits.online.", totalItems, filters.Query),
		Keywords:    fmt.Sprintf("cari hadits %s, hasil pencarian %s", filters.Query, filters.Query),
		OGUrl:       fmt.Sprintf("https://hadits.online/search?q=%s", filters.Query),
		Canonical:   fmt.Sprintf("https://hadits.online/search?q=%s", filters.Query),
	}

	pageData := struct {
		SEO         SEOData
		Query       string
		Filters     SearchFilters
		Results     []SearchResult
		Pagination  Pagination
		TotalItems  int
		Collections []CollectionInfo
	}{
		SEO:         seo,
		Query:       filters.Query,
		Filters:     filters,
		Results:     paginatedResults,
		Pagination:  pagination,
		TotalItems:  totalItems,
		Collections: data.Info,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":          add,
		"add1":         add1,
		"subtract":     subtract,
		"multiply":     multiply,
		"pageNumbers":  pageNumbers,
		"formatHadith": formatHadith,
		"urlEncode": func(s string) string {
			result := strings.ReplaceAll(s, " ", "+")
			result = strings.ReplaceAll(result, "&", "%26")
			result = strings.ReplaceAll(result, "=", "%3D")
			return result
		},
		"hasFilter": func(slice []string, item string) bool {
			for _, v := range slice {
				if v == item {
					return true
				}
			}
			return false
		},
		"buildFilterURL": func(query string, filters SearchFilters, page int) string {
			params := []string{}
			if query != "" {
				params = append(params, "q="+strings.ReplaceAll(query, " ", "+"))
			}
			if filters.Language != "" && filters.Language != "all" {
				params = append(params, "lang="+filters.Language)
			}
			if filters.SortBy != "" && filters.SortBy != "relevance" {
				params = append(params, "sort="+filters.SortBy)
			}
			if len(filters.Collections) > 0 {
				collections := strings.Join(filters.Collections, ",")
				params = append(params, "collections="+collections)
			}
			if filters.NumberRange.Min > 0 {
				params = append(params, fmt.Sprintf("min=%d", filters.NumberRange.Min))
			}
			if filters.NumberRange.Max > 0 {
				params = append(params, fmt.Sprintf("max=%d", filters.NumberRange.Max))
			}
			if page > 1 {
				params = append(params, fmt.Sprintf("page=%d", page))
			}

			if len(params) > 0 {
				return "/search?" + strings.Join(params, "&")
			}
			return "/search"
		},
	}

	tmpl := template.Must(template.New("search.html").Funcs(funcMap).ParseFiles("templates/search.html", "templates/components/navbar.html", "templates/components/footer.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		log.Printf("Template execution error in searchHandler: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// Helper function to highlight search terms
func highlightText(text, query string) string {
	if query == "" {
		return text
	}

	// Simple highlighting - in production you'd want more sophisticated highlighting
	words := strings.Fields(strings.ToLower(query))
	result := text

	for _, word := range words {
		if len(word) > 2 { // Only highlight words longer than 2 characters
			// This is a simple case-insensitive replacement
			// In production, you'd want better HTML escaping and matching
			result = strings.ReplaceAll(result, word,
				"<mark class='bg-yellow-200 px-1 rounded'>"+word+"</mark>")
		}
	}

	return result
}

// Advanced filtering functions
func parseSearchFilters(r *http.Request) SearchFilters {
	filters := SearchFilters{
		Query:    strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q"))),
		Language: r.URL.Query().Get("lang"),
		SortBy:   r.URL.Query().Get("sort"),
	}

	// Parse collections filter
	collectionsParam := r.URL.Query().Get("collections")
	if collectionsParam != "" {
		filters.Collections = strings.Split(collectionsParam, ",")
		// Clean up collection names
		for i, col := range filters.Collections {
			filters.Collections[i] = strings.TrimSpace(col)
		}
	}

	// Parse number range
	minStr := r.URL.Query().Get("min")
	maxStr := r.URL.Query().Get("max")
	if minStr != "" || maxStr != "" {
		filters.NumberRange = NumberRange{}
		if minStr != "" {
			if min, err := strconv.Atoi(minStr); err == nil {
				filters.NumberRange.Min = min
			}
		}
		if maxStr != "" {
			if max, err := strconv.Atoi(maxStr); err == nil {
				filters.NumberRange.Max = max
			}
		}
	}

	// Set defaults
	if filters.Language == "" {
		filters.Language = "all"
	}
	if filters.SortBy == "" {
		filters.SortBy = "relevance"
	}

	return filters
}

func calculateRelevanceScore(hadith Collection, query string) int {
	score := 0
	query = strings.ToLower(query)

	// Check Arabic text
	arabText := strings.ToLower(hadith.Arab)
	idText := strings.ToLower(hadith.ID)

	// Exact match gets highest score
	if strings.Contains(arabText, query) || strings.Contains(idText, query) {
		score += 100
	}

	// Check for partial matches
	words := strings.Fields(query)
	for _, word := range words {
		if len(word) > 2 {
			if strings.Contains(arabText, word) {
				score += 20
			}
			if strings.Contains(idText, word) {
				score += 15
			}
		}
	}

	// Boost score if query is in ID (Indonesian translation)
	if strings.Contains(idText, query) {
		score += 30
	}

	// Boost score if query is in Arabic
	if strings.Contains(arabText, query) {
		score += 25
	}

	return score
}

func matchesFilter(hadith Collection, slug string, filters SearchFilters) bool {
	// Collection filter
	if len(filters.Collections) > 0 {
		found := false
		for _, col := range filters.Collections {
			if strings.EqualFold(col, slug) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Query match check
	queryMatched := false
	lowerArab := strings.ToLower(hadith.Arab)
	lowerID := strings.ToLower(hadith.ID)
	lowerQuery := strings.ToLower(filters.Query)

	if filters.Language == "ar" {
		queryMatched = strings.Contains(lowerArab, lowerQuery)
	} else if filters.Language == "id" {
		queryMatched = strings.Contains(lowerID, lowerQuery)
	} else {
		// Default "all" or anything else: check both
		queryMatched = strings.Contains(lowerArab, lowerQuery) || strings.Contains(lowerID, lowerQuery)
	}

	if !queryMatched {
		return false
	}

	// Number range filter
	if filters.NumberRange.Min > 0 && hadith.Number < filters.NumberRange.Min {
		return false
	}
	if filters.NumberRange.Max > 0 && hadith.Number > filters.NumberRange.Max {
		return false
	}

	return true
}

func sortResults(results []SearchResult, sortBy string) {
	switch sortBy {
	case "number":
		// Sort by hadith number within collections
		sort.Slice(results, func(i, j int) bool {
			if results[i].Slug != results[j].Slug {
				return results[i].Slug < results[j].Slug
			}
			return results[i].Hadith.Number < results[j].Hadith.Number
		})
	case "collection":
		// Sort by collection name
		sort.Slice(results, func(i, j int) bool {
			return results[i].Slug < results[j].Slug
		})
	default: // relevance
		sort.Slice(results, func(i, j int) bool {
			return results[i].Score > results[j].Score
		})
	}
}

func performAdvancedSearch(filters SearchFilters) []SearchResult {
	var allResults []SearchResult

	data.mu.RLock()
	defer data.mu.RUnlock()

	for slug, collection := range data.Collections {
		// Skip if collection filter is set and doesn't match
		if len(filters.Collections) > 0 {
			found := false
			for _, col := range filters.Collections {
				if strings.EqualFold(col, slug) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Get collection name
		collectionName := slug
		for _, info := range data.Info {
			if info.Slug == slug {
				collectionName = info.Name
				break
			}
		}

		for _, hadith := range collection {
			if matchesFilter(hadith, slug, filters) {
				// Calculate relevance score
				score := calculateRelevanceScore(hadith, filters.Query)

				// Get context around match
				context := hadith.ID
				if len(context) > 300 {
					context = context[:300] + "..."
				}

				allResults = append(allResults, SearchResult{
					Collection: collectionName,
					Slug:       slug,
					Hadith:     hadith,
					Context:    context,
					Score:      score,
				})
			}
		}
	}

	// Sort results based on preference
	sortResults(allResults, filters.SortBy)

	return allResults
}

// Helper function to get page URL
func getPageURL(baseURL string, page int) string {
	if page == 1 {
		return baseURL
	}
	separator := "?"
	if strings.Contains(baseURL, "?") {
		separator = "&"
	}
	return baseURL + separator + "page=" + strconv.Itoa(page)
}

func robotsHandler(w http.ResponseWriter, r *http.Request) {
	content := "User-agent: *\nAllow: /\nSitemap: https://hadits.online/sitemap.xml"
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte(content))
}

func sitemapHandler(w http.ResponseWriter, r *http.Request) {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">\n")

	// Home
	sb.WriteString("  <url>\n    <loc>https://hadits.online/</loc>\n    <priority>1.0</priority>\n  </url>\n")
	sb.WriteString("  <url>\n    <loc>https://hadits.online/donate</loc>\n    <priority>0.5</priority>\n  </url>\n")
	sb.WriteString("  <url>\n    <loc>https://hadits.online/faq</loc>\n    <priority>0.5</priority>\n  </url>\n")

	// Collections
	data.mu.RLock()
	for _, info := range data.Info {
		sb.WriteString(fmt.Sprintf("  <url>\n    <loc>https://hadits.online/collection/%s</loc>\n    <priority>0.8</priority>\n  </url>\n", info.Slug))

		// Individual Hadiths (limited to first 100 for sitemap size, or all if preferred)
		// For true SEO, we want all, but sitemaps have limits. Let's do all for now as it's not THAT many.
		for _, h := range data.Collections[info.Slug] {
			sb.WriteString(fmt.Sprintf("  <url>\n    <loc>https://hadits.online/collection/%s/%d</loc>\n    <priority>0.6</priority>\n  </url>\n", info.Slug, h.Number))
		}
	}
	data.mu.RUnlock()

	sb.WriteString("</urlset>")
	w.Header().Set("Content-Type", "application/xml")
	w.Write([]byte(sb.String()))
}

func main() {
	log.Println("Loading hadith data...")
	loadData()
	log.Println("Data loaded successfully")

	r := mux.NewRouter()

	// Routes with better error handling
	r.HandleFunc("/", homeHandler)
	r.HandleFunc("/favorites", favoritesHandler)
	r.HandleFunc("/collection/{slug}", collectionHandler)
	r.HandleFunc("/collection/{slug}/{number}", hadithHandler)
	r.HandleFunc("/search", searchHandler)
	r.HandleFunc("/api/explain", explainHandler)
	r.HandleFunc("/donate", donateHandler)
	r.HandleFunc("/faq", faqHandler)
	r.HandleFunc("/robots.txt", robotsHandler)
	r.HandleFunc("/sitemap.xml", sitemapHandler)
	r.HandleFunc("/manifest.json", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/manifest.json")
	})
	r.HandleFunc("/service-worker.js", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "static/service-worker.js")
	})

	// Serve static files
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))

	// Serve index.html at root
	r.HandleFunc("/index.html", homeHandler)

	// Add 404 handler
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		tmpl := template.Must(template.ParseFiles("templates/404.html", "templates/components/navbar.html", "templates/components/footer.html"))

		seo := SEOData{
			Title:       "Halaman Tidak Ditemukan - hadits.online",
			Description: "Maaf, halaman yang Anda cari tidak dapat ditemukan.",
		}

		tmpl.Execute(w, struct {
			SEO  SEOData
			Path string
		}{SEO: seo, Path: r.URL.Path})
	})

	log.Println("Server starting on :8082")
	log.Println("Access the application at: http://localhost:8082")
	log.Fatal(http.ListenAndServe(":8082", r))
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

func explainHandler(w http.ResponseWriter, r *http.Request) {
	if geminiApiKey == "" {
		http.Error(w, "AI configuration missing", http.StatusServiceUnavailable)
		return
	}

	slug := r.URL.Query().Get("slug")
	numberStr := r.URL.Query().Get("number")

	if slug == "" || numberStr == "" {
		http.Error(w, "Missing parameters", http.StatusBadRequest)
		return
	}

	number, _ := strconv.Atoi(numberStr)

	data.mu.RLock()
	collection, exists := data.Collections[slug]
	data.mu.RUnlock()

	if !exists {
		http.Error(w, "Collection not found", http.StatusNotFound)
		return
	}

	var hadith *Collection
	for _, h := range collection {
		if h.Number == number {
			hadith = &h
			break
		}
	}

	if hadith == nil {
		http.Error(w, "Hadith not found", http.StatusNotFound)
		return
	}

	// Check if explanation already exists in cache
	if hadith.Explanation != "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"explanation": hadith.Explanation,
		})
		return
	}

	// Get collection name
	collectionName := slug
	for _, info := range data.Info {
		if info.Slug == slug {
			collectionName = info.Name
			break
		}
	}

	systemPrompt := `Role: Anda adalah seorang Ahli Ilmu Hadis dan Syarah (Penjelas) Hadis Digital yang akurat, objektif, dan mendalam.

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

	userPrompt := fmt.Sprintf("Kitab: %s\nNomor: %d \n\nArab:\n%s",
		collectionName, hadith.Number, hadith.Arab)

	geminiReq := GeminiRequest{
		Contents: []GeminiContent{
			{Parts: []GeminiPart{{Text: userPrompt}}},
		},
		SystemInstruction: GeminiContent{
			Parts: []GeminiPart{{Text: systemPrompt}},
		},
	}

	jsonData, _ := json.Marshal(geminiReq)
	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/gemini-3-flash-preview:generateContent")
	req, _ := http.NewRequest("POST", url, strings.NewReader(string(jsonData)))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("x-goog-api-key", geminiApiKey)

	client := &http.Client{Timeout: 3 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Error calling AI API", http.StatusInternalServerError)
		return
	}
	fmt.Println(resp)
	defer resp.Body.Close()

	body, _ := ioutil.ReadAll(resp.Body)
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		log.Printf("Gemini parsing error: %v, Body: %s", err, string(body))
		http.Error(w, "Error parsing AI response", http.StatusInternalServerError)
		return
	}

	if len(geminiResp.Candidates) == 0 || len(geminiResp.Candidates[0].Content.Parts) == 0 {
		http.Error(w, "No response from AI", http.StatusInternalServerError)
		return
	}

	explanation := geminiResp.Candidates[0].Content.Parts[0].Text

	// Update in-memory data and save to file
	data.mu.Lock()
	var updatedCollection []Collection
	for i := range data.Collections[slug] {
		if data.Collections[slug][i].Number == number {
			data.Collections[slug][i].Explanation = explanation
			break
		}
	}
	// Create a deep copy of the collection slice for thread-safe file writing
	updatedCollection = make([]Collection, len(data.Collections[slug]))
	copy(updatedCollection, data.Collections[slug])
	data.mu.Unlock()

	go func(s string, coll []Collection) {
		filename := "resource/" + s + ".json"
		jsonData, err := json.MarshalIndent(coll, "", "  ")
		if err != nil {
			log.Printf("Error marshaling collection %s for caching: %v", s, err)
			return
		}
		err = ioutil.WriteFile(filename, jsonData, 0644)
		if err != nil {
			log.Printf("Error writing cache to %s: %v", filename, err)
		}
	}(slug, updatedCollection)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"explanation": explanation,
	})
}

// Donation page handler
func donateHandler(w http.ResponseWriter, r *http.Request) {
	mayarData, err := getMayarProductDetail()
	if err != nil {
		log.Printf("Error fetching Mayar product: %v", err)
		// Continue anyway, template will handle nil data
	}

	seo := SEOData{
		Title:       "Donasi & Dukung Kami - hadits.online",
		Description: "Bantu kami menjaga keberlangsungan layanan Hadits.online agar tetap gratis bagi umat Islam.",
		Keywords:    "donasi islam, dukung dakwah digital, hadits online",
		OGUrl:       "https://hadits.online/donate",
		Canonical:   "https://hadits.online/donate",
	}

	pageData := struct {
		SEO  SEOData
		Data *MayarProductResponse
	}{
		SEO:  seo,
		Data: mayarData,
	}

	tmpl := template.Must(template.New("donate.html").Funcs(template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
		"formatRupiah": func(amount float64) string {
			return fmt.Sprintf("Rp %.0f", amount)
		},
		"calculatePercentage": func(current float64, target float64) float64 {
			if target <= 0 {
				return 0
			}
			percent := (current / target) * 100
			if percent > 100 {
				return 100
			}
			return percent
		},
		"safeHTML": func(s string) template.HTML {
			return template.HTML(s)
		},
	}).ParseFiles("templates/donate.html", "templates/components/navbar.html", "templates/components/footer.html"))

	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

// FAQ page handler
func faqHandler(w http.ResponseWriter, r *http.Request) {
	seo := SEOData{
		Title:       "Pertanyaan Umum (FAQ) - hadits.online",
		Description: "Jawaban atas pertanyaan yang sering diajukan mengenai penggunaan dan fitur Hadits.online.",
		Keywords:    "faq hadits online, bantuan hadits",
		OGUrl:       "https://hadits.online/faq",
		Canonical:   "https://hadits.online/faq",
	}

	pageData := PageData{
		SEO: seo,
	}

	tmpl := template.Must(template.New("faq.html").Funcs(template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
	}).ParseFiles("templates/faq.html", "templates/components/navbar.html", "templates/components/footer.html"))

	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}
