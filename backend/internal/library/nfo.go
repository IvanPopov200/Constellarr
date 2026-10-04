package library

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/metadata"
)

type nfoActor struct {
	Name  string `xml:"name"`
	Order int    `xml:"order"`
}

type nfoUniqueID struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type nfoTitle struct {
	Title     string        `xml:"title,omitempty"`
	Year      int           `xml:"year,omitempty"`
	Plot      string        `xml:"plot,omitempty"`
	Rating    float64       `xml:"rating,omitempty"`
	IMDbID    string        `xml:"imdbid,omitempty"`
	Directors []string      `xml:"director,omitempty"`
	Genres    []string      `xml:"genre,omitempty"`
	Actors    []nfoActor    `xml:"actor,omitempty"`
	UniqueIDs []nfoUniqueID `xml:"uniqueid,omitempty"`
}

type nfoMovie struct {
	XMLName xml.Name `xml:"movie"`
	nfoTitle
}

type nfoSeries struct {
	XMLName xml.Name `xml:"tvshow"`
	nfoTitle
}

type nfoEpisode struct {
	XMLName   xml.Name      `xml:"episodedetails"`
	Title     string        `xml:"title,omitempty"`
	Season    int           `xml:"season"`
	Episode   int           `xml:"episode"`
	Aired     string        `xml:"aired,omitempty"`
	IMDbID    string        `xml:"imdbid,omitempty"`
	UniqueIDs []nfoUniqueID `xml:"uniqueid,omitempty"`
}

type nfoSidecar struct {
	rel  string
	data []byte
}

func nfoBytes(title metadata.Title) ([]byte, error) {
	body, err := xml.MarshalIndent(nfoMovie{nfoTitle: nfoTitleFields(title)}, "", "  ")
	if err != nil {
		return nil, err
	}
	return withHeader(body), nil
}

func seriesNFOBytes(title metadata.Title) ([]byte, error) {
	body, err := xml.MarshalIndent(nfoSeries{nfoTitle: nfoTitleFields(title)}, "", "  ")
	if err != nil {
		return nil, err
	}
	return withHeader(body), nil
}

func episodeNFOBytes(episode *Episode) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString(xml.Header)
	for _, number := range episode.Numbers {
		detail, matched := episodeDetail(episode, number)
		block := nfoEpisode{Season: episode.Season, Episode: number}
		switch {
		case matched:
			block.Title, block.Aired, block.IMDbID = detail.Title, detail.AirDate, detail.IMDbID
		case len(episode.Numbers) == 1:
			block.Title, block.Aired, block.IMDbID = episode.Title, episode.AirDate, episode.IMDbID
		default:
			// A multi-episode fallback may repeat a shared title but never one episode's ID or date.
			block.Title = episode.Title
		}
		block.UniqueIDs = imdbUniqueIDs(block.IMDbID)
		body, err := xml.MarshalIndent(block, "", "  ")
		if err != nil {
			return nil, err
		}
		buffer.Write(body)
		buffer.WriteByte('\n')
	}
	return buffer.Bytes(), nil
}

func episodeDetail(episode *Episode, number int) (metadata.Episode, bool) {
	for _, detail := range episode.Details {
		if detail.Season == episode.Season && detail.Number == number {
			return detail, true
		}
	}
	return metadata.Episode{}, false
}

func nfoTitleFields(title metadata.Title) nfoTitle {
	fields := nfoTitle{
		Title:     title.Title,
		Year:      title.Year,
		Plot:      title.Plot,
		IMDbID:    title.IMDbID,
		Directors: title.Directors,
		Genres:    title.Genres,
	}
	if title.Rating != nil {
		fields.Rating = *title.Rating
	}
	for i, name := range title.Cast {
		fields.Actors = append(fields.Actors, nfoActor{Name: name, Order: i})
	}
	fields.UniqueIDs = imdbUniqueIDs(title.IMDbID)
	return fields
}

func imdbUniqueIDs(imdbID string) []nfoUniqueID {
	if imdbID == "" {
		return nil
	}
	return []nfoUniqueID{{Type: "imdb", Value: imdbID}}
}

func withHeader(body []byte) []byte {
	return append([]byte(xml.Header), append(body, '\n')...)
}

func buildSidecars(opts Options, folderTemplate, folderRel string, targets []*target) ([]nfoSidecar, error) {
	if !opts.WriteNFO {
		return nil, nil
	}
	if opts.Episode == nil {
		data, err := nfoBytes(opts.Metadata)
		if err != nil {
			return nil, err
		}
		return []nfoSidecar{{rel: path.Join(folderRel, nfoName), data: data}}, nil
	}
	seriesRel, err := seriesFolderRel(folderTemplate, folderRel, opts, targets[0].srcRel)
	if err != nil {
		return nil, err
	}
	seriesData, err := seriesNFOBytes(opts.Metadata)
	if err != nil {
		return nil, err
	}
	episodeData, err := episodeNFOBytes(opts.Episode)
	if err != nil {
		return nil, err
	}
	sidecars := []nfoSidecar{{rel: path.Join(seriesRel, tvShowNFO), data: seriesData}}
	for _, t := range targets {
		rel := strings.TrimSuffix(t.destRel, path.Ext(t.destRel)) + ".nfo"
		sidecars = append(sidecars, nfoSidecar{rel: rel, data: episodeData})
	}
	return sidecars, nil
}

// seriesFolderRel is the rendered folder template prefix before {season}, never an arbitrary parent.
func seriesFolderRel(folderTemplate, folderRel string, opts Options, firstSrc string) (string, error) {
	components := strings.Split(folderTemplate, "/")
	for i, component := range components {
		if !strings.Contains(component, "{season}") {
			continue
		}
		if i == 0 {
			return folderRel, nil
		}
		return renderFolder(strings.Join(components[:i], "/"), opts, firstSrc)
	}
	return folderRel, nil
}

// publishNFO archives any previous sidecar and only ever adds the new one atomically.
func publishNFO(root *os.Root, rel string, data []byte, rec *rollback) error {
	if info, err := root.Lstat(rel); err == nil {
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s is a symbolic link", ErrUnsafe, rel)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: %s is not a file", ErrConflict, rel)
		}
		if current, readErr := root.ReadFile(rel); readErr == nil && bytes.Equal(current, data) {
			return nil
		}
		archived, err := archiveInto(root, rel)
		if err != nil {
			return err
		}
		rec.archives = append(rec.archives, archiveRecord{rel, archived})
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	temp := tempName(path.Dir(rel))
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Chmod(temp, 0o644)
	}
	if err == nil {
		err = publishAt(root, temp, rel)
	}
	if err != nil {
		root.Remove(temp)
		return err
	}
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	rec.nfoWritten = append(rec.nfoWritten, publishedFile{rel: rel, info: info})
	return nil
}
