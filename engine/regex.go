package engine

import (
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dlclark/regexp2"
)

/*
Postgres' regular expressions anchor on words with escapes Go's RE2 does not
have: \m (start of a word), \M (end of one), \y (either) and \Y (neither).
RE2 cannot say them — \m needs a word character after it and none before,
and RE2 has no lookaround — so a pattern that uses one runs on regexp2, a
backtracking engine with lookarounds, with each escape spelled as one. Every
other pattern stays on RE2, as before.

A word character is a letter, a digit or an underscore in any script, as in
Postgres with a UTF-8 database: RE2's \b and \w know only ASCII, so the
ã in "vinilão" would have ended a word.

\Z, the end of the string, is RE2's \z.
*/

const (
	wordClass    = `[\p{L}\p{N}_]`
	wordStart    = `(?<!` + wordClass + `)(?=` + wordClass + `)`
	wordEnd      = `(?<=` + wordClass + `)(?!` + wordClass + `)`
	wordBound    = `(?:` + wordStart + `|` + wordEnd + `)`
	wordInterior = `(?:(?<=` + wordClass + `)(?=` + wordClass + `)|(?<!` + wordClass + `)(?!` + wordClass + `))`
)

// matchTimeout bounds a backtracking match, which RE2 never needs.
const matchTimeout = 5 * time.Second

// translateRegex rewrites a Postgres pattern for Go. lookaround reports that
// it uses a word anchor, so it needs regexp2.
func translateRegex(pattern string) (out string, lookaround bool) {
	var b strings.Builder
	bracket := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch {
		case c == '\\' && i+1 < len(pattern):
			next := pattern[i+1]
			i++
			if bracket {
				b.WriteByte(c)
				b.WriteByte(next)
				continue
			}
			switch next {
			case 'm':
				b.WriteString(wordStart)
				lookaround = true
			case 'M':
				b.WriteString(wordEnd)
				lookaround = true
			case 'y':
				b.WriteString(wordBound)
				lookaround = true
			case 'Y':
				b.WriteString(wordInterior)
				lookaround = true
			case 'Z':
				b.WriteString(`\z`)
			default:
				b.WriteByte(c)
				b.WriteByte(next)
			}
		case c == '[' && !bracket:
			bracket = true
			b.WriteByte(c)
			// A ] right after [ or [^ is a member, not the end.
			if i+1 < len(pattern) && pattern[i+1] == '^' {
				b.WriteByte('^')
				i++
			}
			if i+1 < len(pattern) && pattern[i+1] == ']' {
				b.WriteByte(']')
				i++
			}
		case c == ']' && bracket:
			bracket = false
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), lookaround
}

// matcher is a compiled pattern on either engine.
type matcher func(string) (bool, error)

var (
	regexCacheMu sync.Mutex
	regexCache   = map[string]matcher{}
)

// regexCacheMax bounds the cache; a statement builds its patterns from data
// rarely, and then the cache starts over rather than grow.
const regexCacheMax = 512

// compileRegex compiles a Postgres pattern once and reuses it: a WHERE runs
// the regexp function once per row with the same pattern.
func compileRegex(pattern string) (matcher, error) {
	regexCacheMu.Lock()
	m, ok := regexCache[pattern]
	regexCacheMu.Unlock()
	if ok {
		return m, nil
	}

	translated, lookaround := translateRegex(pattern)
	if lookaround {
		re, err := regexp2.Compile(translated, regexp2.RE2|regexp2.Unicode)
		if err != nil {
			return nil, err
		}
		re.MatchTimeout = matchTimeout
		m = re.MatchString
	} else {
		re, err := regexp.Compile(translated)
		if err != nil {
			return nil, err
		}
		m = func(s string) (bool, error) { return re.MatchString(s), nil }
	}

	regexCacheMu.Lock()
	if len(regexCache) >= regexCacheMax {
		regexCache = map[string]matcher{}
	}
	regexCache[pattern] = m
	regexCacheMu.Unlock()
	return m, nil
}
