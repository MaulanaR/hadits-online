package main

import (
	"strconv"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDailyHadithRealData(t *testing.T) {
	setupRealSearch(t)

	day := time.Date(2026, 10, 9, 8, 0, 0, 0, wib)
	a := dailyHadith(day)
	if a == nil {
		t.Fatal("no daily hadith")
	}
	if b := pickDaily(dailyKey(day.Add(10 * time.Hour))); b.Info.Slug != a.Info.Slug || b.Hadith.Number != a.Hadith.Number {
		t.Errorf("same WIB day gave different picks: %s/%d vs %s/%d", a.Info.Slug, a.Hadith.Number, b.Info.Slug, b.Hadith.Number)
	}
	if a.Label != "Jumat, 9 Oktober 2026" {
		t.Errorf("label = %q", a.Label)
	}

	distinct := map[string]bool{}
	for i := 0; i < 30; i++ {
		d := pickDaily(dailyKey(day.AddDate(0, 0, i)))
		if d == nil {
			t.Fatalf("day %d: no pick", i)
		}
		if l := utf8.RuneCountInString(d.Hadith.ID); l < dailyMinChars || l > dailyMaxChars {
			t.Errorf("day %d: %s/%d translation length %d out of range", i, d.Info.Slug, d.Hadith.Number, l)
		}
		distinct[d.Info.Slug+"/"+strconv.Itoa(d.Hadith.Number)] = true
	}
	if len(distinct) < 25 {
		t.Errorf("only %d distinct picks over 30 days", len(distinct))
	}
}

func TestDailyKeyUsesWIB(t *testing.T) {
	// 18:30 UTC on Oct 8 is already Oct 9 in WIB.
	if got := dailyKey(time.Date(2026, 10, 8, 18, 30, 0, 0, time.UTC)); got != "2026-10-09" {
		t.Errorf("dailyKey = %s, want 2026-10-09", got)
	}
}
