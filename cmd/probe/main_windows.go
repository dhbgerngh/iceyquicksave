// Local development probe: capture the game's client area or send one test key.
package main

import (
	"flag"
	"fmt"
	"iceyquicksave/internal/winapi"
	"image"
	"image/png"
	"os"
	"time"
)

type bitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, ImageSize uint32
	X, Y                   int32
	Used, Important        uint32
}

func main() {
	pid := flag.Uint("pid", 0, "process ID")
	out := flag.String("capture", "", "PNG output")
	key := flag.Uint("key", 0, "virtual key code")
	duration := flag.Duration("duration", 120*time.Millisecond, "key duration")
	foreground := flag.Bool("foreground", false, "restore and focus window")
	button := flag.Uint("button", 0, "click a native dialog control ID")
	flag.Parse()
	winapi.U("SetProcessDPIAware")
	h := winapi.Window(uint32(*pid))
	if h == 0 {
		panic("window not found")
	}
	fmt.Printf("hwnd=%x\n", h)
	if *button != 0 {
		control := winapi.U("GetDlgItem", h, uintptr(*button))
		if control == 0 {
			panic("dialog control not found")
		}
		winapi.U("SendMessageW", control, 0xf5, 0, 0)
	}
	if *foreground || *key != 0 {
		winapi.U("ShowWindow", h, 9)
		winapi.U("SetForegroundWindow", h)
		time.Sleep(150 * time.Millisecond)
	}
	if *key != 0 {
		winapi.U("keybd_event", uintptr(*key), 0, 0, 0)
		time.Sleep(*duration)
		winapi.U("keybd_event", uintptr(*key), 0, 2, 0)
	}
	if *out != "" {
		var r winapi.Rect
		winapi.U("GetClientRect", h, winapi.Ptr(&r))
		var origin winapi.Point
		winapi.U("ClientToScreen", h, winapi.Ptr(&origin))
		w, ht := int(r.Right), int(r.Bottom)
		if w <= 0 || ht <= 0 {
			panic("empty client")
		}
		dc := winapi.U("GetDC", 0)
		defer winapi.U("ReleaseDC", 0, dc)
		mem := winapi.G("CreateCompatibleDC", dc)
		defer winapi.G("DeleteDC", mem)
		bi := bitmapInfo{Size: 40, Width: int32(w), Height: -int32(ht), Planes: 1, BitCount: 32}
		var bits uintptr
		bmp := winapi.G("CreateDIBSection", dc, winapi.Ptr(&bi), 0, winapi.Ptr(&bits), 0, 0)
		if bmp == 0 {
			panic("CreateDIBSection")
		}
		defer winapi.G("DeleteObject", bmp)
		old := winapi.G("SelectObject", mem, bmp)
		defer winapi.G("SelectObject", mem, old)
		if winapi.G("BitBlt", mem, 0, 0, uintptr(w), uintptr(ht), dc, uintptr(origin.X), uintptr(origin.Y), 0x40cc0020) == 0 {
			panic("BitBlt")
		}
		im := image.NewRGBA(image.Rect(0, 0, w, ht))
		src := winapi.Bytes(bits, w*ht*4)
		for i := 0; i < len(src); i += 4 {
			im.Pix[i] = src[i+2]
			im.Pix[i+1] = src[i+1]
			im.Pix[i+2] = src[i]
			im.Pix[i+3] = 255
		}
		f, e := os.Create(*out)
		if e != nil {
			panic(e)
		}
		defer f.Close()
		if e = png.Encode(f, im); e != nil {
			panic(e)
		}
	}
}
