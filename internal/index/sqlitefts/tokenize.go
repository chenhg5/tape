package sqlitefts

import (
	"strings"
	"unicode"
)

// Tokenize splits text into index tokens: latin/digit runs become lowercased
// words; CJK runs become overlapping bigrams ("会话备份" -> 会话 话备 备份).
// This is the Lucene CJKAnalyzer approach: dictionary-free and stable for
// mixed Chinese/English content, which SQLite's built-in tokenizers (and
// porter, as used by cass) cannot search at all.
func Tokenize(text string) []string {
	var tokens []string
	var word []rune // current latin/digit run
	var cjk []rune  // current CJK run

	flushWord := func() {
		if len(word) > 0 {
			tokens = append(tokens, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushCJK := func() {
		switch len(cjk) {
		case 0:
		case 1:
			tokens = append(tokens, string(cjk))
		default:
			for i := 0; i+1 < len(cjk); i++ {
				tokens = append(tokens, string(cjk[i:i+2]))
			}
		}
		cjk = cjk[:0]
	}

	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return tokens
}

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
}

// buildMatch turns a user query into an FTS5 MATCH expression over
// pre-tokenized content. Latin words match by prefix; each CJK run becomes
// a phrase of bigrams so adjacency is preserved.
func buildMatch(query string) string {
	var parts []string
	var word []rune
	var cjk []rune

	flushWord := func() {
		if len(word) > 0 {
			parts = append(parts, `"`+escapeFTS(strings.ToLower(string(word)))+`"*`)
			word = word[:0]
		}
	}
	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		toks := Tokenize(string(cjk))
		for i := range toks {
			toks[i] = escapeFTS(toks[i])
		}
		parts = append(parts, `"`+strings.Join(toks, " ")+`"`)
		cjk = cjk[:0]
	}

	for _, r := range query {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return strings.Join(parts, " ") // space = implicit AND in FTS5
}

func escapeFTS(s string) string {
	return strings.ReplaceAll(s, `"`, `""`)
}
