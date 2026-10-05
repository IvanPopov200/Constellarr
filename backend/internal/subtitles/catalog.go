package subtitles

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/library"
	"github.com/IvanPopov200/Constellarr/backend/internal/movies"
	"github.com/IvanPopov200/Constellarr/backend/internal/tv"
)

// Catalog resolves catalog-owned videos through the movie and TV services.
type Catalog interface {
	Videos(ctx context.Context) ([]Video, error)
	Video(ctx context.Context, kind, id string) (Video, error)
}

// NewCatalog adapts the movie and TV services; either one may be nil.
func NewCatalog(movieService *movies.Service, tvService *tv.Service) Catalog {
	return &catalog{movies: movieService, tv: tvService}
}

type catalog struct {
	movies *movies.Service
	tv     *tv.Service
}

func (c *catalog) Videos(ctx context.Context) ([]Video, error) {
	videos := []Video{}
	if c.movies != nil {
		movieList, err := c.movies.List(ctx)
		if err != nil {
			return nil, err
		}
		cfg, err := c.movies.ConfigView(ctx)
		if err != nil {
			return nil, err
		}
		roots := map[string]string{}
		for _, root := range cfg.RootFolders {
			roots[root.ID] = root.Path
		}
		for _, movie := range movieList {
			video := Video{
				Kind:   KindMovie,
				ID:     movie.ID,
				Title:  movie.Metadata.Title,
				Year:   movie.Metadata.Year,
				IMDbID: strings.ToLower(movie.Metadata.IMDbID),
			}
			files := make([]movies.File, 0, len(movie.Files))
			for _, file := range movie.Files {
				if !file.Missing && file.Path != "" {
					files = append(files, file)
				}
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })
			if len(files) == 0 {
				continue
			}
			best := files[0]
			video.RootID, video.Path, video.Size = best.RootID, best.Path, best.Size
			video.rootPath = roots[best.RootID]
			if video.rootPath == "" {
				continue
			}
			videos = append(videos, video)
		}
	}
	if c.tv != nil {
		episodes, err := c.tv.Store.Episodes(ctx, "")
		if err != nil {
			return nil, err
		}
		series, err := c.tv.Store.List(ctx)
		if err != nil {
			return nil, err
		}
		cfg, err := c.tv.Config(ctx)
		if err != nil {
			return nil, err
		}
		roots := map[string]string{}
		for _, root := range cfg.RootFolders {
			roots[root.ID] = root.Path
		}
		byID := map[string]tv.Series{}
		for _, item := range series {
			byID[item.ID] = item
		}
		for _, episode := range episodes {
			parent := byID[episode.SeriesID]
			files := make([]movies.File, 0, len(episode.Files))
			for _, file := range episode.Files {
				if file.Path != "" {
					files = append(files, file)
				}
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })
			if len(files) == 0 {
				continue
			}
			best := files[0]
			rootPath := roots[best.RootID]
			if rootPath == "" {
				continue
			}
			imdb := parent.Metadata.IMDbID
			if imdb == "" {
				imdb = episode.IMDbID
			}
			videos = append(videos, Video{
				Kind:         KindEpisode,
				ID:           episode.ID,
				Title:        episodeLabel(episode),
				SeriesTitle:  parent.Metadata.Title,
				Year:         parent.Metadata.Year,
				IMDbID:       strings.ToLower(episode.IMDbID),
				SeriesIMDbID: strings.ToLower(imdb),
				Season:       episode.Season,
				Episode:      episode.Number,
				RootID:       best.RootID,
				Path:         best.Path,
				Size:         best.Size,
				rootPath:     rootPath,
			})
		}
	}
	sort.Slice(videos, func(i, j int) bool {
		if videos[i].Kind != videos[j].Kind {
			return videos[i].Kind < videos[j].Kind
		}
		if videos[i].SeriesTitle != videos[j].SeriesTitle {
			return videos[i].SeriesTitle < videos[j].SeriesTitle
		}
		if videos[i].Season != videos[j].Season {
			return videos[i].Season < videos[j].Season
		}
		if videos[i].Episode != videos[j].Episode {
			return videos[i].Episode < videos[j].Episode
		}
		return videos[i].Title < videos[j].Title
	})
	return videos, nil
}

func (c *catalog) Video(ctx context.Context, kind, id string) (Video, error) {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 {
		return Video{}, fmt.Errorf("%w: video id is invalid", ErrInvalid)
	}
	switch kind {
	case KindMovie:
		if c.movies == nil {
			return Video{}, ErrNotFound
		}
		movie, err := c.movies.Get(ctx, id)
		if err != nil {
			return Video{}, ErrNotFound
		}
		cfg, err := c.movies.ConfigView(ctx)
		if err != nil {
			return Video{}, err
		}
		roots := map[string]string{}
		for _, root := range cfg.RootFolders {
			roots[root.ID] = root.Path
		}
		files := make([]movies.File, 0, len(movie.Files))
		for _, file := range movie.Files {
			if !file.Missing && file.Path != "" {
				files = append(files, file)
			}
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })
		for _, file := range files {
			rootPath := roots[file.RootID]
			if rootPath == "" {
				continue
			}
			if handle, err := library.Open(rootPath, file.Path); err == nil {
				info, statErr := handle.Stat()
				handle.Close()
				if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
					return Video{
						Kind: KindMovie, ID: movie.ID, Title: movie.Metadata.Title,
						Year: movie.Metadata.Year, IMDbID: strings.ToLower(movie.Metadata.IMDbID),
						RootID: file.RootID, Path: file.Path, Size: info.Size(), rootPath: rootPath,
					}, nil
				}
			}
		}
		return Video{}, ErrNotFound
	case KindEpisode:
		if c.tv == nil {
			return Video{}, ErrNotFound
		}
		episodes, err := c.tv.Store.Episodes(ctx, "")
		if err != nil {
			return Video{}, err
		}
		cfg, err := c.tv.Config(ctx)
		if err != nil {
			return Video{}, err
		}
		roots := map[string]string{}
		for _, root := range cfg.RootFolders {
			roots[root.ID] = root.Path
		}
		for _, episode := range episodes {
			if episode.ID != id {
				continue
			}
			parent, err := c.tv.Store.Get(ctx, episode.SeriesID)
			if err != nil {
				parent = tv.Series{ID: episode.SeriesID}
			}
			files := make([]movies.File, 0, len(episode.Files))
			for _, file := range episode.Files {
				if file.Path != "" {
					files = append(files, file)
				}
			}
			sort.Slice(files, func(i, j int) bool { return files[i].Size > files[j].Size })
			for _, file := range files {
				rootPath := roots[file.RootID]
				if rootPath == "" {
					continue
				}
				if handle, err := library.Open(rootPath, file.Path); err == nil {
					info, statErr := handle.Stat()
					handle.Close()
					if statErr == nil && info.Mode().IsRegular() && info.Size() > 0 {
						imdb := parent.Metadata.IMDbID
						if imdb == "" {
							imdb = episode.IMDbID
						}
						return Video{
							Kind: KindEpisode, ID: episode.ID, Title: episodeLabel(episode),
							SeriesTitle: parent.Metadata.Title, Year: parent.Metadata.Year,
							IMDbID: strings.ToLower(episode.IMDbID), SeriesIMDbID: strings.ToLower(imdb),
							Season: episode.Season, Episode: episode.Number,
							RootID: file.RootID, Path: file.Path, Size: info.Size(), rootPath: rootPath,
						}, nil
					}
				}
			}
			return Video{}, ErrNotFound
		}
		return Video{}, ErrNotFound
	default:
		return Video{}, fmt.Errorf("%w: video kind must be movie or episode", ErrInvalid)
	}
}

// episodeLabel names an episode without repeating the series, which the UI shows separately.
func episodeLabel(episode tv.Episode) string {
	label := "S" + pad2(episode.Season) + "E" + pad2(episode.Number)
	if episode.Title != "" {
		label += " · " + episode.Title
	}
	return label
}

func pad2(value int) string {
	if value < 0 {
		value = 0
	}
	if value < 10 {
		return "0" + strconv.Itoa(value)
	}
	return strconv.Itoa(value)
}

// verifyVideoFile ensures the video is still a regular file below its library root.
func verifyVideoFile(video Video) error {
	if video.rootPath == "" {
		return fmt.Errorf("%w: video is not below a configured library root", ErrUnsafe)
	}
	handle, err := library.Open(video.rootPath, video.Path)
	if err != nil {
		return fmt.Errorf("%w: video file is not available", ErrNotFound)
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: video file is not available", ErrNotFound)
	}
	return nil
}
