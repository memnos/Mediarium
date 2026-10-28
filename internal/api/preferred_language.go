package api

import (
	"github.com/rdborg/mediarium/internal/metadata"
	"github.com/rdborg/mediarium/internal/quality"
	"github.com/rdborg/mediarium/internal/settings"
)

// preferredLanguage is the audio language wanted (Settings > Library >
// Quality). English until it is changed.
func (s *Server) preferredLanguage() string {
	if v, _ := s.Settings.Get(settings.KeyQualityLanguage); v != "" {
		return v
	}
	return quality.DefaultLanguage
}

// languageOnly is the switch "Only take releases in these languages" (off
// until it is turned on): a release has to say it is in a wanted language.
func (s *Server) languageOnly() bool {
	return s.settingOn(settings.KeyQualityLanguageOnly, false)
}

// infoLanguage is the language titles, plots, genres and posters are read in
// (Settings > Info, lists and subtitles > Movie and show info). English until
// it is changed.
func (s *Server) infoLanguage() string {
	if v, _ := s.Settings.Get(settings.KeyInfoLanguage); metadata.IsInfoLanguage(v) {
		return v
	}
	return metadata.DefaultLanguage
}

// displayTMDB is the TMDB client for what is read on screen: Discover, search
// results and title pages, in the chosen language. New movie and show folders
// use that title too (folder_title.go). File names, and the titles searched
// for on indexers, use TMDB() instead, which always asks in English, so
// choosing another language never renames a file or re-searches anything.
func (s *Server) displayTMDB() *metadata.Client {
	return s.TMDB().Localized(s.infoLanguage())
}
