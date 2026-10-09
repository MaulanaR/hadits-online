package main

import (
	"hash/fnv"
	"log"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"
)

// Daily featured hadith ("Hadits Pilihan Hari Ini") on the home page.
// The pick is pseudo-random but deterministic per calendar day in WIB, so every
// visitor sees the same hadith all day and the page stays cacheable.

var wib = time.FixedZone("WIB", 7*60*60)

const (
	dailyMinChars = 60  // skip fragments
	dailyMaxChars = 600 // keep the home page card short
	dailyAttempts = 64
)

type DailyHadith struct {
	Hadith Collection
	Info   CollectionInfo
	Date   string // YYYY-MM-DD (WIB)
	Label  string // e.g. "Jumat, 9 Oktober 2026"
}

var (
	hariID  = [...]string{"Minggu", "Senin", "Selasa", "Rabu", "Kamis", "Jumat", "Sabtu"}
	bulanID = [...]string{"Januari", "Februari", "Maret", "April", "Mei", "Juni", "Juli",
		"Agustus", "September", "Oktober", "November", "Desember"}
)

func dateLabelID(t time.Time) string {
	return hariID[t.Weekday()] + ", " + strconv.Itoa(t.Day()) + " " + bulanID[t.Month()-1] + " " + strconv.Itoa(t.Year())
}

var daily struct {
	mu  sync.Mutex
	key string
	val *DailyHadith
}

func dailyKey(now time.Time) string {
	return now.In(wib).Format("2006-01-02")
}

// dailyHadith returns today's featured hadith, or nil if none could be read.
func dailyHadith(now time.Time) *DailyHadith {
	date := dailyKey(now)
	key := date + "/" + strconv.FormatUint(dataGen.Load(), 10)

	daily.mu.Lock()
	defer daily.mu.Unlock()
	if daily.key == key {
		return daily.val
	}
	v := pickDaily(date)
	if v != nil {
		v.Label = dateLabelID(now.In(wib))
	}
	daily.key, daily.val = key, v
	return v
}

func pickDaily(date string) *DailyHadith {
	if store == nil || len(store.docNum) == 0 {
		return nil
	}
	n := uint64(len(store.docNum))
	h := fnv.New64a()
	h.Write([]byte("hadits-pilihan:" + date))
	seed := h.Sum64()

	var fallback *DailyHadith
	for k := uint64(0); k < dailyAttempts; k++ {
		// Large odd stride walks a different doc on every attempt.
		doc := uint32((seed + k*2654435761) % n)
		c, rec, err := store.readDoc(doc)
		if err != nil || rec.Arab == "" || rec.ID == "" {
			continue
		}
		d := &DailyHadith{Hadith: rec, Info: c.Info, Date: date}
		if l := utf8.RuneCountInString(rec.ID); l >= dailyMinChars && l <= dailyMaxChars &&
			utf8.RuneCountInString(rec.Arab) <= dailyMaxChars {
			return d
		}
		if fallback == nil {
			fallback = d
		}
	}
	if fallback == nil {
		log.Printf("daily: no readable hadith for %s", date)
	}
	return fallback
}
