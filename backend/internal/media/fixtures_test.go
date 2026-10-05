package media

import (
	"archive/zip"
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"hash/crc32"
	"os"
	"testing"
)

type zipEntry struct {
	name  string
	data  []byte
	mode  os.FileMode
	flags uint16
}

func writeZIP(t *testing.T, file string, entries ...zipEntry) {
	t.Helper()
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(f)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store, Flags: entry.flags}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		w, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

type rarEntry struct {
	name  string
	data  []byte
	attrs uint32
	split uint16 // file flags marking the block as split before or after this volume
	size  uint32 // unpacked size, defaults to len(data)
	crc   uint32 // unpacked CRC32, defaults to the CRC of data
}

func rarArchive(entries ...rarEntry) []byte {
	return rarVolume(entries, true, true)
}

// rarVolume encodes one stored (uncompressed) RAR 4 volume.
func rarVolume(entries []rarEntry, single, last bool) []byte {
	return rarVolumeNamed(entries, single, last, true)
}

func rarVolumeNamed(entries []rarEntry, single, last, newNaming bool) []byte {
	var buf bytes.Buffer
	buf.Write([]byte("Rar!\x1a\x07\x00"))
	var archiveFlags uint16
	if newNaming {
		archiveFlags |= 0x0010
	}
	if !single {
		archiveFlags |= 0x0001
	}
	buf.Write(rarBlock(0x73, archiveFlags, make([]byte, 6)))
	for _, entry := range entries {
		size := entry.size
		if size == 0 {
			size = uint32(len(entry.data))
		}
		crc := entry.crc
		if crc == 0 {
			crc = crc32.ChecksumIEEE(entry.data)
		}
		body := binary.LittleEndian.AppendUint32(nil, uint32(len(entry.data)))
		body = binary.LittleEndian.AppendUint32(body, size)
		body = append(body, 3) // host OS: unix
		body = binary.LittleEndian.AppendUint32(body, crc)
		body = binary.LittleEndian.AppendUint32(body, 0x50000000) // DOS time
		body = append(body, 20, 0x30)                             // unpack version, store method
		body = binary.LittleEndian.AppendUint16(body, uint16(len(entry.name)))
		attrs := entry.attrs
		if attrs == 0 {
			attrs = 0x81a4
		}
		body = binary.LittleEndian.AppendUint32(body, attrs)
		body = append(body, entry.name...)
		buf.Write(rarBlock(0x74, 0x8000|entry.split, body))
		buf.Write(entry.data)
	}
	endFlags := uint16(0)
	if !last {
		endFlags = 0x0001
	}
	buf.Write(rarBlock(0x7b, endFlags, nil))
	return buf.Bytes()
}

func rarBlock(htype byte, flags uint16, body []byte) []byte {
	header := make([]byte, 7, 7+len(body))
	header[2] = htype
	binary.LittleEndian.PutUint16(header[3:5], flags)
	binary.LittleEndian.PutUint16(header[5:7], uint16(7+len(body)))
	header = append(header, body...)
	binary.LittleEndian.PutUint16(header[0:2], uint16(crc32.ChecksumIEEE(header[2:])))
	return header
}

func par2Packet(typ string, body []byte) []byte {
	packet := make([]byte, par2HeaderBytes, par2HeaderBytes+len(body))
	copy(packet, par2Magic)
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	binary.LittleEndian.PutUint64(packet[8:16], uint64(par2HeaderBytes+len(body)))
	copy(packet[48:64], typ)
	packet = append(packet, body...)
	digest := md5.Sum(packet[32:])
	copy(packet[16:32], digest[:])
	return packet
}

func par2FileDescPacket(name string) []byte {
	body := make([]byte, 56, 56+len(name))
	copy(body[0:16], "file-id-00000001")
	copy(body[16:32], "full-md5-hash-01")
	copy(body[32:48], "part-md5-hash-01")
	binary.LittleEndian.PutUint64(body[48:56], 4096)
	return par2Packet(par2FileDescType, append(body, name...))
}

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
