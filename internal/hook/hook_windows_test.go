package hook

import (
	"encoding/binary"
	"iceyquicksave/internal/winapi"
	"syscall"
	"testing"
)

func TestTrampolinePreservesRIPRelativeOperand(t *testing.T) {
	mem, e := winapi.Alloc(4096)
	if e != nil {
		t.Fatal(e)
	}
	defer winapi.K("VirtualFree", mem, 0, 0x8000)
	b := winapi.Bytes(mem, 64)
	copy(b, []byte{0x8b, 0x05, 26, 0, 0, 0})
	for i := 6; i < 14; i++ {
		b[i] = 0x90
	}
	b[14] = 0xc3
	binary.LittleEndian.PutUint32(b[32:], 41)
	var original uintptr
	cb := syscall.NewCallback(func() uintptr { return Call(original) + 1 })
	trampoline, e := Install(mem, cb, func(p uintptr) { original = p })
	if e != nil {
		t.Fatal(e)
	}
	defer winapi.K("VirtualFree", trampoline, 0, 0x8000)
	for i := 0; i < 100; i++ {
		if got := Call(mem); got != 42 {
			t.Fatalf("hook = %d", got)
		}
		if got := Call(original); got != 41 {
			t.Fatalf("original = %d", got)
		}
	}
}
func TestRejectsShortBranch(t *testing.T) {
	mem, e := winapi.Alloc(4096)
	if e != nil {
		t.Fatal(e)
	}
	defer winapi.K("VirtualFree", mem, 0, 0x8000)
	copy(winapi.Bytes(mem, 64), []byte{0xeb, 0x10})
	if _, e = Install(mem, mem+32, func(uintptr) {}); e == nil {
		t.Fatal("short relative jump accepted")
	}
}
