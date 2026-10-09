package main

import (
	"encoding/json"
	"html/template"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Query parsing
// ---------------------------------------------------------------------------

const (
	maxQueryRunes   = 200
	maxQueryTerms   = 10
	maxPhraseVerify = 1000 // quoted-phrase candidates verified against the text
	phraseBonusTopN = 100  // top results checked for the proximity bonus
	readChunk       = 64   // records read per batch while verifying
	phraseBonus     = 1.5

	weightExact   = 1.0
	weightVariant = 1.0 // spelling variants are the same word
	weightStem    = 0.7
	weightPrefix  = 0.5

	bm25K1Default = 1.2
	bm25BDefault  = 0.5

	fieldWeightMatan = 1.0
	fieldWeightIsnad = 0.35
	fieldWeightAr    = 1.0
)

type expansion struct {
	id     uint32
	w      float32
	arabic bool
}

type queryTerm struct {
	text string
	exp  []expansion
}

type parsedQuery struct {
	terms   []queryTerm
	phrases [][]string // quoted phrases (normalized tokens)
	seq     []string   // every normalized query token in order (phrase bonus)
	direct  *directRef
	// phraseOnly: the query consists only of quoted phrases.
	phraseOnly bool
}

type directRef struct {
	slug   string
	number int
}

var reQuoted = regexp.MustCompile(`["“”]([^"“”]+)["“”]`)

// capRunes trims s to at most n runes.
func capRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for k := 0; k < n; k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

func tokenTexts(s string) []string {
	var out []string
	scanTokens(s, nil, func(norm []byte, _, _ int, _ uint8, _ bool) {
		out = append(out, string(norm))
	})
	return out
}

// parseQuery turns the raw query into weighted term expansions.
func parseQuery(raw string, ix *searchIndex) *parsedQuery {
	q := &parsedQuery{}
	raw = capRunes(strings.TrimSpace(raw), maxQueryRunes)

	exactOnly := map[string]bool{}
	for _, m := range reQuoted.FindAllStringSubmatch(raw, -1) {
		toks := tokenTexts(m[1])
		if len(toks) == 0 {
			continue
		}
		if len(toks) > 1 {
			q.phrases = append(q.phrases, toks)
		}
		for _, t := range toks {
			exactOnly[t] = true
		}
	}

	q.seq = tokenTexts(raw)
	q.phraseOnly = len(q.phrases) > 0 && len(tokenTexts(reQuoted.ReplaceAllString(raw, " "))) == 0
	q.direct = detectDirectHit(q.seq)

	// Dedupe, drop stopwords (unless the query is only stopwords), cap.
	seen := map[string]bool{}
	var all, kept []string
	for _, t := range q.seq {
		if seen[t] {
			continue
		}
		seen[t] = true
		all = append(all, t)
		if !isStopword(t) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		kept = all
	}
	if len(kept) > maxQueryTerms {
		kept = kept[:maxQueryTerms]
	}
	for _, t := range kept {
		q.terms = append(q.terms, queryTerm{text: t, exp: ix.expand(t, exactOnly[t])})
	}
	return q
}

// expand builds the weighted expansion set of one query term:
// exact 1.0, spelling variant 0.9, same stem 0.7; prefix 0.5 only when none
// of those exist in the corpus.
func (ix *searchIndex) expand(t string, exactOnly bool) []expansion {
	best := map[uint32]float32{}
	add := func(id uint32, w float32) {
		if w > best[id] {
			best[id] = w
		}
	}
	forms := []string{t}
	if id, ok := ix.lookup(t); ok {
		add(id, weightExact)
	}
	for _, v := range variantOf[t] {
		if v == t {
			continue
		}
		forms = append(forms, v)
		if id, ok := ix.lookup(v); ok {
			add(id, weightVariant)
		}
	}
	if !exactOnly {
		for _, f := range forms {
			st := ix.stemOf(f)
			for _, id := range ix.stemGroups[st] {
				add(id, weightStem)
			}
			if id, ok := ix.lookup(st); ok {
				add(id, weightStem)
			}
		}
		if len(best) == 0 && utf8.RuneCountInString(t) >= 4 {
			for _, id := range ix.prefixTerms(t, 50) {
				add(id, weightPrefix)
			}
		}
	}
	out := make([]expansion, 0, len(best))
	for id, w := range best {
		out = append(out, expansion{id: id, w: w, arabic: tokenScript(ix.termList[id]) == scriptArabic})
	}
	slices.SortFunc(out, func(a, b expansion) int { return int(a.id) - int(b.id) })
	return out
}

// ---------------------------------------------------------------------------
// Direct number lookup ("bukhari 1", "HR. Abu Daud no. 1882")
// ---------------------------------------------------------------------------

var collectionAliases = map[string]string{
	"bukhari": "bukhari", "al bukhari": "bukhari", "albukhari": "bukhari", "bukhory": "bukhari", "imam bukhari": "bukhari",
	"muslim": "muslim", "imam muslim": "muslim",
	"abu daud": "abu-dawud", "abu dawud": "abu-dawud", "abudaud": "abu-dawud", "abudawud": "abu-dawud", "abu dawood": "abu-dawud",
	"tirmidzi": "tirmidzi", "tirmizi": "tirmidzi", "turmudzi": "tirmidzi", "at tirmidzi": "tirmidzi", "attirmidzi": "tirmidzi", "tirmidhi": "tirmidzi",
	"nasai": "nasai", "an nasai": "nasai", "annasai": "nasai",
	"ibnu majah": "ibnu-majah", "ibn majah": "ibnu-majah", "ibnumajah": "ibnu-majah", "ibnu majjah": "ibnu-majah",
	"ahmad": "ahmad", "musnad ahmad": "ahmad", "imam ahmad": "ahmad",
	"malik": "malik", "muwatha": "malik", "muwaththa": "malik", "imam malik": "malik",
	"darimi": "darimi", "ad darimi": "darimi", "addarimi": "darimi",
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// detectDirectHit recognises "[hr] <collection> [no|nomor|nomer] <number>".
func detectDirectHit(toks []string) *directRef {
	if len(toks) < 2 || !isDigits(toks[len(toks)-1]) {
		return nil
	}
	num, err := strconv.Atoi(toks[len(toks)-1])
	if err != nil {
		return nil
	}
	mid := toks[:len(toks)-1]
	if len(mid) > 0 && (mid[0] == "hr" || mid[0] == "hadits" || mid[0] == "hadis") {
		mid = mid[1:]
	}
	if n := len(mid); n > 0 && (mid[n-1] == "no" || mid[n-1] == "nomor" || mid[n-1] == "nomer") {
		mid = mid[:n-1]
	}
	if len(mid) == 0 || len(mid) > 3 {
		return nil
	}
	slug, ok := collectionAliases[strings.Join(mid, " ")]
	if !ok {
		return nil
	}
	c, ok := getBySlug(slug)
	if !ok {
		return nil
	}
	if _, ok := c.indexOf(num); !ok {
		return nil
	}
	return &directRef{slug: slug, number: num}
}

// ---------------------------------------------------------------------------
// Scoring
// ---------------------------------------------------------------------------

type scoreScratch struct {
	acc         []float32
	cnt         []uint8
	pM, pI, pA  []float32 // primary tf (exact + variants) per field
	sM, sI, sA  []float32 // secondary tf (stem + prefix expansions) per field
	termTouched []uint32
	touched     []uint32
}

var scratchPool = sync.Pool{New: func() any { return new(scoreScratch) }}

func getScratch(n int) *scoreScratch {
	sc := scratchPool.Get().(*scoreScratch)
	if len(sc.acc) != n {
		sc.acc = make([]float32, n)
		sc.cnt = make([]uint8, n)
		sc.pM, sc.pI, sc.pA = make([]float32, n), make([]float32, n), make([]float32, n)
		sc.sM, sc.sI, sc.sA = make([]float32, n), make([]float32, n), make([]float32, n)
	}
	sc.termTouched = sc.termTouched[:0]
	sc.touched = sc.touched[:0]
	return sc
}

// searchOutcome is what the result cache stores: ranked doc ids + scores.
type searchOutcome struct {
	ids      []uint32
	scores   []float32
	andCount int  // results that contain every query term
	partial  bool // nothing contains every term; only partial matches are shown
	mixed    bool // a few contain every term; partial matches are ranked with them
	direct   *directRef
}

// BM25 parameters (vars so tests can tune them).
var (
	bm25K1 float32 = bm25K1Default
	bm25B  float32 = bm25BDefault
)

// exactBoost rewards a field that contains the exact (or variant) form, so a
// document with the typed word outranks one that only repeats related forms.
// Chosen with a parameter sweep over the PM_PLAN section-5 cases.
var exactBoost float32 = 0.1

// fieldScore is the BM25 of one query term in one field. tfP is the tf of
// exact/variant forms, tfS the weighted tf of stem/prefix forms.
func fieldScore(tfP, tfS, dl, avgdl, idf float32) float32 {
	if tfP == 0 && tfS == 0 {
		return 0
	}
	s := bm25(tfP+tfS, dl, avgdl, idf)
	if tfP > 0 {
		s *= 1 + exactBoost
	}
	return s
}

// fillThreshold: when fewer AND matches than this exist, partial matches are
// ranked together with them.
const fillThreshold = 10

func bm25(tf, dl, avgdl, idf float32) float32 {
	if tf <= 0 {
		return 0
	}
	if avgdl <= 0 {
		avgdl = 1
	}
	return idf * tf * (bm25K1 + 1) / (tf + bm25K1*(1-bm25B+bm25B*dl/avgdl))
}

func idfFor(n, df int) float32 {
	return float32(math.Log(1 + (float64(n)-float64(df)+0.5)/(float64(df)+0.5)))
}

// collPriority is the relevance tie-break order.
var collPriority = map[string]int{
	"bukhari": 0, "muslim": 1, "abu-dawud": 2, "tirmidzi": 3, "nasai": 4,
	"ibnu-majah": 5, "ahmad": 6, "malik": 7, "darimi": 8,
}

func priorityOf(slug string) int {
	if p, ok := collPriority[slug]; ok {
		return p
	}
	return 100
}

// executeSearch runs a parsed query against the index (uncached).
func executeSearch(q *parsedQuery, f SearchFilters, ix *searchIndex, st *hadithStore) *searchOutcome {
	out := &searchOutcome{direct: q.direct}
	n := len(q.terms)
	if n == 0 || ix == nil {
		return out
	}

	// Filters as fast lookups.
	var allowColl [256]bool
	if len(f.Collections) == 0 {
		for i := range allowColl {
			allowColl[i] = true
		}
	} else {
		for _, slug := range f.Collections {
			if c, ok := getBySlug(slug); ok {
				allowColl[c.idx] = true
			}
		}
	}
	minN, maxN := int32(f.NumberRange.Min), int32(f.NumberRange.Max)
	useID := f.Language != "ar"
	useAR := f.Language != "id"

	sc := getScratch(ix.numDocs)
	defer scratchPool.Put(sc)

	N := ix.numDocs
	for _, qt := range q.terms {
		sc.termTouched = sc.termTouched[:0]
		maxDF := 0
		for _, e := range qt.exp {
			if d := ix.df(e.id); d > maxDF {
				maxDF = d
			}
			if (e.arabic && !useAR) || (!e.arabic && !useID) {
				continue
			}
			primary := e.w >= weightVariant
			docs, tfs := ix.postings(e.id)
			for k, d := range docs {
				if !allowColl[st.docColl[d]] {
					continue
				}
				if num := st.docNum[d]; (minN > 0 && num < minN) || (maxN > 0 && num > maxN) {
					continue
				}
				tf := tfs[k]
				lo, hi := float32(tf&0xFF), float32(tf>>8)
				if e.arabic {
					hi = 0
				}
				if lo == 0 && hi == 0 {
					continue
				}
				fresh := sc.pM[d] == 0 && sc.pI[d] == 0 && sc.pA[d] == 0 && sc.sM[d] == 0 && sc.sI[d] == 0 && sc.sA[d] == 0
				switch {
				case e.arabic && primary:
					sc.pA[d] += e.w * lo
				case e.arabic:
					sc.sA[d] += e.w * lo
				case primary:
					sc.pM[d] += e.w * lo
					sc.pI[d] += e.w * hi
				default:
					sc.sM[d] += e.w * lo
					sc.sI[d] += e.w * hi
				}
				if fresh {
					sc.termTouched = append(sc.termTouched, d)
				}
			}
		}
		// Synonym-style BM25: expansions of one query term share one tf per
		// field (each form weighted: exact/variant 1.0, stem 0.7, prefix 0.5);
		// a field with the exact form gets exactBoost. idf comes from the most
		// frequent expansion (like Lucene's SynonymQuery).
		idf := idfFor(N, maxDF)
		for _, d := range sc.termTouched {
			s := fieldWeightMatan*fieldScore(sc.pM[d], sc.sM[d], float32(ix.docLenM[d]), ix.avgM, idf) +
				fieldWeightIsnad*fieldScore(sc.pI[d], sc.sI[d], float32(ix.docLenI[d]), ix.avgI, idf) +
				fieldWeightAr*fieldScore(sc.pA[d], sc.sA[d], float32(ix.docLenA[d]), ix.avgA, idf)
			sc.pM[d], sc.pI[d], sc.pA[d], sc.sM[d], sc.sI[d], sc.sA[d] = 0, 0, 0, 0, 0, 0
			if sc.cnt[d] == 0 {
				sc.touched = append(sc.touched, d)
			}
			sc.acc[d] += s
			sc.cnt[d]++
		}
	}

	// Documents matching only some terms are scaled by coverage (matched/n).
	collect := func(lo int) *searchOutcome {
		b := &searchOutcome{}
		for _, d := range sc.touched {
			if c := int(sc.cnt[d]); c >= lo {
				b.ids = append(b.ids, d)
				b.scores = append(b.scores, sc.acc[d]*float32(c)/float32(n))
				if c == n {
					b.andCount++
				}
			}
		}
		return b
	}
	andB := collect(n)
	filled := false
	if n >= 2 && len(andB.ids) < fillThreshold && len(q.phrases) == 0 {
		// Few or no documents contain every term (typical for long, natural
		// language queries): rank them together with documents that contain
		// at least half of the terms.
		andB = collect((n + 1) / 2)
		filled = andB.andCount < len(andB.ids)
	}
	orB := &searchOutcome{}
	for _, d := range sc.touched {
		sc.acc[d], sc.cnt[d] = 0, 0
	}

	// Quoted phrases are required: verify against the stored text.
	if len(q.phrases) > 0 && len(andB.ids) > 0 {
		if v := verifyPhrases(q, andB, st); len(v.ids) > 0 {
			andB = v
		} else {
			andB, orB = &searchOutcome{}, andB // nothing has the exact phrase: show the AND matches as partial
		}
	}

	rank := func(b *searchOutcome) {
		sortOutcome(b, "relevance", st)
		if len(q.phrases) == 0 && len(q.seq) >= 2 && len(b.ids) > 1 {
			applyPhraseBonus(q, b, st)
			sortOutcome(b, "relevance", st)
		}
		if f.SortBy == "number" || f.SortBy == "collection" {
			sortOutcome(b, f.SortBy, st)
		}
	}
	andCount := andB.andCount
	rank(andB)
	if len(orB.ids) > 0 {
		rank(orB)
	}
	out.ids = append(andB.ids, orB.ids...)
	out.scores = append(andB.scores, orB.scores...)
	out.andCount = andCount
	out.partial = andCount == 0 && len(out.ids) > 0
	out.mixed = filled && andCount > 0
	return out
}

// sortOutcome sorts ids/scores with a complete, deterministic comparator.
func sortOutcome(out *searchOutcome, mode string, st *hadithStore) {
	idx := make([]int, len(out.ids))
	for i := range idx {
		idx[i] = i
	}
	slug := func(d uint32) string { return st.colls[st.docColl[d]].Info.Slug }
	slices.SortFunc(idx, func(a, b int) int {
		da, db := out.ids[a], out.ids[b]
		sa, sb := out.scores[a], out.scores[b]
		byScore := func() int {
			if sa > sb {
				return -1
			}
			if sa < sb {
				return 1
			}
			return 0
		}
		byNum := func() int { return int(st.docNum[da]) - int(st.docNum[db]) }
		switch mode {
		case "number":
			if c := strings.Compare(slug(da), slug(db)); c != 0 {
				return c
			}
			if c := byNum(); c != 0 {
				return c
			}
			return byScore()
		case "collection":
			if c := strings.Compare(slug(da), slug(db)); c != 0 {
				return c
			}
			if c := byScore(); c != 0 {
				return c
			}
			return byNum()
		default:
			if c := byScore(); c != 0 {
				return c
			}
			if c := priorityOf(slug(da)) - priorityOf(slug(db)); c != 0 {
				return c
			}
			return byNum()
		}
	})
	ids := make([]uint32, len(idx))
	scores := make([]float32, len(idx))
	for i, k := range idx {
		ids[i], scores[i] = out.ids[k], out.scores[k]
	}
	out.ids, out.scores = ids, scores
}

// tokenMatches reports whether a document token satisfies a query token
// (exact or spelling variant).
func tokenMatches(docTok, qTok string) bool {
	if docTok == qTok {
		return true
	}
	for _, v := range variantOf[qTok] {
		if v == docTok {
			return true
		}
	}
	return false
}

func containsSeq(toks []string, seq []string) bool {
	if len(seq) == 0 || len(toks) < len(seq) {
		return false
	}
outer:
	for i := 0; i+len(seq) <= len(toks); i++ {
		for j, q := range seq {
			if !tokenMatches(toks[i+j], q) {
				continue outer
			}
		}
		return true
	}
	return false
}

// docStreams returns the matan (non-bracket Indonesian) tokens, all Indonesian
// tokens and the Arabic tokens of a record.
func docStreams(h *Collection) (matan, all, ar []string) {
	scanTokens(h.ID, nil, func(norm []byte, _, _ int, _ uint8, inBracket bool) {
		s := string(norm)
		all = append(all, s)
		if !inBracket {
			matan = append(matan, s)
		}
	})
	ar = tokenTexts(h.Arab)
	return
}

// countSeq counts non-overlapping occurrences of seq in toks.
func countSeq(toks []string, seq []string) int {
	n := 0
outer:
	for i := 0; i+len(seq) <= len(toks); {
		for j, q := range seq {
			if !tokenMatches(toks[i+j], q) {
				i++
				continue outer
			}
		}
		n++
		i += len(seq)
	}
	return n
}

// verifyPhrases keeps only documents that contain every quoted phrase. For a
// query made only of quoted phrases every survivor is an exact match, so they
// are ordered by number of occurrences, then authority and number (the BM25
// differences between them are noise); otherwise the BM25 score gets the
// phrase bonus.
func verifyPhrases(q *parsedQuery, b *searchOutcome, st *hadithStore) *searchOutcome {
	ids, scores := b.ids, b.scores
	if len(ids) > maxPhraseVerify {
		log.Printf("search: phrase verification truncated to %d of %d candidates", maxPhraseVerify, len(ids))
		tmp := &searchOutcome{ids: ids, scores: scores}
		sortOutcome(tmp, "relevance", st)
		ids, scores = tmp.ids[:maxPhraseVerify], tmp.scores[:maxPhraseVerify]
	}
	// Records are read and tokenized in small chunks so a broad phrase query
	// never holds thousands of decoded hadith in memory at once.
	out := &searchOutcome{}
	for start := 0; start < len(ids); start += readChunk {
		end := min(start+readChunk, len(ids))
		recs, err := st.readDocs(ids[start:end])
		if err != nil {
			log.Printf("search: phrase verify: %v", err)
			return out
		}
		for k := range recs {
			_, all, ar := docStreams(&recs[k])
			occ := 0
			for _, ph := range q.phrases {
				c := countSeq(all, ph) + countSeq(ar, ph)
				if c == 0 {
					occ = 0
					break
				}
				occ += c
			}
			if occ == 0 {
				continue
			}
			i := start + k
			out.ids = append(out.ids, ids[i])
			out.andCount++
			if q.phraseOnly {
				out.scores = append(out.scores, float32(occ))
			} else {
				out.scores = append(out.scores, scores[i]*phraseBonus)
			}
		}
	}
	return out
}

// longestRun returns the length of the longest contiguous run of seq tokens
// that appears in toks, ignoring runs made only of stopwords.
func longestRun(toks, seq []string) int {
	best := 0
	for i := range toks {
		for j := range seq {
			k := 0
			content := false
			for i+k < len(toks) && j+k < len(seq) && tokenMatches(toks[i+k], seq[j+k]) {
				if !isStopword(seq[j+k]) {
					content = true
				}
				k++
			}
			if k > best && content {
				best = k
			}
		}
	}
	return best
}

// applyPhraseBonus rewards proximity for the top documents: the score is
// multiplied by up to phraseBonus (whole query found as a contiguous phrase in
// the matan or Arabic text), proportionally for a shorter contiguous run of
// at least two query words (e.g. "sebagian dari iman" for
// "kebersihan sebagian dari iman").
func applyPhraseBonus(q *parsedQuery, out *searchOutcome, st *hadithStore) {
	top := min(len(out.ids), phraseBonusTopN)
	n := float32(len(q.seq))
	for start := 0; start < top; start += readChunk {
		end := min(start+readChunk, top)
		recs, err := st.readDocs(out.ids[start:end])
		if err != nil {
			log.Printf("search: phrase bonus: %v", err)
			return
		}
		for k := range recs {
			matan, _, ar := docStreams(&recs[k])
			run := max(longestRun(matan, q.seq), longestRun(ar, q.seq))
			if run >= 2 {
				out.scores[start+k] *= 1 + (phraseBonus-1)*float32(run)/n
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Batch record reads
// ---------------------------------------------------------------------------

// readDocs reads records by global doc id, opening each collection file once.
func (s *hadithStore) readDocs(ids []uint32) ([]Collection, error) {
	out := make([]Collection, len(ids))
	byColl := map[uint8][]int{}
	for i, d := range ids {
		byColl[s.docColl[d]] = append(byColl[s.docColl[d]], i)
	}
	for ci, positions := range byColl {
		c := s.colls[ci]
		if err := c.readMany(s.docNum, ids, positions, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readMany reads the records ids[p] (p in positions) of this collection into out[p].
// Records are located by hadith number (binary search), so this stays correct
// even if the file was rewritten with records added or removed.
func (c *collStore) readMany(docNum []int32, ids []uint32, positions []int, out []Collection) error {
	c.refreshIfChanged()
	c.mu.RLock()
	defer c.mu.RUnlock()
	f, err := os.Open(c.path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf []byte
	for _, p := range positions {
		num := int(docNum[ids[p]])
		i, ok := c.indexOfLocked(num)
		if !ok {
			out[p] = Collection{Number: num, ID: "(hadits tidak ditemukan; data sedang diperbarui)"}
			continue
		}
		size := int(c.sizes[i])
		if cap(buf) < size {
			buf = make([]byte, size)
		}
		b := buf[:size]
		if _, err := f.ReadAt(b, c.offsets[i]); err != nil {
			return err
		}
		if err := json.Unmarshal(b, &out[p]); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

var searchDebug = os.Getenv("SEARCH_DEBUG") == "1"

// searchSlots bounds how many uncached searches run at once, which bounds the
// transient memory of scoring arrays and phrase verification under bursts.
var searchSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

// runSearch returns the (cached) ranked outcome of a parsed query.
func runSearch(q *parsedQuery, f SearchFilters) *searchOutcome {
	t0 := time.Now()
	out, cached := searchCache.getOrCompute(searchCacheKey(q, f), func() *searchOutcome {
		searchSlots <- struct{}{}
		defer func() { <-searchSlots }()
		return executeSearch(q, f, searchIdx, store)
	})
	if d := time.Since(t0); searchDebug || d > 300*time.Millisecond {
		log.Printf("search q=%q lang=%s terms=%d results=%d partial=%v cached=%v in %v", f.Query, f.Language, len(q.terms), len(out.ids), out.partial, cached, d.Round(time.Microsecond))
	}
	return out
}

// parseSearchFilters reads every filter. collections accepts both the comma
// form (?collections=a,b) and the repeated form (?collections=a&collections=b);
// unknown slugs are dropped.
func parseSearchFilters(r *http.Request) SearchFilters {
	v := r.URL.Query()
	f := SearchFilters{
		Query:    capRunes(strings.TrimSpace(v.Get("q")), maxQueryRunes),
		Language: v.Get("lang"),
		SortBy:   v.Get("sort"),
	}
	if f.Language != "ar" && f.Language != "id" {
		f.Language = "all"
	}
	if f.SortBy != "number" && f.SortBy != "collection" {
		f.SortBy = "relevance"
	}
	seen := map[string]bool{}
	for _, raw := range v["collections"] {
		for _, part := range strings.Split(raw, ",") {
			slug := strings.ToLower(strings.TrimSpace(part))
			if slug == "" || seen[slug] {
				continue
			}
			if _, ok := getBySlug(slug); ok {
				seen[slug] = true
				f.Collections = append(f.Collections, slug)
			}
		}
	}
	if n, err := strconv.Atoi(v.Get("min")); err == nil && n > 0 {
		f.NumberRange.Min = n
	}
	if n, err := strconv.Atoi(v.Get("max")); err == nil && n > 0 {
		f.NumberRange.Max = n
	}
	f.TextSearch = v.Get("text") == "1"
	return f
}

// SearchResult is one rendered result card.
type SearchResult struct {
	Collection  string        `json:"collection"`
	Slug        string        `json:"slug"`
	Hadith      Collection    `json:"hadith"`
	Context     string        `json:"context"`
	Score       int           `json:"score"` // relative score x100 (kept for template compatibility)
	Relative    float64       `json:"relative"`
	Snippet     template.HTML `json:"-"`
	ArabSnippet template.HTML `json:"-"`
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	filters := parseSearchFilters(r)
	if filters.Query == "" {
		http.Redirect(w, r, "/", http.StatusTemporaryRedirect)
		return
	}

	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}

	q := parseQuery(filters.Query, searchIdx)
	// "bukhari 1", "HR. Muslim no. 8": the query is only a hadith reference.
	// Show the hadith itself and offer the full-text search as a link.
	directOnly := q.direct != nil && !filters.TextSearch
	var out *searchOutcome
	if directOnly {
		out = &searchOutcome{direct: q.direct}
	} else {
		out = runSearch(q, filters)
	}
	totalItems := len(out.ids)
	totalPages := (totalItems + ItemsPerPage - 1) / ItemsPerPage
	if page > totalPages && totalPages > 0 {
		page = totalPages
	}
	startIndex := (page - 1) * ItemsPerPage
	endIndex := min(startIndex+ItemsPerPage, totalItems)
	if startIndex > totalItems {
		startIndex = totalItems
	}

	var results []SearchResult
	var topScore float32
	if totalItems > 0 {
		topScore = out.scores[0]
		for _, s := range out.scores[:min(len(out.scores), 50)] {
			topScore = max(topScore, s)
		}
		pageIDs := out.ids[startIndex:endIndex]
		recs, err := store.readDocs(pageIDs)
		if err != nil {
			log.Printf("search: reading results: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		hl := q.highlightSet()
		for i, d := range pageIDs {
			c := store.colls[store.docColl[d]]
			rel := 0.0
			if topScore > 0 {
				rel = float64(out.scores[startIndex+i] / topScore)
			}
			res := SearchResult{
				Collection: c.Info.Name,
				Slug:       c.Info.Slug,
				Hadith:     Collection{Number: recs[i].Number},
				Score:      int(math.Round(rel * 100)),
				Relative:   rel,
			}
			res.Snippet = buildSnippet(recs[i].ID, hl, scriptLatin, 34)
			res.ArabSnippet = buildSnippet(recs[i].Arab, hl, scriptArabic, 26)
			results = append(results, res)
		}
	}

	var direct *SearchResult
	if out.direct != nil {
		if c, ok := getBySlug(out.direct.slug); ok {
			if i, ok := c.indexOf(out.direct.number); ok {
				if h, err := c.read(i); err == nil {
					direct = &SearchResult{
						Collection:  c.Info.Name,
						Slug:        c.Info.Slug,
						Hadith:      Collection{Number: h.Number},
						Snippet:     buildSnippet(h.ID, nil, scriptLatin, 60),
						ArabSnippet: buildSnippet(h.Arab, nil, scriptArabic, 45),
					}
				}
			}
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

	seo := SEOData{
		Title:       "Hasil Pencarian: " + filters.Query + " - hadits.online",
		Description: "Temukan " + strconv.Itoa(totalItems) + " hadits terkait " + filters.Query + " di hadits.online.",
		Keywords:    "cari hadits " + filters.Query + ", hasil pencarian " + filters.Query,
		Canonical:   siteURL + "/search?q=" + urlEncode(filters.Query),
		Robots:      "noindex, follow",
	}.withDefaults()

	pageData := struct {
		SEO           SEOData
		Query         string
		Filters       SearchFilters
		Results       []SearchResult
		Pagination    Pagination
		TotalItems    int
		Collections   []CollectionInfo
		Partial       bool
		Mixed         bool
		DirectOnly    bool
		TextSearchURL string
		ActiveFilters int
		DirectHit     *SearchResult
		From, To      int
	}{
		SEO:         seo,
		Query:       filters.Query,
		Filters:     filters,
		Results:     results,
		Pagination:  pagination,
		TotalItems:  totalItems,
		Collections: data.Info,
		Partial:     out.partial,
		Mixed:       out.mixed,
		DirectOnly:  directOnly,
		TextSearchURL: func() string {
			tf := filters
			tf.TextSearch = true
			return buildFilterURL(filters.Query, tf, 1)
		}(),
		ActiveFilters: activeFilterCount(filters),
		DirectHit:     direct,
		From:          startIndex + 1,
		To:            endIndex,
	}
	w.Header().Set("X-Robots-Tag", "noindex, follow")
	w.Header().Set("Cache-Control", ccSearch)
	render(w, "search.html", http.StatusOK, pageData)
}

// ---------------------------------------------------------------------------
// Snippets with server-side highlighting
// ---------------------------------------------------------------------------

// highlightSet is every normalized form that counts as a hit (all expansions
// of all query terms, plus quoted phrase tokens).
func (q *parsedQuery) highlightSet() map[string]bool {
	hl := map[string]bool{}
	for _, t := range q.terms {
		hl[t.text] = true
		for _, e := range t.exp {
			hl[searchIdx.termList[e.id]] = true
		}
	}
	for _, ph := range q.phrases {
		for _, t := range ph {
			hl[t] = true
		}
	}
	return hl
}

const markOpen = `<mark class="hl">`

// buildSnippet picks the window of about `window` tokens with the most hits
// (matan hits count double) and returns escaped HTML with <mark> around hits
// and the [narrator] -> <strong> convention. Cuts happen on token boundaries.
func buildSnippet(text string, hl map[string]bool, script uint8, window int) template.HTML {
	all := tokenize(text)
	toks := all[:0:0]
	for _, t := range all {
		if t.script == script {
			toks = append(toks, t)
		}
	}
	if len(toks) == 0 {
		return template.HTML(template.HTMLEscapeString(capRunes(text, 300)))
	}
	hit := make([]bool, len(toks))
	weight := make([]int, len(toks))
	anyHit := false
	for i, t := range toks {
		if hl != nil && hl[t.text] {
			hit[i] = true
			anyHit = true
			if t.inBracket {
				weight[i] = 1
			} else {
				weight[i] = 2
			}
		}
	}
	start := 0
	if len(toks) > window {
		if anyHit {
			best, cur := -1, 0
			for i := 0; i < len(toks); i++ {
				cur += weight[i]
				if i >= window {
					cur -= weight[i-window]
				}
				if i >= window-1 && cur > best {
					best, start = cur, i-window+1
				}
			}
			// Start a few tokens before the first hit in the window for context.
			for i := start; i < start+window && i < len(toks); i++ {
				if hit[i] {
					start = max(0, min(i-6, len(toks)-window))
					break
				}
			}
		} else if script == scriptLatin {
			// No hit: lead with the matan (first token after the isnad).
			for i, t := range toks {
				if !t.inBracket && i > 0 && toks[i-1].inBracket {
					start = i
				}
			}
			if start > len(toks)-window/2 {
				start = 0
			}
			start = max(0, min(start, len(toks)-window))
		}
	}
	end := min(len(toks), start+window)

	var b strings.Builder
	b.Grow(window * 16)
	if start > 0 {
		b.WriteString("… ")
	}
	from := toks[start].start
	depth := 0
	if script == scriptLatin && toks[start].inBracket {
		b.WriteString("<strong>")
		depth = 1
	}
	writePlain := func(s string) {
		for len(s) > 0 {
			i := strings.IndexAny(s, "[]")
			if i < 0 {
				b.WriteString(template.HTMLEscapeString(s))
				return
			}
			b.WriteString(template.HTMLEscapeString(s[:i]))
			if s[i] == '[' {
				if depth == 0 {
					b.WriteString("<strong>")
				}
				depth++
			} else if depth > 0 {
				depth--
				if depth == 0 {
					b.WriteString("</strong>")
				}
			}
			s = s[i+1:]
		}
	}
	pos := from
	for i := start; i < end; i++ {
		t := toks[i]
		writePlain(text[pos:t.start])
		if hit[i] {
			b.WriteString(markOpen)
			b.WriteString(template.HTMLEscapeString(text[t.start:t.end]))
			b.WriteString("</mark>")
		} else {
			b.WriteString(template.HTMLEscapeString(text[t.start:t.end]))
		}
		pos = t.end
	}
	if end == len(toks) {
		writePlain(text[pos:]) // trailing punctuation / closing quote
	}
	if depth > 0 {
		b.WriteString("</strong>")
	}
	if end < len(toks) {
		b.WriteString(" …")
	}
	return template.HTML(b.String())
}

// activeFilterCount is shown as "Filter (n aktif)".
func activeFilterCount(f SearchFilters) int {
	n := 0
	if len(f.Collections) > 0 {
		n++
	}
	if f.Language != "all" {
		n++
	}
	if f.SortBy != "relevance" {
		n++
	}
	if f.NumberRange.Min > 0 {
		n++
	}
	if f.NumberRange.Max > 0 {
		n++
	}
	return n
}
