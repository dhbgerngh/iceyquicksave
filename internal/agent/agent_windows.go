package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iceyquicksave/internal/archive"
	"iceyquicksave/internal/gameapi"
	"iceyquicksave/internal/hook"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
	"iceyquicksave/internal/winui"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

type frozen struct {
	scale             []byte
	audio, background byte
	animations        []animation
}
type animation struct {
	handle uint32
	speed  []byte
}
type pendingSave struct {
	slot    *gameapi.Slot
	dir     string
	started time.Time
	writing bool
	done    chan error
}
type pendingLoad struct {
	slot    *gameapi.Slot
	op      loadOperation
	started time.Time
}
type result struct {
	id  string
	err error
}
type agent struct {
	root                              string
	logger                            *log.Logger
	hwnd, originalWnd, originalInvoke uintptr
	a                                 *mono.API
	game                              *gameapi.API
	paused                            atomic.Bool
	ticking                           atomic.Bool
	invocations, blocked              atomic.Uint64
	frozen                            *frozen
	save                              *pendingSave
	load                              *pendingLoad
	picker                            chan result
	pickerCmd                         *exec.Cmd
	f5, f9                            bool
	lastInit, lastRequest             time.Time
	trueBox                           uintptr
	trueHandle                        uint32
	hash                              string
}

func Run() {
	exe, e := os.Executable()
	if e != nil || !strings.EqualFold(filepath.Base(exe), "ICEY.exe") {
		return
	}
	root := filepath.Dir(exe)
	dir := filepath.Join(root, "savedata")
	if os.MkdirAll(dir, 0755) != nil {
		return
	}
	f, e := os.OpenFile(filepath.Join(dir, "iceyqs.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if e != nil {
		return
	}
	s := &agent{root: root, logger: log.New(f, "", log.LstdFlags|log.Lmicroseconds)}
	s.logger.Println("game API save/load readying; pause -> picker -> resume and immediate load; battle restore is partial")
	b, e := os.ReadFile(filepath.Join(root, "ICEY_Data", "Managed", "Assembly-CSharp.dll"))
	if e != nil {
		s.logger.Print(e)
		return
	}
	sum := sha256.Sum256(b)
	s.hash = hex.EncodeToString(sum[:])
	for i := 0; i < 2400; i++ {
		s.hwnd = winapi.Window(uint32(os.Getpid()))
		if s.hwnd != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.hwnd == 0 {
		return
	}
	s.originalWnd = winapi.U("GetWindowLongPtrW", s.hwnd, ^uintptr(3))
	cb := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) (ret uintptr) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() {
			if p := recover(); p != nil {
				s.fail(fmt.Errorf("callback panic: %v", p))
			}
		}()
		if m == 0x113 && w == 0x49515953 {
			s.tick()
			return 0
		}
		if (m == 0x100 || m == 0x101) && (w == 0x74 || w == 0x78) {
			return 0
		}
		return winapi.U("CallWindowProcW", s.originalWnd, h, uintptr(m), w, l)
	})
	if winapi.U("SetWindowLongPtrW", s.hwnd, ^uintptr(3), cb) == 0 {
		return
	}
	winapi.U("SetTimer", s.hwnd, 0x49515953, 16, 0)
}
func (s *agent) initialize() error {
	a, e := mono.Open(filepath.Join(s.root, "ICEY_Data", "Mono", "mono.dll"))
	if e != nil {
		return e
	}
	if a.Call("mono_thread_current") == 0 {
		return fmt.Errorf("window thread is not the Unity Mono thread")
	}
	s.a = a
	s.game = &gameapi.API{M: a}
	yes := byte(1)
	s.trueBox = a.Call("mono_value_box", a.Domain, a.Call("mono_get_boolean_class"), winapi.Ptr(&yes))
	s.trueHandle = a.Pin(s.trueBox)
	cb := syscall.NewCallback(func(method, obj, args, ex uintptr) uintptr {
		s.invocations.Add(1)
		if s.paused.Load() {
			name := mono.CString(a.Call("mono_method_get_name", method))
			block := name == "Update" || name == "FixedUpdate" || name == "LateUpdate" || name == "OnGUI" || name == "OnApplicationFocus" || name == "OnApplicationPause" || strings.HasPrefix(name, "OnState")
			if name == "MoveNext" {
				if ex != 0 {
					*(*uintptr)(unsafe.Pointer(ex)) = 0
				}
				s.blocked.Add(1)
				return s.trueBox
			}
			if name == "InvokeMoveNext" && a.ClassName(a.Call("mono_method_get_class", method)) == "UnityEngine.SetupCoroutine" {
				if args != 0 {
					p := winapi.ReadPtr(args + 8)
					if p != 0 {
						out := winapi.ReadPtr(p)
						if out != 0 {
							*(*byte)(unsafe.Pointer(out)) = 1
						}
					}
				}
				if ex != 0 {
					*(*uintptr)(unsafe.Pointer(ex)) = 0
				}
				s.blocked.Add(1)
				return 0
			}
			if block {
				if ex != 0 {
					*(*uintptr)(unsafe.Pointer(ex)) = 0
				}
				s.blocked.Add(1)
				return 0
			}
		}
		return hook.Call(s.originalInvoke, method, obj, args, ex)
	})
	_, e = hook.Install(a.Addr("mono_runtime_invoke"), cb, func(p uintptr) { s.originalInvoke = p; a.InvokeFunc = p })
	if e != nil {
		return e
	}
	audit := s.game.Audit()
	j, _ := json.MarshalIndent(audit, "", "  ")
	os.WriteFile(filepath.Join(s.root, "savedata", "api-audit.json"), j, 0644)
	s.status("ready; game API save/load", nil)
	return nil
}
func (s *agent) tick() {
	// Window APIs can dispatch nested messages. Never reenter a transaction.
	if !s.ticking.CompareAndSwap(false, true) {
		return
	}
	defer s.ticking.Store(false)
	if s.a == nil {
		if time.Since(s.lastInit) < time.Second {
			return
		}
		s.lastInit = time.Now()
		if e := s.initialize(); e != nil {
			s.logger.Print(e)
			s.a = nil
		}
		return
	}
	focus := winapi.U("GetForegroundWindow") == s.hwnd
	k5 := winapi.U("GetAsyncKeyState", 0x74)&0x8000 != 0
	k9 := winapi.U("GetAsyncKeyState", 0x78)&0x8000 != 0
	req := ""
	if focus && k5 && !s.f5 {
		req = "save"
	}
	if focus && k9 && !s.f9 {
		req = "picker"
	}
	s.f5 = k5
	s.f9 = k9
	if time.Since(s.lastRequest) > 150*time.Millisecond {
		s.lastRequest = time.Now()
		p := filepath.Join(s.root, "savedata", "request.txt")
		if b, e := os.ReadFile(p); e == nil {
			os.Remove(p)
			req = strings.TrimSpace(string(b))
		}
	}
	switch req {
	case "save":
		if s.frozen == nil && s.load == nil && s.picker == nil {
			s.beginSave()
		}
	case "picker":
		if s.frozen == nil && s.load == nil && s.picker == nil {
			s.beginPicker()
		}
	case "cancel":
		// A scene transition already handed to the game cannot be cancelled
		// safely. Cancel only the chooser; loading continues to completion.
		if s.picker != nil {
			s.closePicker()
			if e := s.resume(); e != nil {
				s.fail(e)
			} else {
				s.status("cancelled", nil)
			}
		}
	case "probe":
		s.status("probe", nil)
	}
	if s.save != nil {
		s.pollSave()
	}
	if s.load != nil {
		s.pollLoad()
	}
	if s.picker != nil {
		select {
		case r := <-s.picker:
			s.picker = nil
			s.pickerCmd = nil
			if r.err != nil {
				s.fail(r.err)
			} else if r.id != "" {
				s.beginLoad(r.id)
			} else {
				if e := s.resume(); e != nil {
					s.fail(e)
				} else {
					s.status("cancelled", nil)
				}
			}
		default:
		}
	}
}
func (s *agent) pause() error {
	if s.frozen != nil {
		return fmt.Errorf("operation already paused")
	}
	a := s.a
	f := &frozen{}
	scale, e := a.Static("UnityEngine", "UnityEngine", "Time", "get_timeScale")
	if e != nil {
		return e
	}
	f.scale, e = a.Data(scale, 4)
	if e != nil {
		return e
	}
	v, e := a.Static("UnityEngine", "UnityEngine", "AudioListener", "get_pause")
	if e != nil {
		return e
	}
	if a.Bool(v) {
		f.audio = 1
	}
	v, e = a.Static("UnityEngine", "UnityEngine", "Application", "get_runInBackground")
	if e != nil {
		return e
	}
	if a.Bool(v) {
		f.background = 1
	}
	// Publish only after all original global values have been read. A failed
	// getter must not cause resume to overwrite an uncaptured value with zero.
	s.frozen = f
	var zero float32
	yes := byte(1)
	s.paused.Store(true)
	if _, e = a.Static("UnityEngine", "UnityEngine", "Time", "set_timeScale", winapi.Ptr(&zero)); e != nil {
		return e
	}
	if _, e = a.Static("UnityEngine", "UnityEngine", "AudioListener", "set_pause", winapi.Ptr(&yes)); e != nil {
		return e
	}
	if _, e = a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&yes)); e != nil {
		return e
	}
	objects, e := a.Find("UnityEngine", "UnityEngine", "Animator")
	if e != nil {
		return e
	}
	for _, o := range objects {
		if !a.Alive(o) {
			continue
		}
		goj, e := a.Get(o, "gameObject")
		if e != nil {
			return e
		}
		scene, e := a.Get(goj, "scene")
		if e != nil {
			return e
		}
		if a.Int(scene) == 0 {
			continue
		}
		speed, e := a.Get(o, "speed")
		if e != nil {
			return e
		}
		b, e := a.Data(speed, 4)
		if e != nil {
			return e
		}
		f.animations = append(f.animations, animation{a.Pin(o), b})
		if e = a.Set(o, "speed", winapi.Ptr(&zero)); e != nil {
			return e
		}
	}
	s.status("logic frozen; rendering active", nil)
	return nil
}
func (s *agent) resume() error {
	var errs []error
	collect := func(e error) {
		if e != nil {
			errs = append(errs, e)
		}
	}
	if f := s.frozen; f != nil {
		for _, n := range f.animations {
			o := s.a.Target(n.handle)
			if s.a.Alive(o) {
				collect(s.a.Set(o, "speed", winapi.Ptr(&n.speed[0])))
			}
			s.a.Free(n.handle)
		}
		if len(f.scale) > 0 {
			_, e := s.a.Static("UnityEngine", "UnityEngine", "Time", "set_timeScale", winapi.Ptr(&f.scale[0]))
			collect(e)
		}
		_, e := s.a.Static("UnityEngine", "UnityEngine", "AudioListener", "set_pause", winapi.Ptr(&f.audio))
		collect(e)
		_, e = s.a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&f.background))
		collect(e)
	}
	s.frozen = nil
	s.paused.Store(false)
	return errors.Join(errs...)
}
func (s *agent) closePicker() {
	if s.pickerCmd != nil && s.pickerCmd.Process != nil {
		_ = s.pickerCmd.Process.Kill()
	}
	s.picker = nil
	s.pickerCmd = nil
}
func (s *agent) fail(e error) {
	s.save = nil
	s.closePicker()
	if s.load != nil {
		s.load.op.Close()
		s.load = nil
	}
	e = errors.Join(e, s.resume())
	s.status("operation failed", e)
	go func() {
		cmd := exec.Command(filepath.Join(s.root, "ICEYQuickSave.exe"), "--error", e.Error())
		cmd.Dir = s.root
		_ = cmd.Run()
	}()
}
func (s *agent) beginSave() {
	if e := s.game.Ready(); e != nil {
		s.fail(e)
		return
	}
	if e := s.pause(); e != nil {
		s.fail(e)
		return
	}
	v, e := s.game.Capture()
	if e != nil {
		s.fail(e)
		return
	}
	v.ID = time.Now().Format("20060102-150405.000") + "-api"
	v.Created = time.Now().Format("2006-01-02 15:04:05")
	v.AssemblySHA256 = s.hash
	if e = v.Validate(v.ID, s.hash); e != nil {
		s.fail(e)
		return
	}
	dir := filepath.Join(s.root, "savedata", ".pending-"+v.ID)
	if e = os.MkdirAll(dir, 0755); e != nil {
		s.fail(e)
		return
	}
	p := s.a.NewString(filepath.Join(dir, "screen.png"))
	h := s.a.Pin(p)
	defer s.a.Free(h)
	one := int32(1)
	if _, e = s.a.Static("UnityEngine", "UnityEngine", "Application", "CaptureScreenshot", p, winapi.Ptr(&one)); e != nil {
		s.fail(e)
		return
	}
	s.save = &pendingSave{slot: v, dir: dir, started: time.Now(), done: make(chan error, 1)}
	s.status("capturing game API snapshot", nil)
}
func (s *agent) pollSave() {
	j := s.save
	if time.Since(j.started) > 30*time.Second {
		s.fail(fmt.Errorf("capture timeout"))
		return
	}
	if !j.writing {
		f, e := os.Open(filepath.Join(j.dir, "screen.png"))
		if e != nil {
			return
		}
		_, e = png.DecodeConfig(f)
		f.Close()
		if e != nil {
			return
		}
		j.writing = true
		go func() {
			e := archive.Write(filepath.Join(j.dir, "state.iceyqs"), j.slot)
			if e == nil {
				info := winui.Slot{ID: j.slot.ID, Created: j.slot.Created, Scene: j.slot.Scene, Status: gameapi.SlotStatus, Objects: len(j.slot.Enemies)}
				b, _ := json.MarshalIndent(info, "", "  ")
				e = os.WriteFile(filepath.Join(j.dir, "slot.json"), b, 0644)
			}
			if e == nil {
				e = os.Rename(j.dir, filepath.Join(s.root, "savedata", j.slot.ID))
			}
			j.done <- e
		}()
	}
	select {
	case e := <-j.done:
		s.save = nil
		e = errors.Join(e, s.resume())
		if e != nil {
			s.fail(e)
		} else {
			s.status("saved "+j.slot.ID, nil)
		}
	default:
	}
}
func (s *agent) beginPicker() {
	if e := s.pause(); e != nil {
		s.fail(e)
		return
	}
	ch := make(chan result, 1)
	s.picker = ch
	cmd := exec.Command(filepath.Join(s.root, "ICEYQuickSave.exe"), "--picker", "--root", s.root)
	cmd.Dir = s.root
	var output, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &stderr
	// Start synchronously so cancellation never races cmd.Process assignment.
	if e := cmd.Start(); e != nil {
		s.fail(e)
		return
	}
	s.pickerCmd = cmd
	go func() {
		e := cmd.Wait()
		if e != nil {
			e = fmt.Errorf("读取窗口退出失败：%w %s", e, strings.TrimSpace(stderr.String()))
		}
		ch <- result{strings.TrimSpace(output.String()), e}
	}()
	s.status("choosing; logic paused", nil)
}
func (s *agent) status(state string, e error) {
	v := map[string]any{"state": state, "at": time.Now().Format(time.RFC3339), "pid": os.Getpid(), "paused": s.paused.Load(), "loading": s.load != nil, "runtime_invocations": s.invocations.Load(), "blocked_invocations": s.blocked.Load(), "mode": "game API restore; partial battle state"}
	if e != nil {
		v["error"] = e.Error()
		s.logger.Printf("%s: %v", state, e)
	} else {
		s.logger.Print(state)
	}
	if s.a != nil && s.originalInvoke != 0 {
		if scene, err := s.game.Scene(); err == nil {
			v["scene"] = scene
		}
		for _, n := range []string{"frameCount", "time", "realtimeSinceStartup"} {
			b, e := s.a.Static("UnityEngine", "UnityEngine", "Time", "get_"+n)
			if e == nil {
				d, _ := s.a.Data(b, 4)
				if len(d) == 4 {
					if n == "frameCount" {
						v[n] = *(*int32)(unsafe.Pointer(&d[0]))
					} else {
						v[n] = *(*float32)(unsafe.Pointer(&d[0]))
					}
				}
			}
		}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	p := filepath.Join(s.root, "savedata", "status.json")
	if os.WriteFile(p+".tmp", b, 0644) == nil {
		os.Rename(p+".tmp", p)
	}
}
