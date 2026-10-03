// Package peproxy adds a forwarding export table to a Go shared library.
package peproxy

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

var le = binary.LittleEndian

type Export struct {
	Name    string
	Ordinal uint32
}

func Exports(path string) ([]Export, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := pe.NewFile(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return nil, fmt.Errorf("expected PE64")
	}
	at := func(rva uint32) ([]byte, error) {
		for _, s := range f.Sections {
			if rva >= s.VirtualAddress && uint64(rva-s.VirtualAddress) < uint64(s.Size) {
				off := uint64(s.Offset) + uint64(rva-s.VirtualAddress)
				if off < uint64(len(b)) {
					return b[off:], nil
				}
			}
		}
		return nil, fmt.Errorf("invalid RVA %x", rva)
	}
	d, err := at(oh.DataDirectory[0].VirtualAddress)
	if err != nil || len(d) < 40 {
		return nil, fmt.Errorf("missing exports")
	}
	base, nfn, nname := le.Uint32(d[16:]), le.Uint32(d[20:]), le.Uint32(d[24:])
	funcs, err := at(le.Uint32(d[28:]))
	if err != nil {
		return nil, err
	}
	names, err := at(le.Uint32(d[32:]))
	if err != nil {
		return nil, err
	}
	ords, err := at(le.Uint32(d[36:]))
	if err != nil {
		return nil, err
	}
	if nfn > 65536 || nname > 65536 || len(funcs) < int(nfn)*4 || len(names) < int(nname)*4 || len(ords) < int(nname)*2 {
		return nil, fmt.Errorf("invalid export lengths")
	}
	labels := map[uint32]string{}
	for i := uint32(0); i < nname; i++ {
		s, e := at(le.Uint32(names[i*4:]))
		if e != nil {
			return nil, e
		}
		end := bytes.IndexByte(s, 0)
		if end < 0 {
			return nil, fmt.Errorf("unterminated export")
		}
		labels[uint32(le.Uint16(ords[i*2:]))] = string(s[:end])
	}
	var result []Export
	for i := uint32(0); i < nfn; i++ {
		if le.Uint32(funcs[i*4:]) != 0 {
			result = append(result, Export{labels[i], base + i})
		}
	}
	return result, nil
}

func Forward(dll, original, target string) error {
	exports, err := Exports(original)
	if err != nil {
		return err
	}
	if len(exports) == 0 {
		return fmt.Errorf("empty exports")
	}
	b, err := os.ReadFile(dll)
	if err != nil {
		return err
	}
	f, err := pe.NewFile(bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		return fmt.Errorf("not PE64")
	}
	peoff := int(le.Uint32(b[0x3c:]))
	opt := peoff + 24
	sect := opt + int(f.SizeOfOptionalHeader) + int(f.NumberOfSections)*40
	first := len(b)
	for _, s := range f.Sections {
		if s.Offset > 0 && int(s.Offset) < first {
			first = int(s.Offset)
		}
	}
	if sect+40 > first {
		return fmt.Errorf("no spare section header")
	}
	align := func(v, a uint32) uint32 { return (v + a - 1) / a * a }
	var va uint32
	for _, s := range f.Sections {
		end := s.VirtualAddress + max(s.VirtualSize, s.Size)
		if end > va {
			va = end
		}
	}
	va = align(va, oh.SectionAlignment)
	minOrd, maxOrd := exports[0].Ordinal, exports[0].Ordinal
	var named []Export
	for _, e := range exports {
		minOrd = min(minOrd, e.Ordinal)
		maxOrd = max(maxOrd, e.Ordinal)
		if e.Name != "" {
			named = append(named, e)
		}
	}
	sort.Slice(named, func(i, j int) bool { return named[i].Name < named[j].Name })
	nf := maxOrd - minOrd + 1
	nn := uint32(len(named))
	buf := make([]byte, 40+nf*4+nn*6)
	str := func(s string) uint32 {
		r := va + uint32(len(buf))
		buf = append(buf, []byte(s)...)
		buf = append(buf, 0)
		return r
	}
	dllName := str("version.dll")
	le.PutUint32(buf[12:], dllName)
	le.PutUint32(buf[16:], minOrd)
	le.PutUint32(buf[20:], nf)
	le.PutUint32(buf[24:], nn)
	le.PutUint32(buf[28:], va+40)
	le.PutUint32(buf[32:], va+40+nf*4)
	le.PutUint32(buf[36:], va+40+nf*4+nn*4)
	for _, e := range exports {
		suffix := e.Name
		if suffix == "" {
			suffix = fmt.Sprintf("#%d", e.Ordinal)
		}
		r := str(target + "." + suffix)
		le.PutUint32(buf[40+(e.Ordinal-minOrd)*4:], r)
	}
	for i, e := range named {
		r := str(e.Name)
		le.PutUint32(buf[40+nf*4+uint32(i)*4:], r)
		le.PutUint16(buf[40+nf*4+nn*4+uint32(i)*2:], uint16(e.Ordinal-minOrd))
	}
	raw := align(uint32(len(b)), oh.FileAlignment)
	sz := align(uint32(len(buf)), oh.FileAlignment)
	copy(b[sect:], []byte(".qsexp\x00\x00"))
	le.PutUint32(b[sect+8:], uint32(len(buf)))
	le.PutUint32(b[sect+12:], va)
	le.PutUint32(b[sect+16:], sz)
	le.PutUint32(b[sect+20:], raw)
	le.PutUint32(b[sect+36:], 0x40000040)
	le.PutUint16(b[peoff+6:], f.NumberOfSections+1)
	le.PutUint32(b[opt+56:], align(va+uint32(len(buf)), oh.SectionAlignment))
	le.PutUint32(b[opt+64:], 0)
	le.PutUint32(b[opt+112:], va)
	le.PutUint32(b[opt+116:], uint32(len(buf)))
	// Authenticode no longer applies after the new section is added.
	le.PutUint64(b[opt+112+4*8:], 0)
	b = append(b, make([]byte, int(raw+sz)-len(b))...)
	copy(b[raw:], buf)
	return os.WriteFile(dll, b, 0644)
}
