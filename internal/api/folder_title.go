package api

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rdborg/mediarium/internal/metadata"
	"github.com/rdborg/mediarium/internal/settings"
)

// How long a folder title from TMDB is remembered, and how long a failed
// lookup is remembered. A title page asks for its folder every few seconds,
// so a TMDB outage must not turn into a request on every refresh.
const (
	folderTitleRemember = 6 * time.Hour
	folderTitleRetry    = 2 * time.Minute
)

// folderTitleCache remembers the title TMDB gave for one movie or show in
// one language. The key is "movie:603:de-DE".
type folderTitleCache struct {
	mu sync.Mutex
	m  map[string]folderTitleHit
}

type folderTitleHit struct {
	title string
	until time.Time
}

func (c *folderTitleCache) get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	hit, ok := c.m[key]
	if !ok || time.Now().After(hit.until) {
		return "", false
	}
	return hit.title, true
}

func (c *folderTitleCache) set(key, title string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[string]folderTitleHit)
	}
	c.m[key] = folderTitleHit{title: title, until: until}
}

// foldersFollowInfoLanguage reports whether new folders are named in the
// language chosen for movie and show info. English keeps the names folders
// always had.
func (s *Server) foldersFollowInfoLanguage() bool {
	return s.settingOn(settings.KeyFoldersInInfoLanguage, false) && s.infoLanguage() != metadata.DefaultLanguage
}

// folderNameWouldChange reports whether a new folder for this title would
// be named differently from the English title the library stores. A title
// that already has files then keeps its folder, so picking another language
// never moves those files.
func (s *Server) folderNameWouldChange(tmdbID int, kind, english string) bool {
	if !s.foldersFollowInfoLanguage() {
		return false
	}
	return s.folderTitle(tmdbID, kind, english) != strings.TrimSpace(english)
}

// folderTitle is the title a new movie or show folder and file are named
// with. It is the title in the language chosen for movie and show info, when
// that language is not English and TMDB has a translation. The library keeps
// the English title, and indexers are still searched for it. A lookup that
// fails, or a title TMDB has no translation of, leaves the English name, so
// a folder and a file can still be made. kind is "movie" or "tv".
func (s *Server) folderTitle(tmdbID int, kind, english string) string {
	english = strings.TrimSpace(english)
	if tmdbID <= 0 || !s.foldersFollowInfoLanguage() {
		return english
	}
	key := kind + ":" + strconv.Itoa(tmdbID) + ":" + s.infoLanguage()
	if title, ok := s.folderTitles.get(key); ok {
		return title
	}
	title, ok := s.lookupFolderTitle(tmdbID, kind)
	if !ok || strings.TrimSpace(title) == "" {
		s.folderTitles.set(key, english, time.Now().Add(folderTitleRetry))
		return english
	}
	title = strings.TrimSpace(title)
	s.folderTitles.set(key, title, time.Now().Add(folderTitleRemember))
	return title
}

// lookupFolderTitle asks TMDB for the title in the info language. ok is
// false when there is no key or TMDB does not answer.
func (s *Server) lookupFolderTitle(tmdbID int, kind string) (string, bool) {
	client := s.TMDB()
	if client == nil || !client.HasAPIKey() {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	local := client.Localized(s.infoLanguage())
	switch kind {
	case "tv":
		show, err := local.GetShow(ctx, tmdbID)
		if err != nil || show == nil {
			return "", false
		}
		return show.Name, true
	default:
		movie, err := local.GetMovie(ctx, tmdbID)
		if err != nil || movie == nil {
			return "", false
		}
		return movie.Title, true
	}
}
