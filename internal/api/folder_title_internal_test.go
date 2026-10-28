package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/rdborg/mediarium/internal/library"
	"github.com/rdborg/mediarium/internal/parser"
	"github.com/rdborg/mediarium/internal/settings"
)

// New folders and file names use the title in the info language. A missing
// translation leaves the English name, and a title that already has files
// keeps the folder they are in.
func TestFolderTitleFollowsInfoLanguage(t *testing.T) {
	s := newAuditServer(t)
	var (
		mu    sync.Mutex
		calls int
	)
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
	mux.HandleFunc("/movie/603", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		title := "The Matrix"
		if r.URL.Query().Get("language") == "it-IT" {
			title = "Anna - Parole di guerra"
		}
		if r.URL.Query().Get("language") == "de-DE" {
			title = "Die Matrix"
		}
		write(w, map[string]any{"id": 603, "title": title, "release_date": "1999-03-31"})
	})
	mux.HandleFunc("/movie/700", func(w http.ResponseWriter, r *http.Request) {
		write(w, map[string]any{"id": 700, "title": "Heat", "release_date": "1995-12-15"})
	})
	mux.HandleFunc("/movie/701", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	mux.HandleFunc("/tv/900", func(w http.ResponseWriter, r *http.Request) {
		name := "Breaking Bad"
		if r.URL.Query().Get("language") == "it-IT" {
			name = "Breaking Bad - Reazione a catena"
		}
		write(w, map[string]any{"id": 900, "name": name, "first_air_date": "2008-01-20"})
	})
	tmdb := httptest.NewServer(mux)
	t.Cleanup(tmdb.Close)
	s.TestSetTMDBBaseURL("k", tmdb.URL)

	movies := filepath.Join(t.TempDir(), "movies")
	tv := filepath.Join(t.TempDir(), "tv")
	for k, v := range map[string]string{
		settings.KeyMoviesPath: movies, settings.KeyTVPath: tv,
		settings.KeyInfoLanguage: "it-IT", settings.KeyIllegalCharMode: "strip",
		settings.KeyNamingPreset:      "custom",
		settings.KeyMovieFolderFormat: "{Movie CleanTitle} ({Release Year}) [tmdb-{TmdbId}]",
	} {
		if err := s.Settings.Set(k, v, false); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.buildDestPath(movies, "Words of War", 2025, 603, "", "x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(movies, "Anna - Parole di guerra (2025) [tmdb-603]"); filepath.Dir(got) != want {
		t.Errorf("movie folder = %q, want %q", filepath.Dir(got), want)
	}
	if want := "Anna - Parole di guerra (2025).mkv"; filepath.Base(got) != want {
		t.Errorf("file name = %q, want %q", filepath.Base(got), want)
	}

	// The title page asks again every few seconds: the second lookup is cached.
	mu.Lock()
	first := calls
	mu.Unlock()
	if _, err := s.buildDestPath(movies, "Words of War", 2025, 603, "", "x.mkv"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if calls != first {
		t.Errorf("TMDB was asked %d times, want the first lookup cached (%d)", calls, first)
	}
	mu.Unlock()

	// No translation: the English title, as before.
	plain, err := s.buildDestPath(movies, "Heat", 1995, 700, "", "x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(movies, "Heat (1995) [tmdb-700]"); filepath.Dir(plain) != want {
		t.Errorf("untranslated folder = %q, want %q", filepath.Dir(plain), want)
	}

	// TMDB down: still a folder, under the English name.
	down, err := s.buildDestPath(movies, "Lost", 2020, 701, "", "x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(movies, "Lost (2020) [tmdb-701]"); filepath.Dir(down) != want {
		t.Errorf("folder when TMDB is down = %q, want %q", filepath.Dir(down), want)
	}

	// A movie that already has files keeps that folder, even with the usual
	// folder format, when the localized name would be a different folder.
	if err := s.Settings.Set(settings.KeyMovieFolderFormat, "", false); err != nil {
		t.Fatal(err)
	}
	if err := s.Settings.Set(settings.KeyInfoLanguage, "de-DE", false); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(movies, "The Matrix (1999)", "The Matrix (1999).mkv")
	m := library.Movie{Title: "The Matrix", Year: 1999, TMDBID: 603, FilePath: old}
	dest, err := s.buildDestPath(movies, m.Title, m.Year, m.TMDBID, "", "x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(movies, "Die Matrix (1999)"); filepath.Dir(dest) != want {
		t.Errorf("new folder = %q, want %q", filepath.Dir(dest), want)
	}
	if got := s.keepMovieFolder(m, movies, dest); got != filepath.Join(movies, "The Matrix (1999)", filepath.Base(dest)) {
		t.Errorf("existing movie moved to %q", got)
	}
	m.FilePath = ""
	if got := s.keepMovieFolder(m, movies, dest); got != dest {
		t.Errorf("a movie with no file should use the localized folder, got %q", got)
	}

	// A show folder and its episode file follow the same language.
	if err := s.Settings.Set(settings.KeyInfoLanguage, "it-IT", false); err != nil {
		t.Fatal(err)
	}
	sr := library.Series{Title: "Breaking Bad", Year: 2008, TMDBID: 900}
	ep, err := s.buildTVDestPath(sr, library.Episode{Season: 1, Episode: 1, Title: "Pilot"}, parser.Parse("Breaking.Bad.S01E01.1080p.BluRay-GRP"), "Breaking.Bad.S01E01.1080p.BluRay-GRP", "x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(tv, "Breaking Bad - Reazione a catena (2008)", "Season 01"); filepath.Dir(ep) != want {
		t.Errorf("show folder = %q, want %q", filepath.Dir(ep), want)
	}
	if want := "Breaking Bad - Reazione a catena - S01E01 - Pilot.mkv"; filepath.Base(ep) != want {
		t.Errorf("episode file = %q, want %q", filepath.Base(ep), want)
	}
}
