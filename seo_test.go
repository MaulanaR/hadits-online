package main

import (
	"encoding/json"
	"strings"
	"testing"
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
