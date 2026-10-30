package api

import (
	"os"
	"path/filepath"

	"github.com/rdborg/mediarium/internal/library"
	"github.com/rdborg/mediarium/internal/metafiles"
	"github.com/rdborg/mediarium/internal/organizer"
)

// movieFolderFor is the folder a movie's files are in, or go to when it has
// none yet: the folder its file is in; else the folder an import would create
// (such as /movies/The Matrix (1999)), or the one folder in the library named
// like the movie when that is where a person put the file. "" when there is no
// library folder set to work it out from.
func (s *Server) movieFolderFor(m library.Movie) string {
	if m.FilePath != "" {
		return filepath.Dir(m.FilePath)
	}
	root := s.movieRootFor(m)
	if root == "" {
		return ""
	}
	dest, err := s.buildDestPath(root, m.Title, m.Year, m.TMDBID, "", "x.mkv")
	if err != nil {
		return ""
	}
	folder := filepath.Dir(dest)
	if info, err := os.Stat(folder); err != nil || !info.IsDir() {
		if alike := similarMovieFolder(root, m); alike != "" {
			folder = alike
		}
	}
	return folder
}

// seriesFolderFor is movieFolderFor for a show: the folder above the season
// folders its episode files are in, else the folder an import would create
// (such as /tv/Show Name (2008)). files are the paths of the downloaded episodes.
func (s *Server) seriesFolderFor(sr library.Series, files []string) string {
	root := s.seriesRootFor(sr)
	if len(files) > 0 {
		if dir, ok := metafiles.ShowDir(s.fileRoot(root, files[0]), files); ok {
			return dir
		}
		// A flat dump of episodes with no show folder of its own: where they lie.
		return commonDir(files)
	}
	if root == "" {
		return ""
	}
	mode, replacement := s.illegalCharSettings()
	name := organizer.Sanitize(organizer.Render(s.seriesFolderFormat(), organizer.NamingContext{SeriesTitle: s.folderTitle(sr.TMDBID, "tv", sr.Title), Year: sr.Year, TMDBID: sr.TMDBID}), mode, replacement)
	return filepath.Join(root, name)
}
