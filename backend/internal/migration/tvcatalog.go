package migration

import (
	"path/filepath"
	"strconv"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

// tvAddInput adapts a planned series to the TV service's validation.
func tvAddInput(series SeriesPlan, profileID, rootID string) tv.AddInput {
	return tv.AddInput{
		IMDbID: series.IMDbID, Monitored: series.Monitored, MonitorMode: series.MonitorMode,
		ProfileID: profileID, RootID: rootID, Tags: series.Tags,
		Metadata: metadata.Title{
			IMDbID: series.IMDbID, Title: series.Title, Year: series.Year, Type: "series",
			Poster: series.Poster, Plot: series.Plot,
		},
	}
}

func tvMonitorInput(season int, monitored bool) tv.MonitorInput {
	return tv.MonitorInput{Season: &season, Monitored: monitored}
}

func tvImportInput(rootID, relative, seriesID string, file EpisodeFilePlan) tv.ImportInput {
	return tv.ImportInput{
		RootID: rootID, Path: relative, SeriesID: seriesID,
		Season: file.Season, Episodes: file.Numbers,
	}
}

func episodesHaveFile(episodes []tv.Episode, relative string) bool {
	target := filepath.Base(filepath.ToSlash(relative))
	for _, episode := range episodes {
		for _, file := range episode.Files {
			if !file.Missing && filepath.Base(filepath.ToSlash(file.Path)) == target {
				return true
			}
		}
	}
	return false
}

func movieLabel(title string, year int) string {
	if year <= 0 {
		return title
	}
	return title + " (" + strconv.Itoa(year) + ")"
}
