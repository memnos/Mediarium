package metadata

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Release names are written with the title a film or show has where the release
// was made: a German release of "The Lord of the Rings: The Fellowship of the
// Ring" is called "Der Herr der Ringe Die Gefaehrten". TMDB lists those other
// names, as translations (per language) and as alternative titles (per
// country). This file reads them and picks the few worth searching for.

// MaxExtraTitles is how many other titles are kept for one movie or show, and
// so the most extra searches one search makes.
const MaxExtraTitles = 4

// CountryName is an alternative title TMDB lists for a country.
type CountryName struct {
	Country string // "DE"
	Name    string
	Type    string // "" for an ordinary title; TMDB also has "working title" and the like
}

// Translation is a title TMDB has in a language.
type Translation struct {
	Language string // "de"
	Country  string // "DE"
	Name     string
}

// NameSources is what TMDB knows about the other names of one movie or show.
type NameSources struct {
	// Original is the title in the language the title was made in, and
	// OriginalLanguage that language's code ("ja").
	Original         string
	OriginalLanguage string
	Alternatives     []CountryName
	Translations     []Translation
}

// titleNamesResponse is the single-title answer with the alternative titles and
// the translations appended (append_to_response asks for the two list endpoints
// in the same request). Movies and shows spell a few fields differently.
type titleNamesResponse struct {
	OriginalTitle    string `json:"original_title"`
	OriginalName     string `json:"original_name"`
	OriginalLanguage string `json:"original_language"`
	Alternative      struct {
		Titles  []altTitleJSON `json:"titles"`  // movies
		Results []altTitleJSON `json:"results"` // shows
	} `json:"alternative_titles"`
	Translations struct {
		Translations []struct {
			Country  string `json:"iso_3166_1"`
			Language string `json:"iso_639_1"`
			Data     struct {
				Title string `json:"title"` // movies
				Name  string `json:"name"`  // shows
			} `json:"data"`
		} `json:"translations"`
	} `json:"translations"`
}

type altTitleJSON struct {
	Country string `json:"iso_3166_1"`
	Title   string `json:"title"`
	Type    string `json:"type"`
}

// TitleNameSources fetches the original title, the alternative titles and the
// translations of a movie (kind "movie") or a show (kind "tv"). It is one request,
// and the answer is kept for a few minutes, so asking again for the same title
// (as a search for several wanted things does) costs nothing.
func (c *Client) TitleNameSources(ctx context.Context, kind string, tmdbID int) (NameSources, error) {
	if kind != "movie" && kind != "tv" {
		return NameSources{}, fmt.Errorf("title names: %q is not movie or tv", kind)
	}
	key := fmt.Sprintf("names:%s:%d", kind, tmdbID)
	if v, ok := c.pages.get(key); ok {
		return v.(NameSources), nil
	}
	var r titleNamesResponse
	q := url.Values{"append_to_response": {"alternative_titles,translations"}}
	if err := c.get(ctx, fmt.Sprintf("/%s/%d", kind, tmdbID), q, &r); err != nil {
		return NameSources{}, err
	}
	out := NameSources{Original: r.OriginalTitle, OriginalLanguage: strings.ToLower(r.OriginalLanguage)}
	if kind == "tv" {
		out.Original = r.OriginalName
	}
	alts := r.Alternative.Titles
	if kind == "tv" {
		alts = r.Alternative.Results
	}
	for _, a := range alts {
		if name := strings.TrimSpace(a.Title); name != "" {
			out.Alternatives = append(out.Alternatives, CountryName{Country: strings.ToUpper(a.Country), Name: name, Type: a.Type})
		}
	}
	for _, t := range r.Translations.Translations {
		name := strings.TrimSpace(t.Data.Title)
		if kind == "tv" {
			name = strings.TrimSpace(t.Data.Name)
		}
		if name != "" {
			out.Translations = append(out.Translations, Translation{Language: strings.ToLower(t.Language), Country: strings.ToUpper(t.Country), Name: name})
		}
	}
	c.pages.put(key, out)
	return out, nil
}

// countriesOf lists the countries whose titles count as a language's, the
// likeliest first. A language not listed here uses the country of the same
// letters ("pl" is PL).
var countriesOf = map[string][]string{
	"de": {"DE", "AT", "CH"},
	"fr": {"FR", "CA", "BE", "CH"},
	"es": {"ES", "MX", "AR", "CO", "CL"},
	"pt": {"PT", "BR"},
	"nl": {"NL", "BE"},
	"it": {"IT"},
	"sv": {"SE"},
	"da": {"DK"},
	"no": {"NO"},
	"nb": {"NO"},
	"cs": {"CZ"},
	"el": {"GR"},
	"uk": {"UA"},
	"he": {"IL"},
	"ja": {"JP"},
	"ko": {"KR"},
	"zh": {"CN", "TW", "HK", "SG"},
	"cn": {"CN", "HK"},
	"hi": {"IN"},
	"ta": {"IN"},
	"te": {"IN"},
	"sr": {"RS"},
	"sl": {"SI"},
	"bs": {"BA"},
	"ar": {"SA", "EG", "AE"},
	"vi": {"VN"},
}

func countriesFor(language string) []string {
	if c, ok := countriesOf[language]; ok {
		return c
	}
	return []string{strings.ToUpper(language)}
}

// ExtraTitles picks the other titles worth searching for: at most max, none
// equal to the English title or to each other (compared as TitleKey does), and
// only in the languages given (two-letter codes, best first, English left out
// by the caller) plus the original title.
//
// The original title comes first. It is kept when its language is one of the
// wanted ones or when it is written in Latin letters: an indexer can do nothing
// with a title in an alphabet no release is named in. Then each wanted
// language gives its translation and the alternative titles of its countries;
// the languages take turns, so a second language is not crowded out by the first.
func ExtraTitles(src NameSources, english string, languages []string, max int) []string {
	if max <= 0 || len(languages) == 0 {
		return nil
	}
	seen := map[string]bool{TitleKey(english): true}
	var out []string
	add := func(name string) bool {
		name = strings.Join(strings.Fields(name), " ")
		key := TitleKey(name)
		if !SearchableKey(key) || seen[key] {
			return false
		}
		seen[key] = true
		out = append(out, name)
		return true
	}

	wanted := map[string]bool{}
	for _, l := range languages {
		wanted[l] = true
	}
	if src.Original != "" && (wanted[src.OriginalLanguage] || hasLatinLetter(src.Original)) {
		add(src.Original)
	}

	buckets := make([][]string, len(languages))
	for i, l := range languages {
		buckets[i] = namesForLanguage(src, l)
	}
	return takeInTurns(out, buckets, seen, max)
}

// takeInTurns adds names from each bucket in turn (one new name per bucket per
// round) until max is reached or the buckets are empty.
func takeInTurns(out []string, buckets [][]string, seen map[string]bool, max int) []string {
	next := make([]int, len(buckets))
	for len(out) < max {
		progressed := false
		for i := range buckets {
			for next[i] < len(buckets[i]) && len(out) < max {
				name := strings.Join(strings.Fields(buckets[i][next[i]]), " ")
				next[i]++
				progressed = true
				key := TitleKey(name)
				if !SearchableKey(key) || seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, name)
				break
			}
		}
		if !progressed {
			break
		}
	}
	return out
}

// namesForLanguage lists a language's names in the order they are tried: its
// translation(s) first, then the alternative titles of its countries.
func namesForLanguage(src NameSources, language string) []string {
	countries := countriesFor(language)
	rank := func(country string) int {
		for i, c := range countries {
			if c == country {
				return i
			}
		}
		return len(countries)
	}
	var tr []Translation
	for _, t := range src.Translations {
		if t.Language == language {
			tr = append(tr, t)
		}
	}
	sort.SliceStable(tr, func(i, j int) bool { return rank(tr[i].Country) < rank(tr[j].Country) })
	var alts []CountryName
	for _, a := range src.Alternatives {
		t := strings.ToLower(a.Type)
		if rank(a.Country) < len(countries) && !strings.Contains(t, "working") && !strings.Contains(t, "fake") {
			alts = append(alts, a)
		}
	}
	sort.SliceStable(alts, func(i, j int) bool { return rank(alts[i].Country) < rank(alts[j].Country) })
	var out []string
	for _, t := range tr {
		out = append(out, t.Name)
	}
	for _, a := range alts {
		out = append(out, a.Name)
	}
	return out
}

// SearchableKey says whether a title key is long enough to search for and to
// match a release against: very short names ("Es", "M") would match far too much.
func SearchableKey(key string) bool {
	letters, ascii := 0, true
	for _, r := range key {
		if r == ' ' {
			continue
		}
		letters++
		if r > unicode.MaxASCII {
			ascii = false
		}
	}
	if ascii {
		return letters >= 3
	}
	return letters >= 2
}

func hasLatinLetter(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// accentFold writes the letters with marks the plain way ("é" as "e"), the way
// release names often do.
var accentFold = func() map[rune]string {
	m := map[rune]string{}
	for plain, marked := range map[string]string{
		"a": "àáâãäåāăą", "c": "çćč", "d": "ďđ", "e": "èéêëēėęě", "g": "ğ", "i": "ìíîïīı", "l": "łľ",
		"n": "ñńň", "o": "òóôõöøōő", "r": "ř", "s": "śšş", "t": "ťţ", "u": "ùúûüūůű", "y": "ýÿ", "z": "źžż",
	} {
		for _, r := range marked {
			m[r] = plain
		}
	}
	m['æ'], m['œ'], m['ß'], m['þ'] = "ae", "oe", "ss", "th"
	return m
}()

// umlautSpelled is how German names write the umlauts when the letters are not
// available: "Päpstin" as "Paepstin".
var umlautSpelled = map[rune]string{'ä': "ae", 'ö': "oe", 'ü': "ue"}

func titleKeyWith(s string, spelled map[rune]string) string {
	// "&amp;" is what a name copied from a web page carries for "&".
	s = strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(s), "&amp;", "&"), "&", " and ")
	var b strings.Builder
	for _, r := range s {
		if rep, ok := spelled[r]; ok {
			b.WriteString(rep)
			continue
		}
		if rep, ok := accentFold[r]; ok {
			b.WriteString(rep)
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			// Any other letter with a mark on it ("ǎ", "ṽ") loses the mark; a letter
			// of another alphabet stays as it is.
			if r > unicode.MaxASCII {
				if base := stripMarks(r); base != "" {
					b.WriteString(base)
					continue
				}
			}
			b.WriteRune(r)
		} else if unicode.Is(unicode.Mn, r) {
			// A mark on its own (the title was typed in decomposed form): drop it.
		} else {
			b.WriteByte(' ') // dots, dashes and the like count as spaces
		}
	}
	key := strings.Join(strings.Fields(b.String()), " ")
	for _, article := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(key, article) {
			return key[len(article):]
		}
	}
	return key
}

// stripMarks gives a Latin or Greek letter without its accent marks ("ǎ" as "a",
// "ά" as "α"), or "" when it has none or is a letter of another alphabet (a
// mark can change the letter there: "й" is not "и").
func stripMarks(r rune) string {
	if !unicode.Is(unicode.Latin, r) && !unicode.Is(unicode.Greek, r) {
		return ""
	}
	var b strings.Builder
	for _, d := range norm.NFD.String(string(r)) {
		if !unicode.Is(unicode.Mn, d) {
			b.WriteRune(d)
		}
	}
	if b.String() == string(r) {
		return ""
	}
	return b.String()
}

// TitleKey reduces a title to what is compared when two titles are said to be
// the same: lower case, letters and digits of any alphabet, accents dropped,
// "&" read as "and", dots and dashes read as spaces, and a leading "The", "A"
// or "An" left off.
func TitleKey(s string) string { return titleKeyWith(s, nil) }

// TitleKeys is TitleKey plus, for a title with umlauts, the spelling that writes
// them out ("ae", "oe", "ue"). Two titles are the same when any of their keys is.
func TitleKeys(s string) []string {
	k := TitleKey(s)
	if k == "" {
		return nil
	}
	if strings.ContainsAny(strings.ToLower(s), "äöü") {
		if k2 := titleKeyWith(s, umlautSpelled); k2 != k {
			return []string{k, k2}
		}
	}
	return []string{k}
}

// umlautLetters is how a name writes umlauts and the sharp s out when the
// letters themselves are not used: some release groups post "Sams im Glueck",
// some "Sams im Glück".
var umlautLetters = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss", "ẞ", "SS")

// WithUmlautSpellings puts, right after each name that has umlauts or a sharp
// s, the same name written out with "ae", "oe", "ue" and "ss", so an indexer
// that does not fold the letters is asked for both spellings. A spelling the
// list already has is not added twice.
func WithUmlautSpellings(names []string) []string {
	out := make([]string, 0, len(names)*2)
	seen := map[string]bool{}
	for _, n := range names {
		seen[strings.ToLower(n)] = true
	}
	for _, n := range names {
		out = append(out, n)
		if !strings.ContainsAny(n, "äöüÄÖÜßẞ") {
			continue
		}
		if v := umlautLetters.Replace(n); v != n && !seen[strings.ToLower(v)] {
			seen[strings.ToLower(v)] = true
			out = append(out, v)
		}
	}
	return out
}
