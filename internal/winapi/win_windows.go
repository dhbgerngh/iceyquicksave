package winapi

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

var Kernel = syscall.NewLazyDLL("kernel32.dll")
var User = syscall.NewLazyDLL("user32.dll")
var GDI = syscall.NewLazyDLL("gdi32.dll")

func K(n string, a ...uintptr) uintptr { r, _, _ := Kernel.NewProc(n).Call(a...); return r }
func U(n string, a ...uintptr) uintptr { r, _, _ := User.NewProc(n).Call(a...); return r }
func G(n string, a ...uintptr) uintptr { r, _, _ := GDI.NewProc(n).Call(a...); return r }
func Ptr[T any](p *T) uintptr          { return uintptr(unsafe.Pointer(p)) }
func UTF(s string) *uint16             { p, _ := syscall.UTF16PtrFromString(s); return p }
func Z(s string) *byte                 { p, _ := syscall.BytePtrFromString(s); return p }
func Bytes(p uintptr, n int) []byte    { return unsafe.Slice((*byte)(unsafe.Pointer(p)), n) }
func ReadPtr(p uintptr) uintptr        { return *(*uintptr)(unsafe.Pointer(p)) }
func Message(s string)                 { U("MessageBoxW", 0, Ptr(UTF(s)), Ptr(UTF("ICEY Quick Save")), 0x40) }

type Rect struct{ Left, Top, Right, Bottom int32 }
type Point struct{ X, Y int32 }
type Msg struct {
	Hwnd           uintptr
	Message        uint32
	Wparam, Lparam uintptr
	Time           uint32
	Pt             Point
	Private        uint32
}

type windowSearch struct {
	PID    uint32
	Result uintptr
}

var windowMu sync.Mutex
var windowQuery windowSearch
var enumWindowCallback = syscall.NewCallback(func(h, l uintptr) uintptr {
	q := &windowQuery
	var p uint32
	U("GetWindowThreadProcessId", h, Ptr(&p))
	if p == q.PID && U("IsWindowVisible", h) != 0 && U("GetWindow", h, 4) == 0 {
		q.Result = h
		return 0
	}
	return 1
})

func Window(pid uint32) uintptr {
	windowMu.Lock()
	defer windowMu.Unlock()
	windowQuery = windowSearch{PID: pid}
	U("EnumWindows", enumWindowCallback, 0)
	return windowQuery.Result
}
func Alloc(n int) (uintptr, error) {
	p := K("VirtualAlloc", 0, uintptr(n), 0x3000, 0x40)
	if p == 0 {
		return 0, fmt.Errorf("VirtualAlloc failed")
	}
	return p, nil
}
