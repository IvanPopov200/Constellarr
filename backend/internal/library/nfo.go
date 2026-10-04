package library

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"

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

type nfoMovie struct {
	XMLName   xml.Name      `xml:"movie"`
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

func nfoBytes(title metadata.Title) ([]byte, error) {
	doc := nfoMovie{
		Title:     title.Title,
		Year:      title.Year,
		Plot:      title.Plot,
		IMDbID:    title.IMDbID,
		Directors: title.Directors,
		Genres:    title.Genres,
	}
	if title.Rating != nil {
		doc.Rating = *title.Rating
	}
	for i, name := range title.Cast {
		doc.Actors = append(doc.Actors, nfoActor{Name: name, Order: i})
	}
	if title.IMDbID != "" {
		doc.UniqueIDs = append(doc.UniqueIDs, nfoUniqueID{Type: "imdb", Value: title.IMDbID})
	}
	body, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}

// writeNFO publishes movie.nfo atomically, archiving any previous sidecar.
func writeNFO(root *os.Root, folderRel string, title metadata.Title, rec *rollback) error {
	data, err := nfoBytes(title)
	if err != nil {
		return err
	}
	rel := path.Join(folderRel, nfoName)
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
	temp := tempName(folderRel)
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
	rec.nfoWritten = &publishedFile{rel: rel, info: info}
	return nil
}
