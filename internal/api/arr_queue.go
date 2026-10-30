package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/rdborg/mediarium/internal/library"
	"github.com/rdborg/mediarium/internal/music"
	"github.com/rdborg/mediarium/internal/parser"
	"github.com/rdborg/mediarium/internal/quality"
	"github.com/rdborg/mediarium/internal/queue"
	"github.com/rdborg/mediarium/internal/speed"
)

// The download queue, wanted list and calendar of the arr-style API (see
// arr.go), for all three apps.

type arrQueueRecord struct {
	ID                                  int64            `json:"id"`
	MovieID                             int64            `json:"movieId,omitempty"`
	SeriesID                            int64            `json:"seriesId,omitempty"`
	EpisodeID                           int64            `json:"episodeId,omitempty"`
	SeasonNumber                        int              `json:"seasonNumber,omitempty"`
	ArtistID                            int64            `json:"artistId,omitempty"`
	AlbumID                             int64            `json:"albumId,omitempty"`
	Title                               string           `json:"title"`
	Status                              string           `json:"status"`
	TrackedDownloadStatus               string           `json:"trackedDownloadStatus"`
	TrackedDownloadState                string           `json:"trackedDownloadState"`
	StatusMessages                      []map[string]any `json:"statusMessages"`
	ErrorMessage                        string           `json:"errorMessage,omitempty"`
	Size                                float64          `json:"size"`
	Sizeleft                            float64          `json:"sizeleft"`
	Timeleft                            string           `json:"timeleft,omitempty"`
	EstimatedCompletionTime             string           `json:"estimatedCompletionTime,omitempty"`
	Added                               string           `json:"added,omitempty"`
	Protocol                            string           `json:"protocol"`
	DownloadClient                      string           `json:"downloadClient"`
	DownloadID                          string           `json:"downloadId"`
	Indexer                             string           `json:"indexer"`
	OutputPath                          string           `json:"outputPath,omitempty"`
	Quality                             arrQualityModel  `json:"quality"`
	CustomFormats                       []any            `json:"customFormats"`
	CustomFormatScore                   int              `json:"customFormatScore"`
	DownloadClientHasPostImportCategory bool             `json:"downloadClientHasPostImportCategory"`
	Languages                           []arrLanguage    `json:"languages"`
	Movie                               *arrMovie        `json:"movie,omitempty"`
	Series                              *arrSeries       `json:"series,omitempty"`
	Episode                             *arrEpisode      `json:"episode,omitempty"`
}

// arrQueueStates maps a queue status to the arr app's status, tracked
// state and tracked status. ok is false for items that are no longer in
// the queue as the apps see it (finished, failed, stopped).
func arrQueueStates(st queue.Status) (status, state, tracked string, ok bool) {
	switch st {
	case queue.StatusQueued:
		return "queued", "downloading", "ok", true
	case queue.StatusDownloading:
		return "downloading", "downloading", "ok", true
	case queue.StatusPaused:
		return "paused", "downloading", "ok", true
	case queue.StatusImporting:
		return "completed", "importing", "ok", true
	case queue.StatusConflict:
		return "completed", "importBlocked", "warning", true
	}
	return "", "", "", false
}

// clock formats a duration as "hh:mm:ss".
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, sec)
}

// arrQueueRecords lists the app's part of the queue, newest first. A season
// pack is one record per episode in it, as Sonarr shows it.
func (s *Server) arrQueueRecords(app arrApp, r *http.Request) ([]arrQueueRecord, error) {
	items, err := s.QueueRepo.ListUnfinished()
	if err != nil {
		return nil, err
	}
	withMovie := queryBool(r, "includeMovie")
	withSeries := queryBool(r, "includeSeries")
	withEpisode := queryBool(r, "includeEpisode")
	var (
		mc arrMovieContext
		sc arrSeriesContext
	)
	if withMovie {
		mc = s.newArrMovieContext()
	}
	if withSeries {
		sc = s.newArrSeriesContext()
	}
	rate := speed.Current()
	out := []arrQueueRecord{}
	for _, it := range items {
		switch {
		case app == arrRadarr && it.MovieID == 0,
			app == arrSonarr && it.SeriesID == 0,
			app == arrLidarr && it.AlbumID == 0:
			continue
		}
		status, state, tracked, ok := arrQueueStates(it.Status)
		if !ok {
			continue
		}
		rel := parser.Parse(it.ReleaseTitle)
		tier := quality.Classify(rel)
		size := float64(it.SizeBytes)
		left := size * (1 - it.ProgressPct/100)
		if it.Status == queue.StatusImporting || it.Status == queue.StatusConflict {
			left = 0
		}
		base := arrQueueRecord{
			ID: it.ID, Title: it.ReleaseTitle, Status: status, TrackedDownloadStatus: tracked, TrackedDownloadState: state,
			StatusMessages: []map[string]any{}, ErrorMessage: it.Error, Size: size, Sizeleft: left, Added: arrTime(it.AddedAt),
			Protocol: string(it.Protocol), DownloadClient: "Mediarium", DownloadID: fmt.Sprintf("mediarium-%d", it.ID),
			Quality: arrQualityModelOf(app, tier), CustomFormats: []any{}, Languages: []arrLanguage{arrEnglish},
		}
		if base.Protocol != "usenet" && base.Protocol != "torrent" {
			base.Protocol = "unknown"
		}
		if it.Status == queue.StatusDownloading && rate > 0 && left > 0 {
			d := time.Duration(left/float64(rate)) * time.Second
			base.Timeleft = clock(d)
			base.EstimatedCompletionTime = time.Now().Add(d).UTC().Format("2006-01-02T15:04:05Z")
		} else if it.Status == queue.StatusDownloading || it.Status == queue.StatusQueued {
			base.Timeleft = "00:00:00"
		}
		if it.Status == queue.StatusConflict {
			base.StatusMessages = []map[string]any{{"title": it.ReleaseTitle, "messages": []string{"A file with that name is already there. Decide in Mediarium's Activity page."}}}
		}
		switch app {
		case arrRadarr:
			base.MovieID = it.MovieID
			if withMovie {
				if m, err := s.MovieRepo.Get(it.MovieID); err == nil {
					a := s.toArrMovie(m, mc)
					base.Movie = &a
				}
			}
			out = append(out, base)
		case arrSonarr:
			base.SeriesID, base.SeasonNumber = it.SeriesID, it.Season
			var series *arrSeries
			if withSeries {
				if sr, err := s.MovieRepo.GetSeries(it.SeriesID); err == nil {
					a := s.toArrSeries(sr, sc)
					series = &a
				}
			}
			base.Series = series
			eps := s.queueEpisodes(it)
			if len(eps) == 0 {
				out = append(out, base)
				continue
			}
			for i, ep := range eps {
				rec := base
				rec.EpisodeID = ep.ID
				if len(eps) > 1 {
					rec.ID = it.ID*1000 + int64(i+1)
					rec.Size, rec.Sizeleft = base.Size/float64(len(eps)), base.Sizeleft/float64(len(eps))
				}
				if withEpisode {
					e := toArrEpisode(ep)
					rec.Episode = &e
				}
				out = append(out, rec)
			}
		case arrLidarr:
			base.AlbumID = it.AlbumID
			if a, err := s.MusicRepo.GetAlbum(it.AlbumID); err == nil {
				base.ArtistID = a.ArtistID
			}
			out = append(out, base)
		}
	}
	return out, nil
}

// queueEpisodes is the episodes a TV queue item is for: one, or a season
// pack's whole season.
func (s *Server) queueEpisodes(it queue.Item) []library.Episode {
	if it.Episode > 0 {
		if ep, err := s.MovieRepo.GetEpisode(it.SeriesID, it.Season, it.Episode); err == nil {
			return []library.Episode{ep}
		}
		return nil
	}
	eps, err := s.MovieRepo.ListEpisodes(it.SeriesID)
	if err != nil {
		return nil
	}
	var out []library.Episode
	for _, ep := range eps {
		if ep.Season == it.Season {
			out = append(out, ep)
		}
	}
	return out
}

func (s *Server) arrQueue(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recs, err := s.arrQueueRecords(app, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		page, size := paging(r, 50)
		from, to := pageOf(len(recs), page, size)
		writeJSON(w, http.StatusOK, arrPage{Page: page, PageSize: size, SortKey: "timeleft", SortDirection: "ascending",
			TotalRecords: len(recs), Records: recs[from:to]})
	}
}

func (s *Server) arrQueueDetails(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recs, err := s.arrQueueRecords(app, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, recs)
	}
}

func (s *Server) arrQueueStatus(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recs, err := s.arrQueueRecords(app, r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		warnings := false
		for _, rec := range recs {
			if rec.TrackedDownloadStatus == "warning" {
				warnings = true
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"totalCount": len(recs), "count": len(recs), "unknownCount": 0,
			"errors": false, "warnings": warnings, "unknownErrors": false, "unknownWarnings": false})
	}
}

// arrRemoveQueueItem stops a download (when it's running) and takes it off
// the list. found is false when no such item is there.
func (s *Server) arrRemoveQueueItem(id int64) (found bool, err error) {
	item, err := s.QueueRepo.Get(id)
	if err != nil && id >= 1000 {
		item, err = s.QueueRepo.Get(id / 1000) // one episode of a season pack
	}
	if err != nil {
		return false, nil
	}
	if isActiveStatus(item.Status) || item.Status == queue.StatusPaused {
		if err := s.stopItem(item, false); err != nil {
			return true, err
		}
	}
	ref, inClient := s.clientRefFor(item)
	if err := s.QueueRepo.Delete(item.ID); err != nil {
		return true, err
	}
	s.removeFromClient(ref, inClient, false)
	s.queueItemEvent(item, "removed", queue.LevelInfo, "Removed from the download list: "+item.ReleaseTitle)
	return true, nil
}

// arrDeleteQueueItem takes one item off the list. Its files stay unless
// removeFromClient asks otherwise.
func (s *Server) arrDeleteQueueItem(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := arrPathID(w, r)
		if !ok {
			return
		}
		found, err := s.arrRemoveQueueItem(id)
		if !found {
			arrNotFound(w)
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

// arrBulkDeleteQueue answers DELETE /queue/bulk: several items at once, the
// ids in the body.
func (s *Server) arrBulkDeleteQueue(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []int64 `json:"ids"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || len(body.IDs) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "ids are needed"})
		return
	}
	for _, id := range body.IDs {
		if id <= 0 {
			continue
		}
		if _, err := s.arrRemoveQueueItem(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// arrGrabQueueItem answers POST /queue/grab/{id}: the item is already on the
// list and being handled here, so there is nothing more to start.
func (s *Server) arrGrabQueueItem(w http.ResponseWriter, r *http.Request) {
	id, ok := arrPathID(w, r)
	if !ok {
		return
	}
	if _, err := s.QueueRepo.Get(id); err != nil {
		if id < 1000 {
			arrNotFound(w)
			return
		}
		if _, err := s.QueueRepo.Get(id / 1000); err != nil {
			arrNotFound(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// --- wanted and calendar ---

// seriesEpisodes is every episode of every show in the library, with its
// show, for the wanted list and the calendar.
type showEpisode struct {
	series library.Series
	ep     library.Episode
}

func (s *Server) allShowEpisodes() ([]showEpisode, error) {
	list, err := s.MovieRepo.ListSeries()
	if err != nil {
		return nil, err
	}
	var out []showEpisode
	for _, sr := range list {
		eps, err := s.MovieRepo.ListEpisodes(sr.ID)
		if err != nil {
			return nil, err
		}
		for _, ep := range eps {
			out = append(out, showEpisode{series: sr, ep: ep})
		}
	}
	return out, nil
}

// arrWantedMissing answers GET /wanted/missing. Radarr lists the monitored
// movies that have no file and are out. A title still waiting for a release
// (no date yet, or a date ahead) can't be downloaded, so it is left off:
// nzb360 showed those as something missing. Sonarr lists the episodes that
// have aired.
func (s *Server) arrWantedMissing(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		desc := !strings.EqualFold(r.URL.Query().Get("sortDirection"), "ascending")
		var records []any
		switch app {
		case arrRadarr:
			movies, err := s.MovieRepo.List()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			c := s.newArrMovieContext()
			monitoredOnly := true // monitored=false lists the unmonitored ones too
			if v, ok := arrBoolParam(r, "monitored"); ok {
				monitoredOnly = v
			}
			var list []arrMovie
			for _, m := range movies {
				a := s.toArrMovie(m, c)
				if (a.Monitored || !monitoredOnly) && !a.HasFile && a.IsAvailable {
					list = append(list, a)
				}
			}
			sortArrMovies(list, r.URL.Query().Get("sortKey"), r.URL.Query().Get("sortDirection"))
			for _, a := range list {
				records = append(records, a)
			}
		case arrSonarr:
			all, err := s.allShowEpisodes()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			c := s.newArrSeriesContext()
			series := map[int64]*arrSeries{}
			var list []arrEpisode
			for _, se := range all {
				if !se.series.Monitored || !se.ep.Monitored || se.ep.Status == library.StatusDownloaded || se.ep.AirDate == "" || se.ep.AirDate > today() || se.ep.AirsLater(time.Now()) {
					continue
				}
				if series[se.series.ID] == nil {
					a := s.toArrSeries(se.series, c)
					series[se.series.ID] = &a
				}
				e := toArrEpisode(se.ep)
				e.Series = series[se.series.ID]
				list = append(list, e)
			}
			sort.Slice(list, func(i, j int) bool {
				if desc {
					return list[i].AirDate > list[j].AirDate
				}
				return list[i].AirDate < list[j].AirDate
			})
			for _, e := range list {
				records = append(records, e)
			}
		case arrLidarr:
			albums, artists, err := s.arrMusic()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, a := range albums {
				ar, ok := artists[a.ArtistID]
				if !ok || !ar.Monitored || !a.Monitored || a.Status == music.StatusDownloaded || a.ReleaseDate == "" || a.ReleaseDate > today() {
					continue
				}
				records = append(records, s.toArrAlbum(a, &ar))
			}
		}
		logArrAsked(app, r, len(records))
		arrWantedPage(w, r, records, map[arrApp]string{arrRadarr: "movieMetadata.sortTitle", arrSonarr: "episodes.airDateUtc", arrLidarr: "albums.releaseDate"}[app])
	}
}

func (s *Server) arrCalendar(app arrApp) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start, end := arrRange(r)
		unmonitored := queryBool(r, "unmonitored")
		in := func(date string) bool {
			return date != "" && date[:min(10, len(date))] >= start && date[:min(10, len(date))] <= end
		}
		switch app {
		case arrRadarr:
			movies, err := s.MovieRepo.List()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			c := s.newArrMovieContext()
			out := []arrMovie{}
			for _, m := range movies {
				if (m.Monitored || unmonitored) && in(m.ReleaseDate) {
					out = append(out, s.toArrMovie(m, c))
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i].InCinemas < out[j].InCinemas })
			logArrAsked(app, r, len(out))
			writeJSON(w, http.StatusOK, out)
		case arrSonarr:
			all, err := s.allShowEpisodes()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			c := s.newArrSeriesContext()
			series := map[int64]*arrSeries{}
			out := []arrEpisode{}
			for _, se := range all {
				if !in(se.ep.AirDate) || (!unmonitored && (!se.ep.Monitored || !se.series.Monitored)) {
					continue
				}
				if series[se.series.ID] == nil {
					a := s.toArrSeries(se.series, c)
					series[se.series.ID] = &a
				}
				e := toArrEpisode(se.ep)
				e.Series = series[se.series.ID]
				if queryBool(r, "includeEpisodeFile") || e.HasFile {
					e.EpisodeFile = s.toArrEpisodeFile(se.ep)
				}
				out = append(out, e)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].AirDate < out[j].AirDate })
			logArrAsked(app, r, len(out))
			writeJSON(w, http.StatusOK, out)
		case arrLidarr:
			albums, artists, err := s.arrMusic()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			out := []arrAlbum{}
			for _, a := range albums {
				ar, ok := artists[a.ArtistID]
				if !ok || !in(a.ReleaseDate) || len(a.ReleaseDate) < 10 || (!unmonitored && !a.Monitored) {
					continue
				}
				out = append(out, s.toArrAlbum(a, &ar))
			}
			writeJSON(w, http.StatusOK, out)
		}
	}
}
