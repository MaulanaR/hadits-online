package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/mux"
)

type Collection struct {
	Number int    `json:"number"`
	Arab   string `json:"arab"`
	ID     string `json:"id"`
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
	Query   string
	Filters SearchFilters
	Results []struct {
		Slug    string
		Info    CollectionInfo
		Hadiths []SearchResult
	}
	Pagination  Pagination
	TotalItems  int
	Collections []CollectionInfo
}

var data *HadithData

const (
	ItemsPerPage = 20
)

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

func favoritesHandler(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.ParseFiles("templates/favorites.html"))
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	// Create a template with custom functions
	funcMap := template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
	}

	tmpl := template.Must(template.New("index.html").Funcs(funcMap).ParseFiles("templates/index.html"))
	if err := tmpl.Execute(w, data.Info); err != nil {
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

	pageData := struct {
		Info       *CollectionInfo
		Hadiths    []Collection
		Collection string
		Pagination Pagination
	}{
		Info:       info,
		Hadiths:    paginatedHadiths,
		Collection: slug,
		Pagination: pagination,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
	}

	tmpl := template.Must(template.New("collection.html").Funcs(funcMap).ParseFiles("templates/collection.html"))
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

	pageData := struct {
		Info       *CollectionInfo
		Hadith     *Collection
		PrevHadith *Collection
		NextHadith *Collection
	}{
		Info:       info,
		Hadith:     hadith,
		PrevHadith: prevHadith,
		NextHadith: nextHadith,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
	}

	tmpl := template.Must(template.New("hadith.html").Funcs(funcMap).ParseFiles("templates/hadith.html"))
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

	paginatedResults := allResults[startIndex:endIndex]

	// Group results by slug for display
	type CollectionResults struct {
		Info    CollectionInfo
		Hadiths []SearchResult
	}

	resultsByCollection := make(map[string]*CollectionResults)

	for _, result := range paginatedResults {
		if resultsByCollection[result.Slug] == nil {
			var info CollectionInfo
			for _, collectionInfo := range data.Info {
				if collectionInfo.Slug == result.Slug {
					info = collectionInfo
					break
				}
			}
			resultsByCollection[result.Slug] = &CollectionResults{
				Info:    info,
				Hadiths: []SearchResult{result},
			}
		} else {
			resultsByCollection[result.Slug].Hadiths = append(resultsByCollection[result.Slug].Hadiths, result)
		}
	}

	// Convert to slice for template
	var finalResults []struct {
		Slug    string
		Info    CollectionInfo
		Hadiths []SearchResult
	}
	for slug, collectionData := range resultsByCollection {
		finalResults = append(finalResults, struct {
			Slug    string
			Info    CollectionInfo
			Hadiths []SearchResult
		}{
			Slug:    slug,
			Info:    collectionData.Info,
			Hadiths: collectionData.Hadiths,
		})
	}

	pagination := Pagination{
		CurrentPage: page,
		TotalPages:  totalPages,
		PerPage:     ItemsPerPage,
		TotalItems:  totalItems,
		HasNext:     page < totalPages,
		HasPrev:     page > 1,
	}

	pageData := FilteredResults{
		Query:       filters.Query,
		Filters:     filters,
		Results:     finalResults,
		Pagination:  pagination,
		TotalItems:  totalItems,
		Collections: data.Info,
	}

	// Create template with custom functions
	funcMap := template.FuncMap{
		"add":         add,
		"add1":        add1,
		"subtract":    subtract,
		"multiply":    multiply,
		"pageNumbers": pageNumbers,
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

	tmpl := template.Must(template.New("search.html").Funcs(funcMap).ParseFiles("templates/search.html"))
	if err := tmpl.Execute(w, pageData); err != nil {
		http.Error(w, "Template error: "+err.Error(), http.StatusInternalServerError)
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

	// Language filter
	if filters.Language != "all" {
		if filters.Language == "ar" {
			if !strings.Contains(strings.ToLower(hadith.Arab), filters.Query) {
				return false
			}
		} else if filters.Language == "id" {
			if !strings.Contains(strings.ToLower(hadith.ID), filters.Query) {
				return false
			}
		} else {
			// For "all" language, check both
			if !strings.Contains(strings.ToLower(hadith.ID), filters.Query) &&
				!strings.Contains(strings.ToLower(hadith.Arab), filters.Query) {
				return false
			}
		}
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

		for _, hadith := range collection {
			if matchesFilter(hadith, slug, filters) {
				// Calculate relevance score
				score := calculateRelevanceScore(hadith, filters.Query)

				// Get context around match
				context := hadith.ID
				if len(context) > 200 {
					context = context[:200] + "..."
				}

				allResults = append(allResults, SearchResult{
					Collection: "",
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

	// Serve static files
	r.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("static/"))))

	// Serve index.html at root
	r.HandleFunc("/index.html", homeHandler)

	// Add 404 handler
	r.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		tmpl := template.Must(template.ParseFiles("templates/404.html"))
		tmpl.Execute(w, struct {
			Path string
		}{Path: r.URL.Path})
	})

	log.Println("Server starting on :8081")
	log.Println("Access the application at: http://localhost:8081")
	log.Fatal(http.ListenAndServe(":8081", r))
}
