package peproxy

import (
	"bytes"
	"debug/pe"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestForwardSystemExports(t *testing.T) {
	b := make([]byte, 1024)
	copy(b, "MZ")
	le.PutUint32(b[0x3c:], 0x80)
	copy(b[0x80:], "PE\x00\x00")
	le.PutUint16(b[0x84:], 0x8664)
	le.PutUint16(b[0x86:], 1)
	le.PutUint16(b[0x94:], 240)
	opt := 0x98
	le.PutUint16(b[opt:], 0x20b)
	le.PutUint32(b[opt+32:], 4096)
	le.PutUint32(b[opt+36:], 512)
	le.PutUint32(b[opt+56:], 8192)
	le.PutUint32(b[opt+60:], 512)
	le.PutUint32(b[opt+108:], 16)
	sect := opt + 240
	copy(b[sect:], ".text")
	le.PutUint32(b[sect+8:], 512)
	le.PutUint32(b[sect+12:], 4096)
	le.PutUint32(b[sect+16:], 512)
	le.PutUint32(b[sect+20:], 512)
	p := filepath.Join(t.TempDir(), "proxy.dll")
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	system := filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll")
	want, e := Exports(system)
	if e != nil {
		t.Fatal(e)
	}
	if e = Forward(p, system, "iceyqs_system_version"); e != nil {
		t.Fatal(e)
	}
	got, e := Exports(p)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("exports mismatch: %v / %v", want, got)
	}
	data, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	f, e := pe.NewFile(bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if f.NumberOfSections != 2 {
		t.Fatal("missing section")
	}
	s := f.Sections[1]
	d := data[s.Offset:]
	name := le.Uint32(d[12:]) - s.VirtualAddress
	if string(d[name:name+12]) != "version.dll\x00" {
		t.Fatal("invalid export DLL name")
	}
	for i := uint32(0); i < le.Uint32(d[20:]); i++ {
		rva := le.Uint32(d[40+4*i:])
		if rva == 0 {
			continue
		}
		off := rva - s.VirtualAddress
		if !bytes.HasPrefix(d[off:], []byte("iceyqs_system_version.")) {
			t.Fatal("incorrect forwarder")
		}
	}
}
