package metadata_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rdborg/mediarium/internal/metadata"
)

func TestTitleNameSourcesMovie(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/movie/120" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("append_to_response"); got != "alternative_titles,translations" {
			t.Fatalf("append_to_response = %q", got)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 120, "original_title": "The Lord of the Rings: The Fellowship of the Ring", "original_language": "en",
			"alternative_titles": map[string]any{"titles": []map[string]any{
				{"iso_3166_1": "DE", "title": "Der Herr der Ringe 1", "type": ""},
				{"iso_3166_1": "", "title": "  "},
			}},
			"translations": map[string]any{"translations": []map[string]any{
				{"iso_3166_1": "DE", "iso_639_1": "de", "data": map[string]any{"title": "Der Herr der Ringe: Die Gefährten"}},
				{"iso_3166_1": "IT", "iso_639_1": "it", "data": map[string]any{"title": ""}},
			}},
		})
	}))
	defer srv.Close()

	c := metadata.NewWithBaseURL("k", srv.URL)
	src, err := c.TitleNameSources(context.Background(), "movie", 120)
	if err != nil {
		t.Fatal(err)
	}
	if src.OriginalLanguage != "en" || len(src.Alternatives) != 1 || src.Alternatives[0].Country != "DE" {
		t.Fatalf("unexpected alternatives: %+v", src)
	}
	if len(src.Translations) != 1 || src.Translations[0].Name != "Der Herr der Ringe: Die Gefährten" || src.Translations[0].Language != "de" {
		t.Fatalf("unexpected translations: %+v", src.Translations)
	}
	if _, err := c.TitleNameSources(context.Background(), "movie", 120); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Errorf("the second ask went to TMDB again (%d requests)", calls.Load())
	}
}

func TestTitleNameSourcesShow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tv/1396" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": 1396, "original_name": "Breaking Bad", "original_language": "en",
			"alternative_titles": map[string]any{"results": []map[string]any{{"iso_3166_1": "FR", "title": "Breaking Bad : Le Chimiste"}}},
			"translations":       map[string]any{"translations": []map[string]any{{"iso_3166_1": "ES", "iso_639_1": "es", "data": map[string]any{"name": "Breaking Bad: Volviéndose Malo"}}}},
		})
	}))
	defer srv.Close()
	src, err := metadata.NewWithBaseURL("k", srv.URL).TitleNameSources(context.Background(), "tv", 1396)
	if err != nil {
		t.Fatal(err)
	}
	if src.Original != "Breaking Bad" || len(src.Alternatives) != 1 || len(src.Translations) != 1 || src.Translations[0].Name != "Breaking Bad: Volviéndose Malo" {
		t.Fatalf("unexpected: %+v", src)
	}
}

func TestTitleNameSourcesErrors(t *testing.T) {
	c := metadata.NewWithBaseURL("", "http://127.0.0.1:1")
	if _, err := c.TitleNameSources(context.Background(), "movie", 1); err != metadata.ErrNoAPIKey {
		t.Fatalf("want ErrNoAPIKey, got %v", err)
	}
	if _, err := metadata.NewWithBaseURL("k", "http://127.0.0.1:1").TitleNameSources(context.Background(), "book", 1); err == nil {
		t.Fatal("a kind that is not movie or tv should be refused")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	defer srv.Close()
	_, err := metadata.NewWithBaseURL("k", srv.URL).TitleNameSources(context.Background(), "movie", 9)
	var se *metadata.StatusError
	if !errorsAs(err, &se) || se.Code != 404 {
		t.Fatalf("want a 404 StatusError, got %v", err)
	}
}

func errorsAs(err error, target **metadata.StatusError) bool {
	se, ok := err.(*metadata.StatusError)
	if ok {
		*target = se
	}
	return ok
}

func TestExtraTitles(t *testing.T) {
	lotr := metadata.NameSources{
		Original: "The Lord of the Rings: The Fellowship of the Ring", OriginalLanguage: "en",
		Alternatives: []metadata.CountryName{
			{Country: "US", Name: "Fellowship of the Ring"},
			{Country: "DE", Name: "Der Herr der Ringe"},
			{Country: "AT", Name: "Herr der Ringe - Die Gefährten"},
			{Country: "DE", Name: "LOTR Arbeitstitel", Type: "working title"},
			{Country: "IT", Name: "Il Signore degli Anelli - La Compagnia dell'Anello"},
		},
		Translations: []metadata.Translation{
			{Language: "de", Country: "DE", Name: "Der Herr der Ringe: Die Gefährten"},
			{Language: "it", Country: "IT", Name: "Il Signore degli Anelli - La Compagnia dell'Anello"},
			{Language: "fr", Country: "FR", Name: "Le Seigneur des anneaux : La Communauté de l'anneau"},
		},
	}
	english := "The Lord of the Rings: The Fellowship of the Ring"
	tests := []struct {
		name      string
		src       metadata.NameSources
		english   string
		languages []string
		max       int
		want      []string
	}{
		{"no language wanted gives nothing", lotr, english, nil, 4, nil},
		{"german: translation first, then the alternative titles of German-speaking countries", lotr, english, []string{"de"}, 4,
			[]string{"Der Herr der Ringe: Die Gefährten", "Der Herr der Ringe", "Herr der Ringe - Die Gefährten"}},
		{"a working title is not kept", lotr, english, []string{"de"}, 10,
			[]string{"Der Herr der Ringe: Die Gefährten", "Der Herr der Ringe", "Herr der Ringe - Die Gefährten"}},
		{"two languages take turns", lotr, english, []string{"de", "it", "fr"}, 3,
			[]string{"Der Herr der Ringe: Die Gefährten", "Il Signore degli Anelli - La Compagnia dell'Anello", "Le Seigneur des anneaux : La Communauté de l'anneau"}},
		{"the cap is kept", lotr, english, []string{"de", "it", "fr"}, 2,
			[]string{"Der Herr der Ringe: Die Gefährten", "Il Signore degli Anelli - La Compagnia dell'Anello"}},
		{"a country that is not wanted gives nothing", lotr, english, []string{"pl"}, 4, nil},
		{"the English title is skipped whatever its punctuation", metadata.NameSources{
			Translations: []metadata.Translation{{Language: "de", Country: "DE", Name: "Matrix"}, {Language: "de", Country: "AT", Name: "The Matrix!"}},
		}, "The Matrix", []string{"de"}, 4, []string{"Matrix"}[:0]},
		{"the original title comes first when its language is wanted", metadata.NameSources{
			Original: "Das Boot", OriginalLanguage: "de",
			Translations: []metadata.Translation{{Language: "de", Country: "DE", Name: "Das Boot - Director's Cut"}},
		}, "Das Boot (English)", []string{"de"}, 4, []string{"Das Boot", "Das Boot - Director's Cut"}},
		{"an original title in another alphabet is not kept", metadata.NameSources{
			Original: "千と千尋の神隠し", OriginalLanguage: "ja",
			Translations: []metadata.Translation{{Language: "de", Country: "DE", Name: "Chihiros Reise ins Zauberland"}},
		}, "Spirited Away", []string{"de"}, 4, []string{"Chihiros Reise ins Zauberland"}},
		{"an original title in Latin letters is kept", metadata.NameSources{
			Original: "Shingeki no Kyojin", OriginalLanguage: "ja",
		}, "Attack on Titan", []string{"de"}, 4, []string{"Shingeki no Kyojin"}},
		{"too short names are left out", metadata.NameSources{
			Translations: []metadata.Translation{{Language: "de", Country: "DE", Name: "Es"}, {Language: "de", Country: "DE", Name: "Es Kapitel"}},
		}, "It", []string{"de"}, 4, []string{"Es Kapitel"}},
		{"a language with its own alphabet keeps it", metadata.NameSources{
			Translations: []metadata.Translation{{Language: "ru", Country: "RU", Name: "Матрица"}},
		}, "The Matrix", []string{"ru"}, 4, []string{"Матрица"}},
		{"repeats in different spellings are one", metadata.NameSources{
			Translations: []metadata.Translation{{Language: "de", Country: "DE", Name: "Die Päpstin"}, {Language: "de", Country: "AT", Name: "Die Papstin"}},
		}, "Pope Joan", []string{"de"}, 4, []string{"Die Päpstin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := metadata.ExtraTitles(tt.src, tt.english, tt.languages, tt.max)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ExtraTitles = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTitleKeys(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"The Matrix", []string{"matrix"}},
		{"Fast & Furious", []string{"fast and furious"}},
		{"Der.Herr.der.Ringe", []string{"der herr der ringe"}},
		{"Amélie", []string{"amelie"}},
		{"Die Päpstin", []string{"die papstin", "die paepstin"}},
		{"Straße", []string{"strasse"}},
		{"Матрица", []string{"матрица"}},
		{"...", nil},
	}
	for _, tt := range tests {
		if got := metadata.TitleKeys(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("TitleKeys(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTitleKeyDropsAnyAccentAndKeepsOtherLetters(t *testing.T) {
	for in, want := range map[string]string{
		"Ǎrdal":       "ardal",
		"Cafe\u0301":  "cafe", // typed as e plus a combining mark
		"Ελλάδα":      "ελλαδα",
		"Война и мир": "война и мир",
		"千と千尋の神隠し":    "千と千尋の神隠し",
		"!!!":         "",
	} {
		if got := metadata.TitleKey(in); got != want {
			t.Errorf("TitleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitleKeyReadsAmpersandsAndTheirEntity(t *testing.T) {
	want := "tom and jerry"
	for _, in := range []string{
		"Tom & Jerry", "Tom &amp; Jerry", "Tom &AMP; Jerry", "Tom and Jerry",
		"Tom&Jerry", "Tom.&.Jerry", "Tom.&amp;.Jerry", "tom_and_jerry",
	} {
		if got := metadata.TitleKey(in); got != want {
			t.Errorf("TitleKey(%q) = %q, want %q", in, got, want)
		}
	}
	// A name with an ampersand inside a word keeps both sides.
	for in, want := range map[string]string{
		"R&B Divas":       "r and b divas",
		"AT&amp;T Story":  "at and t story",
		"Law &amp; Order": "law and order",
	} {
		if got := metadata.TitleKey(in); got != want {
			t.Errorf("TitleKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWithUmlautSpellings(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, []string{}},
		{[]string{"Heat"}, []string{"Heat"}},
		{[]string{"Sams im Glück"}, []string{"Sams im Glück", "Sams im Glueck"}},
		{[]string{"Die Päpstin", "Fußball ÖSTERREICH"}, []string{"Die Päpstin", "Die Paepstin", "Fußball ÖSTERREICH", "Fussball OeSTERREICH"}},
		{[]string{"Glück", "Glueck"}, []string{"Glück", "Glueck"}},
	}
	for _, c := range cases {
		got := metadata.WithUmlautSpellings(c.in)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("WithUmlautSpellings(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
