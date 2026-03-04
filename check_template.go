package main

import (
	"fmt"
	"html/template"
	"os"
)

func main() {
	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
		"add1": func(a int) int { return a + 1 },
		"subtract": func(a, b int) int { return a - b },
		"multiply": func(a, b int) int { return a * b },
		"pageNumbers": func(current, total int) []int { 
			var pages []int
			start := current - 2
			if start < 1 { start = 1 }
			end := start + 4
			if end > total { end = total }
			for page := start; page <= end; page++ {
				pages = append(pages, page)
			}
			return pages
		},
		"urlEncode": func(s string) string { 
			result := s
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
		"buildFilterURL": func(query string, filters interface{}, page int) string {
			return "/search?q=" + query
		},
	}
	
	_, err := template.New("search.html").Funcs(funcMap).ParseFiles("templates/search.html")
	if err != nil {
		fmt.Printf("Template error: %s\n", err.Error())
		os.Exit(1)
	}
	fmt.Println("Template parsed successfully!")
}
