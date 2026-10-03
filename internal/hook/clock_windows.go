package hook

import (
	"encoding/binary"
	"fmt"
	"iceyquicksave/internal/winapi"
	"math"
)

// FloatClock adapts a Windows x64 float-returning, zero-argument engine icall.
// The tiny ABI stub is emitted by Go; all policy and saved data remain Go code.
// A Go syscall callback cannot return a float in XMM0, hence the native stub.
type FloatClock struct {
	Memory, Original uintptr
	Offset           float32
}

func InstallClock(at uintptr) (*FloatClock, error) {
	mem, e := winapi.Alloc(4096)
	if e != nil {
		return nil, e
	}
	c := &FloatClock{Memory: mem}
	// sub rsp,40; mov rax,original; call rax; add rsp,40;
	// subss xmm0,[rip+offset]; ret
	code := []byte{0x48, 0x83, 0xec, 0x28, 0x48, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xd0, 0x48, 0x83, 0xc4, 0x28, 0xf3, 0x0f, 0x5c, 0x05, 0, 0, 0, 0, 0xc3}
	binary.LittleEndian.PutUint32(code[24:], 128-28)
	copy(winapi.Bytes(mem, len(code)), code)
	original, e := Install(at, mem, func(p uintptr) { binary.LittleEndian.PutUint64(winapi.Bytes(mem+6, 8), uint64(p)); c.Original = p })
	if e != nil {
		winapi.K("VirtualFree", mem, 0, 0x8000)
		return nil, fmt.Errorf("clock hook: %w", e)
	}
	c.Original = original
	return c, nil
}
func (c *FloatClock) Shift(delta float32) {
	c.Offset += delta
	binary.LittleEndian.PutUint32(winapi.Bytes(c.Memory+128, 4), math.Float32bits(c.Offset))
}
