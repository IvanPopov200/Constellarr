package torrents

import (
	"bytes"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

const (
	maxMagnetBytes  = 4096
	maxTorrentBytes = 8 << 20
)

func parseMagnet(uri string) (string, error) {
	uri = strings.TrimSpace(uri)
	if len(uri) > maxMagnetBytes || !strings.HasPrefix(strings.ToLower(uri), "magnet:?") {
		return "", invalidMagnet()
	}
	values, err := url.ParseQuery(uri[len("magnet:?"):])
	if err != nil {
		return "", invalidMagnet()
	}
	for _, xt := range values["xt"] {
		hash, err := infoHashFromXT(xt)
		if err == nil {
			return hash, nil
		}
	}
	return "", invalidMagnet()
}

func infoHashFromXT(xt string) (string, error) {
	value, ok := strings.CutPrefix(xt, "urn:btih:")
	if !ok || value == "" {
		return "", invalidMagnet()
	}
	switch {
	case len(value) == 40:
		hash := strings.ToLower(value)
		if !validInfoHash(hash) {
			return "", invalidMagnet()
		}
		return hash, nil
	case len(value) == 32:
		raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(value))
		if err != nil || len(raw) != 20 {
			return "", invalidMagnet()
		}
		return hex.EncodeToString(raw), nil
	default:
		return "", invalidMagnet()
	}
}

func invalidMagnet() error {
	return invalid("the magnet link is not a valid BitTorrent v1 magnet")
}

// privateFromMetainfo reports the BEP 27 private flag of a torrent file.
func privateFromMetainfo(info *metainfo.Info) bool {
	return info != nil && info.Private != nil && *info.Private
}

// loadMetainfo parses a torrent file and rejects files without an info dictionary.
func loadMetainfo(data []byte) (*metainfo.MetaInfo, string, error) {
	if len(data) == 0 || len(data) > maxTorrentBytes {
		return nil, "", invalid("the torrent file is empty or too large")
	}
	meta, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, "", invalid("the torrent file is not valid bencode")
	}
	info, err := meta.UnmarshalInfo()
	if err != nil {
		return nil, "", invalid("the torrent file has no readable metadata")
	}
	if _, err := torrentFiles(&info); err != nil {
		return nil, "", err
	}
	hash := meta.HashInfoBytes().HexString()
	return meta, hash, nil
}

func decodeBase64Torrent(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if index := strings.IndexByte(value, ','); strings.HasPrefix(value, "data:") && index >= 0 {
		value = value[index+1:]
	}
	if value == "" || len(value) > base64.StdEncoding.EncodedLen(maxTorrentBytes) {
		return nil, invalid("the encoded torrent file is empty or too large")
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, invalid("the torrent file is not valid base64")
	}
	if len(data) > maxTorrentBytes {
		return nil, invalid("the torrent file is too large")
	}
	return data, nil
}

// magnetHasTrackers reports a magnet that carries its own tracker announcements.
func magnetHasTrackers(uri string) bool {
	values, err := url.ParseQuery(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(uri), "magnet:?")))
	if err != nil {
		return false
	}
	for _, tracker := range values["tr"] {
		if strings.TrimSpace(tracker) != "" {
			return true
		}
	}
	return false
}

func magnetFromInfoHash(hash, name string) string {
	if !validInfoHash(hash) {
		return ""
	}
	magnet := "magnet:?xt=urn:btih:" + hash
	if name = strings.TrimSpace(name); name != "" && len(name) <= maxNameRunes {
		magnet += "&dn=" + url.QueryEscape(name)
	}
	return magnet
}
