package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// libdogecoin file-based headers.db (headersdb_file.c), not SQLite.
// See vendors/libdogecoin/include/dogecoin/headersdb_file.h — magic + uint32 LE version, then fixed-length records.

var libdogeHeadersFileMagic = []byte{0xA8, 0xF0, 0x11, 0xC5}

const (
	libdogeHeadersFileHdrLen = 8  // magic(4) + version uint32 LE(4)
	libdogeHeadersFileRecLen = 148 // hash(32) + height u32(4) + chainwork(32) + header(80)
	libdogeHeadersFileVerMax = 3
)

func isLibdogecoinHeadersFileFormat(path string) bool {
	b := make([]byte, libdogeHeadersFileHdrLen)
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Read(b); err != nil {
		return false
	}
	if len(b) < libdogeHeadersFileHdrLen {
		return false
	}
	if !bytes.Equal(b[:4], libdogeHeadersFileMagic) {
		return false
	}
	ver := binary.LittleEndian.Uint32(b[4:8])
	return ver >= 1 && ver <= libdogeHeadersFileVerMax
}

// truncateLibdogecoinHeadersFile truncates headers.db to the last full record whose height <= keepHeight.
// libdogecoin stores records in chain order with non-decreasing height; height is uint32 LE at offset 32 in each record.
func truncateLibdogecoinHeadersFile(path string, keepHeight int64) (maxKeptHeight uint32, newSize int64, err error) {
	if keepHeight < 0 || keepHeight > 200_000_000 {
		return 0, 0, fmt.Errorf("keep height out of range")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	size := fi.Size()
	if size < libdogeHeadersFileHdrLen {
		return 0, 0, fmt.Errorf("headers file too small")
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	hdr := make([]byte, libdogeHeadersFileHdrLen)
	if _, err := f.ReadAt(hdr, 0); err != nil {
		return 0, 0, err
	}
	if !bytes.Equal(hdr[:4], libdogeHeadersFileMagic) {
		return 0, 0, fmt.Errorf("not libdogecoin headers file magic")
	}
	ver := binary.LittleEndian.Uint32(hdr[4:8])
	if ver < 1 || ver > libdogeHeadersFileVerMax {
		return 0, 0, fmt.Errorf("unsupported headers file version %d", ver)
	}

	cut := int64(libdogeHeadersFileHdrLen)
	off := int64(libdogeHeadersFileHdrLen)
	rec := make([]byte, libdogeHeadersFileRecLen)
	var maxKept uint32
	var firstHeight *uint32

	for off+int64(libdogeHeadersFileRecLen) <= size {
		n, rerr := f.ReadAt(rec, off)
		if rerr != nil || n < libdogeHeadersFileRecLen {
			break
		}
		h := binary.LittleEndian.Uint32(rec[32:36])
		if firstHeight == nil {
			tmp := h
			firstHeight = &tmp
		}
		if int64(h) > keepHeight {
			break
		}
		cut = off + int64(libdogeHeadersFileRecLen)
		maxKept = h
		off += int64(libdogeHeadersFileRecLen)
	}

	if firstHeight == nil {
		if size <= int64(libdogeHeadersFileHdrLen) {
			return 0, 0, fmt.Errorf("headers.db has no header records (only file header)")
		}
		return 0, 0, fmt.Errorf("headers.db is incomplete or corrupt (expected %d-byte records after %d-byte header)", libdogeHeadersFileRecLen, libdogeHeadersFileHdrLen)
	}
	if int64(*firstHeight) > keepHeight {
		return 0, 0, fmt.Errorf("on-disk header chain starts at height %d; cannot roll back to %d (remove headers.db with Full SPV rescan, or choose rollback_height >= %d)", *firstHeight, keepHeight, *firstHeight)
	}

	if cut < int64(libdogeHeadersFileHdrLen) {
		return 0, 0, fmt.Errorf("invalid truncate offset")
	}
	if err := f.Truncate(cut); err != nil {
		return 0, 0, err
	}
	return maxKept, cut, nil
}

// libdogecoinHeadersFileFirstHeight returns the height field of the first stored header record (after the file header).
func libdogecoinHeadersFileFirstHeight(path string) (uint32, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if size < int64(libdogeHeadersFileHdrLen)+int64(libdogeHeadersFileRecLen) {
		return 0, fmt.Errorf("headers.db has no full header records")
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	rec := make([]byte, libdogeHeadersFileRecLen)
	n, err := f.ReadAt(rec, int64(libdogeHeadersFileHdrLen))
	if err != nil || n < libdogeHeadersFileRecLen {
		return 0, fmt.Errorf("headers.db read first record: %w", err)
	}
	return binary.LittleEndian.Uint32(rec[32:36]), nil
}

// libdogecoinHeadersFileTipHeight returns the largest header height stored in a libdogecoin file-based headers.db.
func libdogecoinHeadersFileTipHeight(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if size < int64(libdogeHeadersFileHdrLen)+int64(libdogeHeadersFileRecLen) {
		return 0, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	off := int64(libdogeHeadersFileHdrLen)
	rec := make([]byte, libdogeHeadersFileRecLen)
	var maxH uint32
	for off+int64(libdogeHeadersFileRecLen) <= size {
		n, rerr := f.ReadAt(rec, off)
		if rerr != nil || n < libdogeHeadersFileRecLen {
			break
		}
		h := binary.LittleEndian.Uint32(rec[32:36])
		if h > maxH {
			maxH = h
		}
		off += int64(libdogeHeadersFileRecLen)
	}
	return int64(maxH), nil
}

// libdogecoinHeaderHashHexAtHeight returns lowercase hex of the 32-byte block hash field for the record at height, or "".
func libdogecoinHeaderHashHexAtHeight(path string, height int64) string {
	if height < 0 || height > 200_000_000 {
		return ""
	}
	want := uint32(height)
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	size := fi.Size()
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	off := int64(libdogeHeadersFileHdrLen)
	rec := make([]byte, libdogeHeadersFileRecLen)
	for off+int64(libdogeHeadersFileRecLen) <= size {
		n, rerr := f.ReadAt(rec, off)
		if rerr != nil || n < libdogeHeadersFileRecLen {
			break
		}
		h := binary.LittleEndian.Uint32(rec[32:36])
		if h == want {
			return strings.ToLower(hex.EncodeToString(rec[0:32]))
		}
		off += int64(libdogeHeadersFileRecLen)
	}
	return ""
}

// libdogecoinHeader80HexAtHeight returns lowercase hex of the 80-byte block header stored in the record at height, or "".
func libdogecoinHeader80HexAtHeight(path string, height int64) string {
	if height < 0 || height > 200_000_000 {
		return ""
	}
	want := uint32(height)
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	size := fi.Size()
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	off := int64(libdogeHeadersFileHdrLen)
	rec := make([]byte, libdogeHeadersFileRecLen)
	const headerOff = 32 + 4 + 32 // after hash, height, chainwork
	for off+int64(libdogeHeadersFileRecLen) <= size {
		n, rerr := f.ReadAt(rec, off)
		if rerr != nil || n < libdogeHeadersFileRecLen {
			break
		}
		h := binary.LittleEndian.Uint32(rec[32:36])
		if h == want {
			return strings.ToLower(hex.EncodeToString(rec[headerOff : headerOff+80]))
		}
		off += int64(libdogeHeadersFileRecLen)
	}
	return ""
}
