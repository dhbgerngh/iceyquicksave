package winui

import (
	"encoding/json"
	"fmt"
	"iceyquicksave/internal/winapi"
	"image"
	"image/draw"
	_ "image/png"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"syscall"
	"unsafe"
)

type Slot struct {
	ID, Session, Created, Scene, Status string
	Objects                             int
}

func (s Slot) loadable() bool {
	return s.Status == "game-api" || s.Status == "api-diagnostic-not-loadable"
}

type WndClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClsExtra, WndExtra                 int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	IconSmall                          uintptr
}
type Paint struct {
	DC                 uintptr
	Erase              int32
	Rect               winapi.Rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}
type BitmapInfo struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	Used, Important        uint32
}

func ReadSlots(root string) []Slot {
	paths, _ := filepath.Glob(filepath.Join(root, "savedata", "*", "slot.json"))
	var slots []Slot
	for _, p := range paths {
		b, e := os.ReadFile(p)
		if e != nil || len(b) > 65536 {
			continue
		}
		var s Slot
		if json.Unmarshal(b, &s) == nil && s.ID == filepath.Base(filepath.Dir(p)) {
			slots = append(slots, s)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].ID > slots[j].ID })
	return slots
}

// Picker owns a separate OS process and a native Win32 window/message loop.
func Picker(root, session string) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	winapi.U("SetProcessDPIAware")
	slots := ReadSlots(root)
	var hwnd, list, loadButton uintptr
	selected := ""
	var pixels []byte
	var iw, ih int32
	loadPreview := func(index int) {
		pixels = nil
		if loadButton != 0 {
			var enabled uintptr
			if index >= 0 && index < len(slots) && slots[index].loadable() {
				enabled = 1
			}
			winapi.U("EnableWindow", loadButton, enabled)
		}
		if index < 0 || index >= len(slots) {
			return
		}
		f, e := os.Open(filepath.Join(root, "savedata", slots[index].ID, "screen.png"))
		if e != nil {
			return
		}
		defer f.Close()
		im, _, e := image.Decode(f)
		if e != nil {
			return
		}
		b := im.Bounds()
		if b.Dx() > 8192 || b.Dy() > 8192 {
			return
		}
		rgba := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		draw.Draw(rgba, rgba.Bounds(), im, b.Min, draw.Src)
		pixels = rgba.Pix
		for i := 0; i < len(pixels); i += 4 {
			pixels[i], pixels[i+2] = pixels[i+2], pixels[i]
		}
		iw = int32(b.Dx())
		ih = int32(b.Dy())
	}
	accept := func() {
		idx := int(int32(winapi.U("SendMessageW", list, 0x188, 0, 0)))
		if idx < 0 || idx >= len(slots) {
			return
		}
		s := slots[idx]
		if !s.loadable() {
			return
		}
		selected = s.ID
		winapi.U("DestroyWindow", hwnd)
	}
	proc := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) uintptr {
		switch m {
		case 0x111:
			id, code := w&0xffff, (w>>16)&0xffff
			if id == 1001 && code == 1 {
				loadPreview(int(int32(winapi.U("SendMessageW", list, 0x188, 0, 0))))
				winapi.U("InvalidateRect", h, 0, 1)
			}
			if id == 1002 || (id == 1001 && code == 2) {
				accept()
			}
			if id == 1003 {
				winapi.U("DestroyWindow", h)
			}
			return 0
		case 0x100:
			if w == 0x1b {
				winapi.U("DestroyWindow", h)
			}
			return 0
		case 0xf:
			var ps Paint
			dc := winapi.U("BeginPaint", h, winapi.Ptr(&ps))
			if len(pixels) > 0 {
				bw, bh := int32(620), int32(349)
				if float64(iw)/float64(ih) > float64(bw)/float64(bh) {
					bh = int32(float64(bw) * float64(ih) / float64(iw))
				} else {
					bw = int32(float64(bh) * float64(iw) / float64(ih))
				}
				bi := BitmapInfo{Size: 40, Width: iw, Height: -ih, Planes: 1, BitCount: 32}
				winapi.G("SetStretchBltMode", dc, 4)
				winapi.G("StretchDIBits", dc, 360, 70, uintptr(bw), uintptr(bh), 0, 0, uintptr(iw), uintptr(ih), winapi.Ptr(&pixels[0]), winapi.Ptr(&bi), 0, 0xcc0020)
			}
			winapi.U("EndPaint", h, winapi.Ptr(&ps))
			return 0
		case 0x10:
			winapi.U("DestroyWindow", h)
			return 0
		case 2:
			winapi.U("PostQuitMessage", 0)
			return 0
		}
		return winapi.U("DefWindowProcW", h, uintptr(m), w, l)
	})
	inst := winapi.K("GetModuleHandleW", 0)
	name := winapi.UTF("ICEYQuickSavePicker")
	wc := WndClass{Size: uint32(unsafe.Sizeof(WndClass{})), Proc: proc, Instance: inst, Cursor: winapi.U("LoadCursorW", 0, 32512), Background: 6, Name: name}
	if winapi.U("RegisterClassExW", winapi.Ptr(&wc)) == 0 {
		return "", fmt.Errorf("RegisterClassExW failed")
	}
	hwnd = winapi.U("CreateWindowExW", 0, winapi.Ptr(name), winapi.Ptr(winapi.UTF("ICEY 读取存档")), 0x00ca0000, 100, 100, 1020, 560, 0, 0, inst, 0)
	if hwnd == 0 {
		return "", fmt.Errorf("CreateWindowExW failed")
	}
	child := func(class, text string, style uintptr, x, y, w, h int, id uintptr) uintptr {
		return winapi.U("CreateWindowExW", 0, winapi.Ptr(winapi.UTF(class)), winapi.Ptr(winapi.UTF(text)), 0x50000000|style, uintptr(x), uintptr(y), uintptr(w), uintptr(h), hwnd, id, inst, 0)
	}
	child("STATIC", "游戏逻辑已冻结。关闭窗口可取消并继续游戏。", 0, 20, 16, 950, 24, 0)
	child("STATIC", "确认后恢复游戏并立即读档。恢复地图、进度和角色；战斗会重新初始化，非完整战斗回滚。", 0, 20, 40, 980, 24, 0)
	list = child("LISTBOX", "", 0x00a10001, 20, 70, 320, 360, 1001)
	loadButton = child("BUTTON", "读取所选存档", 0x10001, 360, 455, 190, 36, 1002)
	child("BUTTON", "取消 / 继续游戏", 0x10000, 570, 455, 190, 36, 1003)
	for _, s := range slots {
		suffix := ""
		if !s.loadable() {
			suffix = " [旧版不兼容，请重新保存]"
		} else if session != "" && s.Session != "" && s.Session != session {
			suffix = " [其他会话]"
		}
		label := s.Created + "  " + s.Scene + suffix
		winapi.U("SendMessageW", list, 0x180, 0, winapi.Ptr(winapi.UTF(label)))
	}
	if len(slots) > 0 {
		winapi.U("SendMessageW", list, 0x186, 0, 0)
		loadPreview(0)
	} else {
		winapi.U("EnableWindow", loadButton, 0)
		child("STATIC", "还没有快照。返回游戏后按 F5。", 0, 360, 100, 550, 30, 0)
	}
	// The first ShowWindow may obey a parent's STARTUPINFO SW_HIDE (older
	// agents launched this GUI helper with HideWindow). A second explicit
	// show honors SW_SHOWNORMAL and also works with those installed agents.
	winapi.U("ShowWindow", hwnd, 5)
	winapi.U("ShowWindow", hwnd, 1)
	winapi.U("UpdateWindow", hwnd)
	winapi.U("SetForegroundWindow", hwnd)
	var msg winapi.Msg
	for {
		r := int32(winapi.U("GetMessageW", winapi.Ptr(&msg), 0, 0, 0))
		if r == 0 {
			break
		}
		if r == -1 {
			return "", fmt.Errorf("GetMessageW failed")
		}
		if msg.Message == 0x100 && msg.Wparam == 0x1b {
			winapi.U("DestroyWindow", hwnd)
			continue
		}
		if msg.Message == 0x100 && msg.Wparam == 0x0d && msg.Hwnd == list {
			accept()
			continue
		}
		if winapi.U("IsDialogMessageW", hwnd, winapi.Ptr(&msg)) == 0 {
			winapi.U("TranslateMessage", winapi.Ptr(&msg))
			winapi.U("DispatchMessageW", winapi.Ptr(&msg))
		}
	}
	return selected, nil
}
