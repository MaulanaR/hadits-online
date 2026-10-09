package main

import (
	"strings"
	"unicode/utf8"
)

// Text normalization and tokenization shared by the indexer, the query parser
// and the snippet builder. Pure functions, stdlib only.

const (
	scriptLatin  uint8 = 1
	scriptArabic uint8 = 2
)

type token struct {
	text      string // normalized form
	start     int    // byte offsets in the ORIGINAL string
	end       int
	script    uint8
	inBracket bool // inside [...] (narrator names, i.e. isnad) – Latin only
}

// --- Arabic -----------------------------------------------------------------

// arabicDrop reports harakat, Quranic marks and tatweel that are removed.
func arabicDrop(r rune) bool {
	return (r >= 0x0610 && r <= 0x061A) || (r >= 0x064B && r <= 0x065F) ||
		r == 0x0670 || (r >= 0x06D6 && r <= 0x06ED) || r == 0x0640
}

// arabicFold maps letter variants to one canonical letter (0 = unchanged).
func arabicFold(r rune) rune {
	switch r {
	case 'أ', 'إ', 'آ', 'ٱ':
		return 'ا'
	case 'ى', 'ی':
		return 'ي'
	case 'ة':
		return 'ه'
	case 'ؤ':
		return 'و'
	case 'ئ':
		return 'ي'
	case 'ک':
		return 'ك'
	}
	return 0
}

func isArabicLetter(r rune) bool {
	return (r >= 0x0621 && r <= 0x063A) || (r >= 0x0641 && r <= 0x064A) ||
		r == 0x0671 || r == 0x06A9 || r == 0x06CC
}

// normalizeArabic strips harakat/tatweel and folds alef/hamza/ya/ta-marbuta forms.
func normalizeArabic(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if arabicDrop(r) {
			continue
		}
		if f := arabicFold(r); f != 0 {
			r = f
		}
		b.WriteRune(r)
	}
	return b.String()
}

// --- Latin ------------------------------------------------------------------

func isApostrophe(r rune) bool {
	return r == '\'' || r == '’' || r == '‘' || r == 'ʼ' || r == '`' || r == 'ʻ'
}

// latinFold lowercases ASCII and folds common accented / transliteration
// letters to ASCII. Returns 0 if r is not a Latin letter or digit.
func latinFold(r rune) rune {
	switch {
	case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return r
	case r >= 'A' && r <= 'Z':
		return r + ('a' - 'A')
	case r < 0xC0:
		return 0
	}
	switch r {
	case 'à', 'á', 'â', 'ã', 'ä', 'å', 'ā', 'À', 'Á', 'Â', 'Ã', 'Ä', 'Å', 'Ā':
		return 'a'
	case 'è', 'é', 'ê', 'ë', 'ē', 'È', 'É', 'Ê', 'Ë', 'Ē':
		return 'e'
	case 'ì', 'í', 'î', 'ï', 'ī', 'Ì', 'Í', 'Î', 'Ï', 'Ī':
		return 'i'
	case 'ò', 'ó', 'ô', 'õ', 'ö', 'ō', 'Ò', 'Ó', 'Ô', 'Õ', 'Ö', 'Ō':
		return 'o'
	case 'ù', 'ú', 'û', 'ü', 'ū', 'Ù', 'Ú', 'Û', 'Ü', 'Ū':
		return 'u'
	case 'ñ', 'Ñ':
		return 'n'
	case 'ç', 'Ç':
		return 'c'
	case 'ḥ', 'Ḥ':
		return 'h'
	case 'ṣ', 'Ṣ':
		return 's'
	case 'ḍ', 'Ḍ':
		return 'd'
	case 'ṭ', 'Ṭ':
		return 't'
	case 'ẓ', 'Ẓ':
		return 'z'
	}
	return 0
}

// normalizeLatin lowercases, removes apostrophes and folds diacritics.
// Other characters are kept as-is.
func normalizeLatin(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isApostrophe(r) {
			continue
		}
		if f := latinFold(r); f != 0 {
			r = f
		} else if r < utf8.RuneSelf && r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// --- Tokenizer --------------------------------------------------------------

// scanTokens walks s and calls fn for each token. norm is only valid during
// the call (it aliases buf, which is returned for reuse). Latin tokens are
// maximal runs of letters/digits (apostrophes inside a word are dropped, so
// "Jum'at" -> "jumat"); Arabic tokens are runs of Arabic letters with harakat
// removed and letters folded. '[' ... ']' depth marks isnad tokens.
func scanTokens(s string, buf []byte, fn func(norm []byte, start, end int, script uint8, inBracket bool)) []byte {
	depth := 0
	i := 0
	n := len(s)
	for i < n {
		r, size := rune(s[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		switch {
		case r == '[':
			depth++
			i += size
		case r == ']':
			if depth > 0 {
				depth--
			}
			i += size
		case latinFold(r) != 0:
			start := i
			buf = buf[:0]
			for i < n {
				r, size = rune(s[i]), 1
				if r >= utf8.RuneSelf {
					r, size = utf8.DecodeRuneInString(s[i:])
				}
				if f := latinFold(r); f != 0 {
					buf = append(buf, byte(f))
					i += size
					continue
				}
				// apostrophe between two letters: skip it and continue the word
				if isApostrophe(r) && i+size < n {
					r2, _ := utf8.DecodeRuneInString(s[i+size:])
					if latinFold(r2) != 0 {
						i += size
						continue
					}
				}
				break
			}
			fn(buf, start, i, scriptLatin, depth > 0)
		case isArabicLetter(r):
			start := i
			buf = buf[:0]
			end := i
			for i < n {
				r, size = utf8.DecodeRuneInString(s[i:])
				if isArabicLetter(r) {
					if f := arabicFold(r); f != 0 {
						r = f
					}
					buf = utf8.AppendRune(buf, r)
					i += size
					end = i
					continue
				}
				if arabicDrop(r) {
					i += size
					end = i
					continue
				}
				break
			}
			if len(buf) > 0 {
				fn(buf, start, end, scriptArabic, depth > 0)
			}
		default:
			i += size
		}
	}
	return buf
}

// tokenize returns all tokens of s with offsets into s.
func tokenize(s string) []token {
	var out []token
	scanTokens(s, nil, func(norm []byte, start, end int, script uint8, inBracket bool) {
		out = append(out, token{text: string(norm), start: start, end: end, script: script, inBracket: inBracket})
	})
	return out
}

// tokenScript reports the script of a normalized term.
func tokenScript(t string) uint8 {
	if t == "" {
		return 0
	}
	if t[0] < utf8.RuneSelf {
		return scriptLatin
	}
	return scriptArabic
}

// --- Indonesian stemmer -----------------------------------------------------

var (
	idParticles   = []string{"lah", "kah", "tah", "pun"}
	idPossessives = []string{"nya", "ku", "mu"}
	idSuffixes    = []string{"kan", "an", "i"}
)

func isVowel(b byte) bool { return b == 'a' || b == 'i' || b == 'u' || b == 'e' || b == 'o' }

// idPrefixForms returns the possible remainders after removing one prefix,
// including the nasal recoding rules (meny+V -> s, mem+V -> p, men+V -> t,
// meng+V -> k, same for pe-).
func idPrefixForms(w string) []string {
	var out []string
	add := func(s string) {
		if len(s) >= 3 {
			out = append(out, s)
		}
	}
	type pr struct {
		p      string
		recode byte // letter restored before a vowel (0 = none)
	}
	prefixes := []pr{
		{"meng", 'k'}, {"meny", 's'}, {"mem", 'p'}, {"men", 't'}, {"me", 0},
		{"peng", 'k'}, {"peny", 's'}, {"pem", 'p'}, {"pen", 't'}, {"per", 0}, {"pe", 0},
		{"ber", 0}, {"ter", 0}, {"di", 0}, {"ke", 0}, {"se", 0},
	}
	for _, p := range prefixes {
		if !strings.HasPrefix(w, p.p) || len(w) <= len(p.p)+2 {
			continue
		}
		rest := w[len(p.p):]
		add(rest)
		if p.recode != 0 && isVowel(rest[0]) {
			add(string(p.recode) + rest)
		}
	}
	// be-/te- only before r ("berenang", "terasa"), bel-/pel- only before "ajar".
	if strings.HasPrefix(w, "be") && len(w) > 4 && w[2] == 'r' {
		add(w[2:])
	}
	if strings.HasPrefix(w, "te") && len(w) > 4 && w[2] == 'r' {
		add(w[2:])
	}
	if strings.HasPrefix(w, "belajar") || strings.HasPrefix(w, "pelajar") {
		add(w[3:])
	}
	return out
}

func idSuffixForms(w string) []string {
	out := []string{w}
	for _, p := range idParticles {
		if strings.HasSuffix(w, p) && len(w)-len(p) >= 3 {
			out = append(out, w[:len(w)-len(p)])
		}
	}
	base := append([]string(nil), out...)
	for _, b := range base {
		for _, p := range idPossessives {
			if strings.HasSuffix(b, p) && len(b)-len(p) >= 3 {
				out = append(out, b[:len(b)-len(p)])
			}
		}
	}
	base = append([]string(nil), out...)
	for _, b := range base {
		for _, p := range idSuffixes {
			if strings.HasSuffix(b, p) && len(b)-len(p) >= 3 {
				out = append(out, b[:len(b)-len(p)])
			}
		}
	}
	return out
}

// stemID is a light Indonesian stemmer that uses the corpus vocabulary as its
// dictionary. df returns the document frequency of a term (0 = not in the
// corpus). Every (suffix-stripped) x (optionally prefix-stripped) candidate is
// generated; among candidates that exist in the corpus the most frequent one
// wins (ties -> shorter). It is applied up to three times so chains converge
// (menikahi -> menikah -> nikah). Words of 4 letters or less are kept.
//
// Deviation from the plan text ("first accepted candidate"): with a corpus
// vocabulary every inflected form is a word, so "first accepted" would stop at
// menikahi -> menikah; picking the most frequent root groups the family.
func stemID(word string, df func(string) int) string {
	w := word
	for iter := 0; iter < 3; iter++ {
		if len(w) <= 4 || tokenScript(w) != scriptLatin {
			return w
		}
		best, bestDF := "", 0
		consider := func(c string) {
			if c == w || len(c) < 3 {
				return
			}
			d := df(c)
			if d == 0 {
				return
			}
			if d > bestDF || (d == bestDF && len(c) < len(best)) {
				best, bestDF = c, d
			}
		}
		for _, s := range idSuffixForms(w) {
			consider(s)
			for _, p := range idPrefixForms(s) {
				consider(p)
			}
		}
		if best == "" {
			return w
		}
		w = best
	}
	return w
}

// --- Arabic light stemmer (Light10 style) -------------------------------------

var (
	arPrefixes = []string{"وال", "بال", "كال", "فال", "لل", "ال", "و"}
	arSuffixes = []string{"ها", "ان", "ات", "ون", "ين", "يه", "ه", "ي"}
)

// stemAR strips one article/conjunction prefix and then suffixes while at
// least two letters remain. Used only as a grouping key.
func stemAR(word string) string {
	w := word
	for _, p := range arPrefixes {
		if strings.HasPrefix(w, p) && utf8.RuneCountInString(w)-utf8.RuneCountInString(p) >= 2 {
			w = w[len(p):]
			break
		}
	}
	for _, s := range arSuffixes {
		if strings.HasSuffix(w, s) && utf8.RuneCountInString(w)-utf8.RuneCountInString(s) >= 2 {
			w = w[:len(w)-len(s)]
		}
	}
	return w
}

// --- Variants and stopwords -----------------------------------------------------

// variantGroups lists spellings that must match each other (applied at query
// time in both directions).
var variantGroups = [][]string{
	{"shalat", "salat", "sholat", "solat"},
	{"ramadhan", "ramadan"},
	{"wudhu", "wudlu", "wudu"},
	{"sedekah", "shadaqah", "shodaqoh", "sadaqah", "shadaqoh"},
	{"fitrah", "fithri", "fitri"},
	{"dzikir", "zikir"},
	{"jumat", "jumuah"},
	{"subuh", "shubuh", "shubh"},
	{"dzuhur", "zhuhur", "zuhur", "dhuhur"},
	{"ashar", "asar"},
	{"maghrib", "magrib"},
	{"isya", "isyak"},
	{"tahajud", "tahajjud"},
	{"qurban", "kurban"},
	{"umrah", "umroh"},
	{"quran", "alquran"},
	{"hadits", "hadis"},
	{"shaum", "puasa"},
}

var variantOf = func() map[string][]string {
	m := make(map[string][]string)
	for _, g := range variantGroups {
		for _, w := range g {
			m[w] = g
		}
	}
	return m
}()

func makeSet(words string) map[string]bool {
	m := make(map[string]bool)
	for _, w := range strings.Fields(words) {
		m[w] = true
	}
	return m
}

// idStopwords: isnad boilerplate and function words. nabi, rasulullah, allah
// and abu are deliberately NOT stopwords.
var idStopwords = makeSet(`telah menceritakan mengabarkan kepada kami dari bin binti berkata dan yang ia dia di
	itu ini dengan bahwa maka lalu pada untuk ke ada akan dalam adalah atau juga kemudian
	shallallahu alaihi wasallam radhiyallahu anhu anha`)

// arStopwords are normalized forms. علي is NOT a stopword (it is also the name
// Ali). الله is a stopword unless it is the only query term (handled by the
// "all stopwords" rule in the query parser).
var arStopwords = makeSet(normalizeArabic(`حدثنا اخبرنا حدثني اخبرني عن قال ان في من إلى بن ابن ابي ابو صلى عليه وسلم الله`))

func isStopword(t string) bool {
	if tokenScript(t) == scriptArabic {
		return arStopwords[t]
	}
	return idStopwords[t]
}
