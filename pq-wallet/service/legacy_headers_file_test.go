package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestTruncateLibdogecoinHeadersFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "headers.db")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write(libdogeHeadersFileMagic)
	_ = binary.Write(f, binary.LittleEndian, uint32(3))
	rec := make([]byte, libdogeHeadersFileRecLen)
	binary.LittleEndian.PutUint32(rec[32:36], 0)
	if _, err := f.Write(rec); err != nil {
		t.Fatal(err)
	}
	rec2 := make([]byte, libdogeHeadersFileRecLen)
	binary.LittleEndian.PutUint32(rec2[32:36], 100)
	if _, err := f.Write(rec2); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	maxK, sz, err := truncateLibdogecoinHeadersFile(p, 50)
	if err != nil {
		t.Fatal(err)
	}
	if maxK != 0 {
		t.Fatalf("maxKept=%d want 0", maxK)
	}
	if sz != libdogeHeadersFileHdrLen+libdogeHeadersFileRecLen {
		t.Fatalf("size=%d", sz)
	}
	st, _ := os.Stat(p)
	if st.Size() != sz {
		t.Fatalf("file size %d != %d", st.Size(), sz)
	}
}
