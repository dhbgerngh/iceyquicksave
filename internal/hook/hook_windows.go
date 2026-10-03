package hook

import (
	"encoding/binary"
	"fmt"
	"golang.org/x/arch/x86/x86asm"
	"iceyquicksave/internal/winapi"
	"math"
	"syscall"
	"unsafe"
)

func Jump(to uintptr) []byte {
	b := []byte{0xff, 0x25, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint64(b[6:], uint64(to))
	return b
}

// Install relocates whole instructions and refuses short relative branches.
// Caller installs only at the main-thread message boundary, before enabling requests.
func Install(at, to uintptr, publish func(uintptr)) (uintptr, error) {
	src := append([]byte(nil), winapi.Bytes(at, 64)...)
	n := 0
	for n < 14 {
		i, e := x86asm.Decode(src[n:], 64)
		if e != nil || i.Len == 0 {
			return 0, fmt.Errorf("decode %x: %v", at+uintptr(n), e)
		}
		if i.PCRel > 0 && i.PCRel != 4 {
			return 0, fmt.Errorf("short branch in hook prologue")
		}
		n += i.Len
	}
	// RIP-relative operands require a trampoline within 2 GB.
	var mem uintptr
	center := at &^ 0xffff
	for d := uintptr(0x10000); d < 0x70000000; d += 0x10000 {
		for _, p := range []uintptr{center + d, center - d} {
			mem = winapi.K("VirtualAlloc", p, 4096, 0x3000, 0x40)
			if mem != 0 {
				break
			}
		}
		if mem != 0 {
			break
		}
	}
	if mem == 0 {
		return 0, fmt.Errorf("no nearby trampoline memory")
	}
	relocated := append([]byte(nil), src[:n]...)
	for off := 0; off < n; {
		i, _ := x86asm.Decode(src[off:], 64)
		if i.PCRel == 4 {
			p := off + i.PCRelOff
			old := int64(int32(binary.LittleEndian.Uint32(src[p:])))
			dest := int64(at) + int64(off+i.Len) + old
			delta := dest - int64(mem) - int64(off+i.Len)
			if delta < math.MinInt32 || delta > math.MaxInt32 {
				winapi.K("VirtualFree", mem, 0, 0x8000)
				return 0, fmt.Errorf("relative operand out of range")
			}
			binary.LittleEndian.PutUint32(relocated[p:], uint32(int32(delta)))
		}
		off += i.Len
	}
	relocated = append(relocated, Jump(at+uintptr(n))...)
	copy(winapi.Bytes(mem, len(relocated)), relocated)
	var old uint32
	if winapi.K("VirtualProtect", at, uintptr(n), 0x40, winapi.Ptr(&old)) == 0 {
		return 0, fmt.Errorf("VirtualProtect failed")
	}
	patch := Jump(to)
	for len(patch) < n {
		patch = append(patch, 0x90)
	}
	publish(mem)
	copy(unsafe.Slice((*byte)(unsafe.Pointer(at)), n), patch)
	winapi.K("FlushInstructionCache", ^uintptr(0), at, uintptr(n))
	var unused uint32
	winapi.K("VirtualProtect", at, uintptr(n), uintptr(old), winapi.Ptr(&unused))
	return mem, nil
}
func Call(fn uintptr, args ...uintptr) uintptr { r, _, _ := syscall.SyscallN(fn, args...); return r }
