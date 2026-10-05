package operations

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxArchiveBytes       = 1 << 30
	maxEntryBytes         = 512 << 20
	maxTotalUncompressed  = 2 << 30
	maxArchiveEntries     = 16
	maxManifestBytes      = 1 << 20
	maxCompressionRatio   = 200
	backupManifestName    = "manifest.json"
	backupDatabaseName    = "database.dump"
	backupFilesName       = "files.tar"
	maxConfigEntryBytes   = 64 << 20
	maxConfigArchiveTotal = 512 << 20
)

type manifest struct {
	ID             string    `json:"id"`
	Format         string    `json:"format"`
	FormatVersion  int       `json:"formatVersion"`
	SchemaVersion  int       `json:"schemaVersion"`
	CreatedAt      time.Time `json:"createdAt"`
	Origin         string    `json:"origin"`
	DatabaseSHA256 string    `json:"databaseSha256"`
	DatabaseBytes  int64     `json:"databaseBytes"`
	FilesSHA256    string    `json:"filesSha256"`
	FilesBytes     int64     `json:"filesBytes"`
	ServerVersion  string    `json:"serverVersion,omitempty"`
	Included       []string  `json:"included"`
	Excluded       []string  `json:"excluded"`
	RollbackFor    string    `json:"rollbackFor,omitempty"`
}

const backupFormatName = "constellarr-backup"

func (m manifest) validate() error {
	if m.Format != backupFormatName {
		return InvalidError("the archive is not a Constellarr backup")
	}
	if m.FormatVersion != backupFormatVersion {
		return InvalidError("the backup format version is not supported")
	}
	if m.SchemaVersion < 1 {
		return InvalidError("the backup does not record a schema version")
	}
	if m.ID == "" || !validBackupID(m.ID) {
		return InvalidError("the backup manifest has an invalid identifier")
	}
	if m.CreatedAt.IsZero() {
		return InvalidError("the backup manifest has no creation time")
	}
	if len(m.DatabaseSHA256) != 64 || len(m.FilesSHA256) != 64 {
		return InvalidError("the backup manifest has an invalid checksum")
	}
	if _, err := hex.DecodeString(m.DatabaseSHA256); err != nil {
		return InvalidError("the backup manifest has an invalid checksum")
	}
	if _, err := hex.DecodeString(m.FilesSHA256); err != nil {
		return InvalidError("the backup manifest has an invalid checksum")
	}
	if m.DatabaseBytes < 0 || m.FilesBytes < 0 {
		return InvalidError("the backup manifest has an invalid size")
	}
	switch m.Origin {
	case "manual", "scheduled", "rollback", "imported":
	default:
		return InvalidError("the backup manifest has an invalid origin")
	}
	if m.RollbackFor != "" && !validBackupID(m.RollbackFor) {
		return InvalidError("the backup manifest has an invalid rollback reference")
	}
	return nil
}

// safeEntryName rejects absolute paths, parent traversal, symlink-style names and duplicates.
func safeEntryName(name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || strings.Contains(name, `\`) || strings.HasPrefix(name, "/") {
		return "", InvalidError("the archive contains an unsafe entry name")
	}
	cleaned := path.Clean(name)
	if cleaned == "." || cleaned != name || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", InvalidError("the archive contains an unsafe entry name")
	}
	return cleaned, nil
}

func extractBackupArchive(archivePath, destDir string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return InvalidError("the backup upload could not be read")
	}
	defer file.Close()
	header := make([]byte, 512)
	count, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return InvalidError("the backup upload could not be read")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return InvalidError("the backup upload could not be read")
	}
	switch {
	case count >= 4 && bytes.HasPrefix(header, []byte("PK\x03\x04")):
		return extractZipEntries(file, destDir)
	case count >= 2 && header[0] == 0x1f && header[1] == 0x8b:
		info, err := file.Stat()
		if err != nil {
			return InvalidError("the backup upload could not be read")
		}
		reader, err := gzip.NewReader(io.LimitReader(file, maxArchiveBytes))
		if err != nil {
			return InvalidError("the backup archive is not readable")
		}
		defer reader.Close()
		return extractTarEntries(reader, destDir, info.Size())
	case count >= 512 && string(header[257:262]) == "ustar":
		return extractTarEntries(io.LimitReader(file, maxArchiveBytes), destDir, 0)
	}
	return InvalidError("the upload is not a supported tar or zip backup archive")
}

// archiveBytes is the compressed size for ratio checks, or zero for an uncompressed tar.
func extractTarEntries(source io.Reader, destDir string, archiveBytes int64) error {
	reader := tar.NewReader(source)
	seen := map[string]bool{}
	total := int64(0)
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return InvalidError("the backup archive is corrupted")
		}
		entries++
		if entries > maxArchiveEntries {
			return InvalidError("the backup archive has too many entries")
		}
		name, err := safeEntryName(header.Name)
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return InvalidError("the backup archive contains an unsupported entry type")
		}
		if !knownBackupEntry(name) {
			return InvalidError("the backup archive contains an unexpected entry")
		}
		if seen[name] {
			return InvalidError("the backup archive contains a duplicate entry")
		}
		seen[name] = true
		if header.Size < 0 || header.Size > maxEntryBytes {
			return InvalidError("a backup entry is too large")
		}
		total += header.Size
		if total > maxTotalUncompressed {
			return InvalidError("the backup archive expands beyond the allowed size")
		}
		if exceedsRatio(header.Size, archiveBytes) {
			return InvalidError("the backup archive expands beyond the allowed ratio")
		}
		if err := writeEntry(reader, filepath.Join(destDir, name), header.Size); err != nil {
			return err
		}
	}
	for name := range knownEntries {
		if !seen[name] {
			return InvalidError("the backup archive is missing the " + name + " entry")
		}
	}
	return nil
}

func exceedsRatio(size, archiveBytes int64) bool {
	if archiveBytes <= 0 || size <= 1<<20 {
		return false
	}
	return size > maxCompressionRatio*archiveBytes
}

func extractZipEntries(file *os.File, destDir string) error {
	info, err := file.Stat()
	if err != nil {
		return InvalidError("the backup upload could not be read")
	}
	reader, err := zip.NewReader(file, info.Size())
	if err != nil {
		return InvalidError("the backup archive is corrupted")
	}
	if len(reader.File) > maxArchiveEntries {
		return InvalidError("the backup archive has too many entries")
	}
	seen := map[string]bool{}
	total := int64(0)
	for _, entry := range reader.File {
		name, err := safeEntryName(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if entry.Mode()&os.ModeType != 0 {
			return InvalidError("the backup archive contains an unsupported entry type")
		}
		if !knownBackupEntry(name) {
			return InvalidError("the backup archive contains an unexpected entry")
		}
		if seen[name] {
			return InvalidError("the backup archive contains a duplicate entry")
		}
		seen[name] = true
		if entry.UncompressedSize64 > maxEntryBytes {
			return InvalidError("a backup entry is too large")
		}
		total += int64(entry.UncompressedSize64)
		if total > maxTotalUncompressed {
			return InvalidError("the backup archive expands beyond the allowed size")
		}
		if entry.CompressedSize64 > 0 && entry.UncompressedSize64 > maxCompressionRatio*entry.CompressedSize64 && entry.UncompressedSize64 > 1<<20 {
			return InvalidError("the backup archive expands beyond the allowed ratio")
		}
		opened, err := entry.Open()
		if err != nil {
			return InvalidError("the backup archive is corrupted")
		}
		written := io.LimitReader(opened, int64(entry.UncompressedSize64)+1)
		err = writeEntry(written, filepath.Join(destDir, name), -1)
		opened.Close()
		if err != nil {
			return err
		}
	}
	for name := range knownEntries {
		if !seen[name] {
			return InvalidError("the backup archive is missing the " + name + " entry")
		}
	}
	return nil
}

var knownEntries = map[string]bool{
	backupManifestName: true, backupDatabaseName: true, backupFilesName: true,
}

func knownBackupEntry(name string) bool { return knownEntries[name] }

// writeEntry writes a regular file only; existing files and links are never followed.
func writeEntry(source io.Reader, destination string, expected int64) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("operations: the backup could not be written")
	}
	written, copyErr := io.Copy(file, io.LimitReader(source, maxEntryBytes+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(destination)
		return InvalidError("the backup archive could not be extracted")
	}
	if written > maxEntryBytes || (expected >= 0 && written != expected) {
		os.Remove(destination)
		return InvalidError("a backup entry does not match its recorded size")
	}
	return nil
}

func loadManifest(dir string) (manifest, error) {
	file, err := os.Open(filepath.Join(dir, backupManifestName))
	if err != nil {
		return manifest{}, ErrNotFound
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return manifest{}, InvalidError("the backup manifest could not be read")
	}
	if len(raw) > maxManifestBytes {
		return manifest{}, InvalidError("the backup manifest is too large")
	}
	var loaded manifest
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return manifest{}, InvalidError("the backup manifest is not valid JSON")
	}
	if err := loaded.validate(); err != nil {
		return manifest{}, err
	}
	return loaded, nil
}

func writeManifest(dir string, value manifest) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return errors.New("operations: the backup manifest could not be encoded")
	}
	return os.WriteFile(filepath.Join(dir, backupManifestName), body, 0o600)
}

func verifyBackup(dir string, value manifest) error {
	database := filepath.Join(dir, backupDatabaseName)
	sum, size, err := hashFile(database)
	if err != nil {
		return InvalidError("the backup database dump is missing")
	}
	if size != value.DatabaseBytes || sum != value.DatabaseSHA256 {
		return InvalidError("the backup database dump does not match its checksum")
	}
	files := filepath.Join(dir, backupFilesName)
	filesSum, filesSize, err := hashFile(files)
	if err != nil {
		return InvalidError("the backup file archive is missing")
	}
	if filesSize != value.FilesBytes || filesSum != value.FilesSHA256 {
		return InvalidError("the backup file archive does not match its checksum")
	}
	return validateFilesArchive(files)
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func validateFilesArchive(name string) error {
	file, err := os.Open(name)
	if err != nil {
		return InvalidError("the backup file archive could not be read")
	}
	defer file.Close()
	reader := tar.NewReader(file)
	total := int64(0)
	entries := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return InvalidError("the backup file archive is corrupted")
		}
		entries++
		if entries > 4096 {
			return InvalidError("the backup file archive has too many entries")
		}
		cleaned, err := safeEntryName(header.Name)
		if err != nil {
			return err
		}
		if !allowedConfigEntry(cleaned) {
			return InvalidError("the backup file archive contains an unexpected entry")
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return InvalidError("the backup file archive contains an unsupported entry type")
		}
		if header.Size < 0 || header.Size > maxConfigEntryBytes {
			return InvalidError("a backup file entry is too large")
		}
		total += header.Size
		if total > maxConfigArchiveTotal {
			return InvalidError("the backup file archive is too large")
		}
	}
}

func writeBackupArchive(dir string, writer io.Writer) error {
	archive := tar.NewWriter(writer)
	for _, name := range []string{backupManifestName, backupDatabaseName, backupFilesName} {
		source := filepath.Join(dir, name)
		file, err := os.Open(source)
		if err != nil {
			return ErrNotFound
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return InvalidError("the backup is not readable")
		}
		header := &tar.Header{Name: name, Mode: 0o600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			file.Close()
			return errors.New("operations: the backup archive could not be written")
		}
		if _, err := io.Copy(archive, file); err != nil {
			file.Close()
			return errors.New("operations: the backup archive could not be written")
		}
		file.Close()
	}
	return archive.Close()
}
