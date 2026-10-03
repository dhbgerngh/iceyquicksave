package archive

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const Limit = 128 << 20

var magic = []byte("ICEYQS01")

// Write commits one checksummed snapshot atomically; a partial file is never a slot.
func Write(path string, v any) error {
	raw, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(raw) > Limit {
		return fmt.Errorf("snapshot exceeds %d bytes", Limit)
	}
	var b bytes.Buffer
	b.Write(magic)
	binary.Write(&b, binary.LittleEndian, uint64(len(raw)))
	sum := sha256.Sum256(raw)
	b.Write(sum[:])
	gz := gzip.NewWriter(&b)
	if _, e = gz.Write(raw); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if e != nil {
		return e
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, e = f.Write(b.Bytes()); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if _, e = os.Stat(path); e == nil {
		return fmt.Errorf("slot already exists")
	}
	return os.Rename(temp, path)
}
func Read(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	h := make([]byte, 48)
	if _, e = io.ReadFull(f, h); e != nil {
		return e
	}
	if !bytes.Equal(h[:8], magic) {
		return fmt.Errorf("invalid snapshot header")
	}
	n := binary.LittleEndian.Uint64(h[8:])
	if n > Limit {
		return fmt.Errorf("snapshot too large")
	}
	z, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer z.Close()
	raw, e := io.ReadAll(io.LimitReader(z, Limit+1))
	if e != nil {
		return e
	}
	if uint64(len(raw)) != n {
		return fmt.Errorf("snapshot size mismatch")
	}
	sum := sha256.Sum256(raw)
	if !bytes.Equal(sum[:], h[16:48]) {
		return fmt.Errorf("snapshot checksum mismatch")
	}
	return json.Unmarshal(raw, v)
}
