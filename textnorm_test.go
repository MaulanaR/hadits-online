package main

import (
	"strings"
	"testing"
)

func TestNormArabic(t *testing.T) {
	cases := map[string]string{
		"الصَّلَاةَ":   "الصلاه",
		"إِنَّمَا":     "انما",
		"الْأَعْمَالُ": "الاعمال",
		"عَلَى":        "علي",
		"ـــقال":       "قال",
		"مُؤْمِن":      "مومن",
	}
	for in, want := range cases {
		if got := normalizeArabic(in); got != want {
			t.Errorf("normalizeArabic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormLatin(t *testing.T) {
	cases := map[string]string{"Jum'at": "jumat", "Do’a": "doa", "Ḥadīth": "hadith", "ÉTÉ": "ete"}
	for in, want := range cases {
		if got := normalizeLatin(in); got != want {
			t.Errorf("normalizeLatin(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormTokenize(t *testing.T) {
	s := "Telah menceritakan kepada kami [Al Humaidi Abdullah] dia berkata; Rasulullah bersabda: \"Shalat Jum'at itu (wajib)\" 12 kali - إِنَّمَا الْأَعْمَالُ بِالنِّيَّاتِ"
	toks := tokenize(s)
	var texts []string
	for _, tk := range toks {
		texts = append(texts, tk.text)
		orig := s[tk.start:tk.end]
		var norm string
		if tk.script == scriptArabic {
			norm = normalizeArabic(orig)
		} else {
			norm = normalizeLatin(orig)
		}
		if norm != tk.text {
			t.Errorf("offset round trip: %q -> %q, token %q", orig, norm, tk.text)
		}
		if tk.text == "humaidi" && !tk.inBracket {
			t.Error("humaidi should be inBracket")
		}
		if tk.text == "rasulullah" && tk.inBracket {
			t.Error("rasulullah should not be inBracket")
		}
	}
	want := "telah menceritakan kepada kami al humaidi abdullah dia berkata rasulullah bersabda shalat jumat itu wajib 12 kali انما الاعمال بالنيات"
	if got := strings.Join(texts, " "); got != want {
		t.Errorf("tokens:\n got %s\nwant %s", got, want)
	}
}

func TestNormTokenizeBukhari1(t *testing.T) {
	s := "Telah menceritakan kepada kami [Al Humaidi Abdullah bin Az Zubair] dia berkata, Telah menceritakan kepada kami [Sufyan] yang berkata"
	for _, tk := range tokenize(s) {
		if tk.text == "humaidi" && !tk.inBracket {
			t.Fatal("Humaidi must be inBracket")
		}
		if tk.text == "sufyan" && !tk.inBracket {
			t.Fatal("Sufyan must be inBracket")
		}
		if tk.text == "berkata" && tk.inBracket {
			t.Fatal("berkata must not be inBracket")
		}
	}
}

func TestNormStemID(t *testing.T) {
	vocab := map[string]int{
		"nikah": 500, "menikah": 300, "menikahi": 200, "pernikahan": 100,
		"sabar": 400, "kesabaran": 50,
		"sedekah": 475, "bersedekah": 60,
		"kabar": 300, "mengabarkan": 9000, "kabarkan": 5,
		"niat": 200, "berniat": 30, "niatnya": 120,
		"kata": 5000, "berkata": 30000, "katakan": 400, "mengatakan": 300,
		"senyum": 40, "tersenyum": 80,
		"bersih": 90, "kebersihan": 10,
		"tuntut": 20, "menuntut": 60,
	}
	df := func(s string) int { return vocab[s] }
	cases := map[string]string{
		"menikahi": "nikah", "pernikahan": "nikah", "menikah": "nikah",
		"kesabaran": "sabar", "bersedekah": "sedekah", "mengabarkan": "kabar",
		"berniat": "niat", "niatnya": "niat", "mengatakan": "kata", "berkata": "kata",
		"tersenyum": "senyum", "kebersihan": "bersih", "menuntut": "tuntut",
		"nikah": "nikah", "sabar": "sabar",
	}
	for in, want := range cases {
		if got := stemID(in, df); got != want {
			t.Errorf("stemID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormStemAR(t *testing.T) {
	cases := map[string]string{
		"الصلاه": "صلا", "والصلاه": "صلا", "بالصلاه": "صلا", "للصلاه": "صلا",
		"المسلمون": "مسلم", "المسلمين": "مسلم",
	}
	for in, want := range cases {
		if got := stemAR(in); got != want {
			t.Errorf("stemAR(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormStopwords(t *testing.T) {
	for _, w := range []string{"dan", "yang", "telah", "حدثنا", "عن", "الله"} {
		if !isStopword(normalizeArabicOrLatin(w)) {
			t.Errorf("%q should be a stopword", w)
		}
	}
	for _, w := range []string{"nabi", "rasulullah", "allah", "abu", "علي"} {
		if isStopword(normalizeArabicOrLatin(w)) {
			t.Errorf("%q should not be a stopword", w)
		}
	}
}

func normalizeArabicOrLatin(s string) string {
	toks := tokenize(s)
	if len(toks) == 0 {
		return ""
	}
	return toks[0].text
}
