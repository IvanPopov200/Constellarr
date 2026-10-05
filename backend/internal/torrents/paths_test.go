package torrents

import (
	"encoding/base32"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func infoBytes(t *testing.T, info metainfo.Info) []byte {
	t.Helper()
	raw, err := bencode.Marshal(info)
	if err != nil {
		t.Fatalf("encode info: %v", err)
	}
	return raw
}

func mustAddrPort(t *testing.T, port int) netip.AddrPort {
	t.Helper()
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port))
}

func TestTorrentFileValidation(t *testing.T) {
	valid := metainfo.Info{Name: "Show Pack", PieceLength: 32 << 10, Pieces: make([]byte, 20),
		Files: []metainfo.FileInfo{
			{Length: 10, Path: []string{"Season 1", "episode-01.mkv"}},
			{Length: 20, Path: []string{"Season 1", "episode-02.mkv"}},
		}}
	files, err := torrentFiles(&valid)
	if err != nil {
		t.Fatalf("valid torrent rejected: %v", err)
	}
	if len(files) != 2 || files[0].Name != "Season 1/episode-01.mkv" {
		t.Fatalf("unexpected files: %+v", files)
	}

	cases := map[string][]metainfo.FileInfo{
		"traversal":        {{Length: 1, Path: []string{"..", "escape.txt"}}},
		"nested traversal": {{Length: 1, Path: []string{"media", "..", "..", "escape.txt"}}},
		"absolute":         {{Length: 1, Path: []string{"", "etc", "passwd"}}},
		"backslash":        {{Length: 1, Path: []string{`media\escape.txt`}}},
		"empty component":  {{Length: 1, Path: []string{"media", ""}}},
		"dot component":    {{Length: 1, Path: []string{"."}}},
		"duplicate":        {{Length: 1, Path: []string{"a.mkv"}}, {Length: 1, Path: []string{"a.mkv"}}},
		"case collision":   {{Length: 1, Path: []string{"Show", "EP01.mkv"}}, {Length: 1, Path: []string{"show", "ep01.MKV"}}},
	}
	for name, files := range cases {
		info := metainfo.Info{Name: "pack", PieceLength: 32 << 10, Pieces: make([]byte, 20), Files: files}
		if _, err := torrentFiles(&info); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}

	abs := metainfo.Info{Name: "pack", PieceLength: 32 << 10, Pieces: make([]byte, 20), Files: []metainfo.FileInfo{{Length: 1, Path: []string{"/etc/passwd"}}}}
	if _, err := torrentFiles(&abs); err == nil {
		t.Error("absolute single-component path must be rejected")
	}
}

func TestCheckNoSymlinksAndRemoveDataDir(t *testing.T) {
	root := t.TempDir()
	jobDir := filepath.Join(root, "0123456789abcdef0123456789abcdef01234567")
	if err := os.MkdirAll(jobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	service := &Service{root: root}
	if err := checkNoSymlinks(jobDir); err != nil {
		t.Fatalf("clean directory rejected: %v", err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(jobDir, "payload")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available: %v", err)
	}
	if err := checkNoSymlinks(jobDir); err == nil {
		t.Fatal("symlink inside the job directory must be rejected")
	}
	if err := service.removeDataDir(root); err == nil {
		t.Fatal("removing the root itself must be refused")
	}
	if err := service.removeDataDir(filepath.Join(root, "..", "elsewhere")); err == nil {
		t.Fatal("removing a directory outside the root must be refused")
	}
	if err := service.removeDataDir(jobDir); err != nil {
		t.Fatalf("removing a job directory failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret.txt")); err != nil {
		t.Fatalf("symlink target was removed: %v", err)
	}
}

func TestMagnetParsing(t *testing.T) {
	hex := strings.Repeat("ab", 20)
	magnet := "magnet:?xt=urn:btih:" + hex + "&dn=Example&tr=udp%3A%2F%2Ftracker.example%3A80"
	hash, err := parseMagnet(magnet)
	if err != nil || hash != hex {
		t.Fatalf("hex magnet: %v %q", err, hash)
	}
	upper := strings.ToUpper(hex)
	if hash, err := parseMagnet("magnet:?xt=urn:btih:" + upper); err != nil || hash != hex {
		t.Fatalf("uppercase magnet: %v %q", err, hash)
	}
	base32Hash, err := infoHashFromXT("urn:btih:" + base32.StdEncoding.EncodeToString([]byte(hex[:20])))
	if err != nil || len(base32Hash) != 40 {
		t.Fatalf("base32 magnet rejected: %v %q", err, base32Hash)
	}
	for _, bad := range []string{
		"", "http://example.com/torrent", "magnet:?xt=urn:btmh:1220" + hex, "magnet:?xt=urn:btih:xyz",
		"magnet:?dn=missing-xt",
	} {
		if _, err := parseMagnet(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
	long := "magnet:?xt=urn:btih:" + hex + "&dn=" + strings.Repeat("a", maxMagnetBytes)
	if _, err := parseMagnet(long); err == nil {
		t.Error("oversized magnet must be rejected")
	}
}

func TestTorrentFileAndBase64Bounds(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "tiny.bin")
	if err := os.WriteFile(name, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := metainfo.Info{PieceLength: 16 << 10}
	if err := info.BuildFromFilePath(name); err != nil {
		t.Fatal(err)
	}
	raw := infoBytes(t, info)
	meta := metainfo.MetaInfo{InfoBytes: raw}
	var buf strings.Builder
	if err := meta.Write(&buf); err != nil {
		t.Fatal(err)
	}
	loaded, hash, err := loadMetainfo([]byte(buf.String()))
	if err != nil || len(hash) != 40 {
		t.Fatalf("load torrent: %v %q", err, hash)
	}
	if loaded.HashInfoBytes().HexString() != hash {
		t.Fatal("hash mismatch")
	}
	if _, _, err := loadMetainfo([]byte("not bencode")); err == nil {
		t.Fatal("garbage torrent must be rejected")
	}
	if _, _, err := loadMetainfo(nil); err == nil {
		t.Fatal("empty torrent must be rejected")
	}
}

func TestSafeFilePathDropsUnsafeComponents(t *testing.T) {
	info := metainfo.Info{Name: "tiny.bin"}
	file := metainfo.FileInfo{Length: 1, Path: []string{"ok", "..", "..", "escape"}}
	got := safeFilePath(&info, file)
	if strings.Contains(got, "..") || strings.HasPrefix(got, "/") {
		t.Fatalf("unsafe path survived: %q", got)
	}
	single := metainfo.FileInfo{Length: 1}
	if name := safeFilePath(&info, single); name != "tiny.bin" {
		t.Fatalf("single-file torrents must use the torrent name: %q", name)
	}
}
