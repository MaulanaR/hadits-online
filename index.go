package main

import (
	"log"
	"slices"
	"strings"
	"time"
)

// In-memory inverted index built once at startup from the same scan that
// builds the record store (the JSON is parsed exactly once).
//
// Fields per document:
//   matan – Indonesian tokens outside [...]
//   isnad – Indonesian tokens inside [...] (narrator names)
//   ar    – Arabic tokens (harakat stripped, letters folded)
// Latin and Arabic terms share one dictionary; the scripts never collide.
// A posting's tf packs matan/ar tf in the low byte and isnad tf in the high
// byte (each saturating at 255).

type searchIndex struct {
	terms    map[string]uint32 // term -> id
	termList []string          // id -> term
	// Postings of term id t are postDocs[postStart[t]:postStart[t+1]] (ascending doc ids).
	postStart []uint32
	postDocs  []uint32
	postTF    []uint16

	docLenM, docLenI, docLenA []uint16
	avgM, avgI, avgA          float32
	numDocs                   int

	sortedIDs  []uint32            // term ids sorted by term (prefix expansion)
	stemGroups map[string][]uint32 // stem -> term ids (only groups with 2+ terms)
}

var searchIdx *searchIndex

type indexBuilder struct {
	terms    map[string]uint32
	termList []string
	docs     [][]uint32
	tfs      [][]uint16
	lenM     []uint16
	lenI     []uint16
	lenA     []uint16
	cur      map[uint32]uint16
	order    []uint32
	buf      []byte
}

func newIndexBuilder() *indexBuilder {
	return &indexBuilder{
		terms: make(map[string]uint32, 1<<17),
		cur:   make(map[uint32]uint16, 512),
	}
}

func sat16(n int) uint16 {
	if n > 65535 {
		return 65535
	}
	return uint16(n)
}

// add indexes one record; doc ids must arrive in ascending order starting at 0.
func (b *indexBuilder) add(doc uint32, rec *scanRecord) {
	clear(b.cur)
	b.order = b.order[:0]
	var nM, nI, nA int
	visit := func(norm []byte, _, _ int, script uint8, inBracket bool) {
		id, ok := b.terms[string(norm)]
		if !ok {
			id = uint32(len(b.termList))
			s := string(norm)
			b.terms[s] = id
			b.termList = append(b.termList, s)
			b.docs = append(b.docs, nil)
			b.tfs = append(b.tfs, nil)
		}
		tf, seen := b.cur[id]
		if !seen {
			b.order = append(b.order, id)
		}
		lo, hi := tf&0xFF, tf>>8
		// Field lengths count content words only, so long isnad boilerplate
		// ("telah menceritakan kepada kami ...") doesn't penalise a document.
		var stop bool
		if script == scriptArabic {
			stop = arStopwords[string(norm)]
		} else {
			stop = idStopwords[string(norm)]
		}
		switch {
		case script == scriptLatin && inBracket:
			if !stop {
				nI++
			}
			if hi < 255 {
				hi++
			}
		default:
			if !stop {
				if script == scriptArabic {
					nA++
				} else {
					nM++
				}
			}
			if lo < 255 {
				lo++
			}
		}
		b.cur[id] = hi<<8 | lo
	}
	b.buf = scanTokens(rec.ID, b.buf, visit)
	b.buf = scanTokens(rec.Arab, b.buf, visit)
	for _, id := range b.order {
		b.docs[id] = append(b.docs[id], doc)
		b.tfs[id] = append(b.tfs[id], b.cur[id])
	}
	b.lenM = append(b.lenM, sat16(nM))
	b.lenI = append(b.lenI, sat16(nI))
	b.lenA = append(b.lenA, sat16(nA))
}

func (b *indexBuilder) finish() *searchIndex {
	t0 := time.Now()
	ix := &searchIndex{
		terms:    b.terms,
		termList: b.termList,
		docLenM:  b.lenM,
		docLenI:  b.lenI,
		docLenA:  b.lenA,
		numDocs:  len(b.lenM),
	}
	total := 0
	for _, d := range b.docs {
		total += len(d)
	}
	// Compact into flat arrays (exact size, no per-term slice headers).
	ix.postStart = make([]uint32, len(b.docs)+1)
	ix.postDocs = make([]uint32, 0, total)
	ix.postTF = make([]uint16, 0, total)
	for t := range b.docs {
		ix.postStart[t] = uint32(len(ix.postDocs))
		ix.postDocs = append(ix.postDocs, b.docs[t]...)
		ix.postTF = append(ix.postTF, b.tfs[t]...)
		b.docs[t], b.tfs[t] = nil, nil
	}
	ix.postStart[len(b.docs)] = uint32(len(ix.postDocs))
	b.docs, b.tfs, b.cur, b.terms, b.termList = nil, nil, nil, nil, nil

	var sM, sI, sA float64
	for i := 0; i < ix.numDocs; i++ {
		sM += float64(ix.docLenM[i])
		sI += float64(ix.docLenI[i])
		sA += float64(ix.docLenA[i])
	}
	if n := float64(ix.numDocs); n > 0 {
		ix.avgM, ix.avgI, ix.avgA = float32(sM/n), float32(sI/n), float32(sA/n)
	}

	ix.sortedIDs = make([]uint32, len(ix.termList))
	for i := range ix.sortedIDs {
		ix.sortedIDs[i] = uint32(i)
	}
	slices.SortFunc(ix.sortedIDs, func(a, b uint32) int { return strings.Compare(ix.termList[a], ix.termList[b]) })

	// Stem groups: stem every vocabulary term once.
	groups := make(map[string][]uint32)
	for id, t := range ix.termList {
		var s string
		if tokenScript(t) == scriptArabic {
			s = stemAR(t)
		} else {
			s = stemID(t, ix.dfOf)
		}
		groups[s] = append(groups[s], uint32(id))
	}
	for s, g := range groups {
		if len(g) < 2 {
			delete(groups, s)
		} else {
			groups[s] = slices.Clip(g)
		}
	}
	ix.stemGroups = groups

	var missing []string
	for _, g := range variantGroups {
		for _, w := range g {
			if _, ok := ix.terms[w]; !ok {
				missing = append(missing, w)
			}
		}
	}
	log.Printf("Index: %d terms, %d postings, %d stem groups (finish %v); variants absent from corpus: %s",
		len(ix.termList), total, len(groups), time.Since(t0).Round(time.Millisecond), strings.Join(missing, ", "))
	return ix
}

func (ix *searchIndex) lookup(term string) (uint32, bool) {
	id, ok := ix.terms[term]
	return id, ok
}

func (ix *searchIndex) df(id uint32) int {
	return int(ix.postStart[id+1] - ix.postStart[id])
}

// dfOf returns the document frequency of a term string (0 if absent).
func (ix *searchIndex) dfOf(term string) int {
	if id, ok := ix.terms[term]; ok {
		return ix.df(id)
	}
	return 0
}

func (ix *searchIndex) postings(id uint32) ([]uint32, []uint16) {
	a, b := ix.postStart[id], ix.postStart[id+1]
	return ix.postDocs[a:b], ix.postTF[a:b]
}

// prefixTerms returns up to limit term ids starting with prefix (same script),
// most frequent first.
func (ix *searchIndex) prefixTerms(prefix string, limit int) []uint32 {
	lo, _ := slices.BinarySearchFunc(ix.sortedIDs, prefix, func(id uint32, p string) int {
		return strings.Compare(ix.termList[id], p)
	})
	var out []uint32
	for i := lo; i < len(ix.sortedIDs); i++ {
		id := ix.sortedIDs[i]
		if !strings.HasPrefix(ix.termList[id], prefix) {
			break
		}
		if ix.termList[id] != prefix {
			out = append(out, id)
		}
	}
	slices.SortFunc(out, func(a, b uint32) int { return ix.df(b) - ix.df(a) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// stemOf returns the grouping key of a (normalized) term.
func (ix *searchIndex) stemOf(term string) string {
	if tokenScript(term) == scriptArabic {
		return stemAR(term)
	}
	return stemID(term, ix.dfOf)
}
