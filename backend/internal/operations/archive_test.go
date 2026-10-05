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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type archiveEntry struct {
	name     string
	body     []byte
	typeflag byte
	linkname string
}

func tarArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.body)), Typeflag: typeflag, Linkname: entry.linkname}
		if typeflag != tar.TypeReg && typeflag != tar.TypeRegA {
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatalf("write tar header: %v", err)
		}
		if header.Size > 0 {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatalf("write tar body: %v", err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buffer.Bytes()
}

func zipArchive(t *testing.T, entries []archiveEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(0o600)
		item, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("write zip header: %v", err)
		}
		if _, err := item.Write(entry.body); err != nil {
			t.Fatalf("write zip body: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buffer.Bytes()
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("write gzip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buffer.Bytes()
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func writeBackupDir(t *testing.T, dir string, value manifest, database, files []byte) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create backup dir: %v", err)
	}
	for name, body := range map[string][]byte{backupDatabaseName: database, backupFilesName: files} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := writeManifest(dir, value); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

func emptyTar() []byte {
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	_ = writer.Close()
	return buffer.Bytes()
}

func testManifest(id string, schemaVersion int) (manifest, []byte, []byte) {
	database := []byte("custom-format-dump")
	files := emptyTar()
	value := manifest{
		ID: id, Format: backupFormatName, FormatVersion: backupFormatVersion, SchemaVersion: schemaVersion,
		CreatedAt: time.Now().UTC(), Origin: "manual",
		DatabaseSHA256: sha256Hex(database), DatabaseBytes: int64(len(database)),
		FilesSHA256: sha256Hex(files), FilesBytes: int64(len(files)),
	}
	return value, database, files
}

func TestExtractBackupArchiveRejectsUnsafeEntries(t *testing.T) {
	value, database, files := testManifest("20260101T000000Z-abcdef01", 12)
	manifestBody, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	valid := []archiveEntry{
		{name: backupManifestName, body: manifestBody},
		{name: backupDatabaseName, body: database},
		{name: backupFilesName, body: files},
	}
	cases := []struct {
		name    string
		entries []archiveEntry
	}{
		{"parent traversal", []archiveEntry{{name: "../escape.json", body: []byte("x")}, valid[0], valid[1], valid[2]}},
		{"absolute path", []archiveEntry{{name: "/etc/passwd", body: []byte("x")}, valid[0], valid[1], valid[2]}},
		{"nested traversal", []archiveEntry{{name: "config/../../escape.json", body: []byte("x")}, valid[0], valid[1], valid[2]}},
		{"unknown entry", []archiveEntry{valid[0], valid[1], valid[2], {name: "payload.sh", body: []byte("rm -rf /")}}},
		{"symlink entry", []archiveEntry{{name: "database.dump", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"}, valid[0], valid[2]}},
		{"duplicate entry", []archiveEntry{valid[0], valid[0], valid[1], valid[2]}},
		{"missing dump", []archiveEntry{valid[0], valid[2]}},
		{"backslash path", []archiveEntry{{name: `..\escape.json`, body: []byte("x")}, valid[0], valid[1], valid[2]}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "extract")
			if err := os.MkdirAll(dest, 0o700); err != nil {
				t.Fatalf("create destination: %v", err)
			}
			source := filepath.Join(root, "backup.tar")
			if err := os.WriteFile(source, tarArchive(t, test.entries), 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}
			err := extractBackupArchive(source, dest)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected an invalid archive error, got %v", err)
			}
			assertNoEscape(t, root, dest)
		})
	}
}

func TestExtractBackupArchiveRejectsCorruptionAndBombs(t *testing.T) {
	value, database, files := testManifest("20260101T000000Z-abcdef02", 12)
	manifestBody, _ := json.Marshal(value)
	validEntries := []archiveEntry{
		{name: backupManifestName, body: manifestBody},
		{name: backupDatabaseName, body: database},
		{name: backupFilesName, body: files},
	}
	cases := []struct {
		name string
		body []byte
	}{
		{"garbage", []byte("this is not a backup archive at all, but it is long enough to sniff")},
		{"truncated tar", tarArchive(t, validEntries)[:300]},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "backup.tar")
			if err := os.WriteFile(source, test.body, 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}
			if err := extractBackupArchive(source, t.TempDir()); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected an invalid archive error, got %v", err)
			}
		})
	}

	t.Run("tar gzip bomb", func(t *testing.T) {
		bomb := tarArchive(t, []archiveEntry{
			{name: backupManifestName, body: manifestBody},
			{name: backupDatabaseName, body: make([]byte, 4<<20)},
			{name: backupFilesName, body: files},
		})
		source := filepath.Join(t.TempDir(), "backup.tar.gz")
		if err := os.WriteFile(source, gzipBytes(t, bomb), 0o600); err != nil {
			t.Fatalf("write archive: %v", err)
		}
		if err := extractBackupArchive(source, t.TempDir()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected the compressed archive to be refused, got %v", err)
		}
	})

	t.Run("zip bomb", func(t *testing.T) {
		source := filepath.Join(t.TempDir(), "backup.zip")
		body := zipArchive(t, []archiveEntry{
			{name: backupManifestName, body: manifestBody},
			{name: backupDatabaseName, body: make([]byte, 8<<20)},
			{name: backupFilesName, body: files},
		})
		if err := os.WriteFile(source, body, 0o600); err != nil {
			t.Fatalf("write archive: %v", err)
		}
		if err := extractBackupArchive(source, t.TempDir()); !errors.Is(err, ErrInvalid) {
			t.Fatalf("expected the zip to be refused, got %v", err)
		}
	})
}

func TestExtractBackupArchiveAcceptsTarZipAndGzip(t *testing.T) {
	for _, format := range []string{"tar", "zip", "tar.gz"} {
		t.Run(format, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "source")
			value, database, files := testManifest("20260101T000000Z-abcdef03", 12)
			writeBackupDir(t, dir, value, database, files)
			var buffer bytes.Buffer
			if err := writeBackupArchive(dir, &buffer); err != nil {
				t.Fatalf("write backup archive: %v", err)
			}
			body := buffer.Bytes()
			switch format {
			case "zip":
				body = zipArchive(t, []archiveEntry{
					{name: backupManifestName, body: mustRead(t, filepath.Join(dir, backupManifestName))},
					{name: backupDatabaseName, body: database},
					{name: backupFilesName, body: files},
				})
			case "tar.gz":
				body = gzipBytes(t, body)
			}
			archive := filepath.Join(t.TempDir(), "backup."+format)
			if err := os.WriteFile(archive, body, 0o600); err != nil {
				t.Fatalf("write archive: %v", err)
			}
			dest := filepath.Join(t.TempDir(), "extracted")
			if err := os.MkdirAll(dest, 0o700); err != nil {
				t.Fatalf("create destination: %v", err)
			}
			if err := extractBackupArchive(archive, dest); err != nil {
				t.Fatalf("extract %s: %v", format, err)
			}
			loaded, err := loadManifest(dest)
			if err != nil || loaded.ID != value.ID {
				t.Fatalf("extracted manifest mismatch: %+v (%v)", loaded, err)
			}
		})
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return body
}

func assertNoEscape(t *testing.T, sandbox, dest string) {
	t.Helper()
	entries, err := os.ReadDir(sandbox)
	if err != nil {
		t.Fatalf("read sandbox: %v", err)
	}
	allowed := map[string]bool{filepath.Base(dest): true, "backup.tar": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			t.Fatalf("an archive entry escaped into %s", entry.Name())
		}
	}
	extracted, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("read extraction directory: %v", err)
	}
	for _, entry := range extracted {
		if entry.Name() == "escape.json" || entry.Name() == "payload.sh" || entry.Name() == "passwd" {
			t.Fatalf("an archive entry was extracted outside the allowlist: %s", entry.Name())
		}
	}
}

func TestManifestValidation(t *testing.T) {
	base, _, _ := testManifest("20260101T000000Z-abcdef04", 12)
	cases := []struct {
		name   string
		mutate func(value *manifest)
	}{
		{"foreign format", func(value *manifest) { value.Format = "something-else" }},
		{"newer format", func(value *manifest) { value.FormatVersion = backupFormatVersion + 1 }},
		{"missing schema version", func(value *manifest) { value.SchemaVersion = 0 }},
		{"unsafe identifier", func(value *manifest) { value.ID = "../escape" }},
		{"invalid checksum", func(value *manifest) { value.DatabaseSHA256 = strings.Repeat("z", 64) }},
		{"invalid origin", func(value *manifest) { value.Origin = "unknown" }},
		{"negative size", func(value *manifest) { value.DatabaseBytes = -1 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := base
			test.mutate(&value)
			if err := value.validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("expected an invalid manifest error, got %v", err)
			}
		})
	}
}

func TestVerifyBackupDetectsTampering(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "backup")
	value, database, files := testManifest("20260101T000000Z-abcdef05", 12)
	writeBackupDir(t, dir, value, database, files)
	if err := verifyBackup(dir, value); err != nil {
		t.Fatalf("intact backup should verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, backupDatabaseName), []byte("tampered-dump"), 0o600); err != nil {
		t.Fatalf("tamper with dump: %v", err)
	}
	if err := verifyBackup(dir, value); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected a checksum failure, got %v", err)
	}
}

func TestParseTOCAndValidation(t *testing.T) {
	listing := `;
; Archive created at 2026-10-05 21:28:18 EEST
;     dbname: constellarr
;     TOC Entries: 4
;     Dumped from database version: 18.6
;
219; 1259 16385 TABLE public settings constellarr
3469; 0 16385 TABLE DATA public settings constellarr
`
	info := parseTOC(listing)
	if info.entries != 2 || info.serverVersion != "18.6" {
		t.Fatalf("unexpected TOC parse: %+v", info)
	}
	if !info.hasTable("settings") {
		t.Fatalf("expected the settings table in the TOC: %+v", info)
	}
	if err := validateTOC(info); err != nil {
		t.Fatalf("a Constellarr dump should validate: %v", err)
	}
	cluster := parseTOC("1; 1262 16384 DATABASE constellarr constellarr")
	if err := validateTOC(cluster); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected a cluster-level dump to be refused, got %v", err)
	}
	foreign := parseTOC("219; 1259 16385 TABLE public some_other_table constellarr")
	if err := validateTOC(foreign); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected an unrelated dump to be refused, got %v", err)
	}
}

func TestSanitizeToolErrorHidesConnectionDetails(t *testing.T) {
	databaseURL := "postgresql://user:sup3rsecret@db.internal:5432/constellarr?sslmode=disable"
	output := "pg_dump: error: connection to server at \"db.internal\" failed\npostgresql://user:sup3rsecret@db.internal:5432/constellarr?sslmode=disable\nmore detail"
	message := sanitizeToolError(output, databaseURL)
	if strings.Contains(message, "sup3rsecret") || strings.Contains(message, "5432") {
		t.Fatalf("sanitized message leaks connection details: %q", message)
	}
	if !strings.Contains(message, "[connection]") {
		t.Fatalf("expected the connection string to be replaced: %q", message)
	}
}

func TestValidBackupID(t *testing.T) {
	for _, valid := range []string{"20260101T000000Z-abcdef01", "manual-backup_1", "a"} {
		if !validBackupID(valid) {
			t.Fatalf("expected %q to be valid", valid)
		}
	}
	for _, invalid := range []string{"", "../escape", "a/b", ".hidden", strings.Repeat("a", 65)} {
		if validBackupID(invalid) {
			t.Fatalf("expected %q to be rejected", invalid)
		}
	}
}
