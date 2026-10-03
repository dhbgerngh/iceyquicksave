package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iceyquicksave/internal/archive"
	"iceyquicksave/internal/hook"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/snapshot"
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

type frozenAnimator struct {
	Handle uint32
	Speed  []byte
}
type freezeState struct {
	Scale             []byte
	Audio, Background byte
	Animators         []frozenAnimator
}
type saved struct {
	Format                      int
	Session, ID, Scene, Created string
	Coverage                    string
	Graph                       *snapshot.Graph
	Engine                      *snapshot.Engine
	World                       *snapshot.World
	Scale                       []byte
	animators                   map[uintptr][]byte
}
type captureJob struct {
	Save    *saved
	Dir     string
	Started time.Time
	stage   int
}
type loadJob struct {
	target, rollback *saved
	stage            int
	started          time.Time
}
type pickerResult struct {
	id  string
	err error
}
type agent struct {
	root, session  string
	logger         *log.Logger
	hwnd, oldWnd   uintptr
	a              *mono.API
	initError      string
	lastInit       time.Time
	paused         atomic.Bool
	skipped        atomic.Uint64
	invoked        atomic.Uint64
	originalInvoke uintptr
	trueBox        uintptr
	trueHandle     uint32
	freeze         *freezeState
	job            *captureJob
	loading        *loadJob
	cross          *crossJob
	picker         chan pickerResult
	pickerCmd      *exec.Cmd
	retained       map[string]*saved
	order          []string
	lastRequest    time.Time
	f5, f9         bool
	busy           bool
	message        string
}

func Run() {
	rootExe, e := os.Executable()
	if e != nil {
		return
	}
	root := filepath.Dir(rootExe)
	if !strings.EqualFold(filepath.Base(rootExe), "ICEY.exe") {
		return
	}
	dir := filepath.Join(root, "savedata")
	if e = os.MkdirAll(dir, 0755); e != nil {
		return
	}
	f, e := os.OpenFile(filepath.Join(dir, "iceyqs.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if e != nil {
		return
	}
	token := make([]byte, 16)
	if _, e = rand.Read(token); e != nil {
		f.Close()
		return
	}
	s := &agent{root: root, session: hex.EncodeToString(token), logger: log.New(f, "", log.LstdFlags|log.Lmicroseconds), retained: map[string]*saved{}}
	s.logger.Println("Go proxy loaded; experimental same-session snapshots; no complete-state guarantee")
	pid := uint32(os.Getpid())
	for i := 0; i < 2400; i++ {
		s.hwnd = winapi.Window(pid)
		if s.hwnd != 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if s.hwnd == 0 {
		s.logger.Println("game window not found")
		return
	}
	s.oldWnd = winapi.U("GetWindowLongPtrW", s.hwnd, ^uintptr(3))
	callback := syscall.NewCallback(func(h uintptr, m uint32, w, l uintptr) (ret uintptr) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer func() {
			if p := recover(); p != nil {
				s.logger.Printf("callback panic: %v", p)
				s.abort(fmt.Errorf("internal error: %v", p))
			}
		}()
		if m == 0x113 && w == 0x49515953 {
			s.tick()
			return 0
		}
		if (m == 0x100 || m == 0x101) && (w == 0x74 || w == 0x78) {
			return 0
		}
		return winapi.U("CallWindowProcW", s.oldWnd, h, uintptr(m), w, l)
	})
	if winapi.U("SetWindowLongPtrW", s.hwnd, ^uintptr(3), callback) == 0 {
		s.logger.Println("window subclass failed")
		return
	}
	if winapi.U("SetTimer", s.hwnd, 0x49515953, 16, 0) == 0 {
		s.logger.Println("SetTimer failed")
		return
	}
	s.logger.Println("window dispatcher installed")
}

func (s *agent) init() error {
	a, e := mono.Open(filepath.Join(s.root, "ICEY_Data", "Mono", "mono.dll"))
	if e != nil {
		return e
	}
	s.a = a
	if a.Call("mono_thread_current") == 0 {
		return fmt.Errorf("Unity window thread is not attached to Mono; refusing to invoke Unity APIs")
	}
	one := byte(1)
	s.trueBox = a.Call("mono_value_box", a.Domain, a.Call("mono_get_boolean_class"), winapi.Ptr(&one))
	s.trueHandle = a.Pin(s.trueBox)
	cb := syscall.NewCallback(func(method, obj, args, exception uintptr) uintptr {
		s.invoked.Add(1)
		if s.paused.Load() {
			name := mono.CString(a.Call("mono_method_get_name", method))
			blocked := name == "Update" || name == "LateUpdate" || name == "FixedUpdate" || name == "OnGUI" || name == "OnApplicationFocus" || name == "OnApplicationPause" || strings.HasPrefix(name, "OnState")
			if name == "MoveNext" {
				s.skipped.Add(1)
				if exception != 0 {
					*(*uintptr)(unsafe.Pointer(exception)) = 0
				}
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
				if exception != 0 {
					*(*uintptr)(unsafe.Pointer(exception)) = 0
				}
				s.skipped.Add(1)
				return 0
			}
			if blocked {
				if exception != 0 {
					*(*uintptr)(unsafe.Pointer(exception)) = 0
				}
				s.skipped.Add(1)
				return 0
			}
		}
		return hook.Call(s.originalInvoke, method, obj, args, exception)
	})
	original, e := hook.Install(a.Addr("mono_runtime_invoke"), cb, func(p uintptr) { s.originalInvoke = p; a.InvokeFunc = p })
	if e != nil {
		return e
	}
	s.originalInvoke = original
	a.InvokeFunc = original
	s.logger.Printf("Mono ready; domain=%x; runtime_invoke trampoline installed", a.Domain)
	s.status("ready", nil)
	return nil
}
func (s *agent) tick() {
	if s.a == nil || s.originalInvoke == 0 {
		if time.Since(s.lastInit) < time.Second {
			return
		}
		s.lastInit = time.Now()
		if e := s.init(); e != nil {
			if e.Error() != s.initError {
				s.initError = e.Error()
				s.logger.Printf("initialization: %v", e)
			}
			s.a = nil
		}
		return
	}
	fg := winapi.U("GetForegroundWindow") == s.hwnd
	key5 := winapi.U("GetAsyncKeyState", 0x74)&0x8000 != 0
	key9 := winapi.U("GetAsyncKeyState", 0x78)&0x8000 != 0
	request := ""
	if fg && key5 && !s.f5 {
		request = "save"
	}
	if fg && key9 && !s.f9 {
		request = "picker"
	}
	s.f5 = key5
	s.f9 = key9
	if time.Since(s.lastRequest) > 150*time.Millisecond {
		s.lastRequest = time.Now()
		path := filepath.Join(s.root, "savedata", "request.txt")
		if b, e := os.ReadFile(path); e == nil {
			os.Remove(path)
			request = strings.TrimSpace(string(b))
		}
	}
	switch request {
	case "save":
		if !s.busy {
			s.beginSave()
		}
	case "picker":
		if !s.busy {
			s.beginPicker()
		}
	case "cancel":
		if s.busy {
			s.abort(fmt.Errorf("operation cancelled"))
		}
	case "probe":
		s.status("probe", nil)
	case "debug-screenshot":
		s.debugScreenshot()
	case "debug-continue":
		s.debugContinue()
	}
	if s.job != nil {
		s.stepSave()
	}
	if s.loading != nil {
		s.stepLoad()
	}
	if s.cross != nil {
		s.stepCross()
	}
	if s.picker != nil {
		select {
		case r := <-s.picker:
			s.picker = nil
			s.pickerCmd = nil
			if r.err != nil {
				s.abort(r.err)
			} else if r.id == "" {
				s.resume()
				s.status("cancelled", nil)
			} else {
				s.load(r.id)
			}
		default:
		}
	}
}
func (s *agent) scene() (string, error) {
	o, e := s.a.Static("Assembly-CSharp", "", "LevelManager", "get_SceneName")
	if e != nil {
		return "", e
	}
	name := s.a.String(o)
	if name == "" || name == "ui_start" {
		return "", fmt.Errorf("请先进入可操作的游戏关卡")
	}
	return name, nil
}
func (s *agent) pause() error {
	if s.freeze != nil {
		return fmt.Errorf("already frozen")
	}
	a := s.a
	f := &freezeState{}
	s.freeze = f
	s.busy = true
	scale, e := a.Static("UnityEngine", "UnityEngine", "Time", "get_timeScale")
	if e != nil {
		return e
	}
	f.Scale, e = a.Data(scale, 4)
	if e != nil {
		return e
	}
	audio, e := a.Static("UnityEngine", "UnityEngine", "AudioListener", "get_pause")
	if e != nil {
		return e
	}
	if a.Bool(audio) {
		f.Audio = 1
	}
	bg, e := a.Static("UnityEngine", "UnityEngine", "Application", "get_runInBackground")
	if e != nil {
		return e
	}
	if a.Bool(bg) {
		f.Background = 1
	}
	var zero float32
	one := byte(1)
	s.paused.Store(true)
	if _, e = a.Static("UnityEngine", "UnityEngine", "Time", "set_timeScale", winapi.Ptr(&zero)); e != nil {
		return e
	}
	if _, e = a.Static("UnityEngine", "UnityEngine", "AudioListener", "set_pause", winapi.Ptr(&one)); e != nil {
		return e
	}
	if _, e = a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&one)); e != nil {
		return e
	}
	anims, e := a.Find("UnityEngine", "UnityEngine", "Animator")
	if e != nil {
		return e
	}
	for _, o := range anims {
		if !a.Alive(o) || !snapshot.SceneObject(a, o) {
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
		f.Animators = append(f.Animators, frozenAnimator{a.Pin(o), b})
		if e = a.Set(o, "speed", winapi.Ptr(&zero)); e != nil {
			return e
		}
	}
	s.status("frozen", nil)
	return nil
}
func (s *agent) resume() {
	f := s.freeze
	if f != nil {
		for _, n := range f.Animators {
			o := s.a.Target(n.Handle)
			if s.a.Alive(o) && len(n.Speed) > 0 {
				if e := s.a.Set(o, "speed", winapi.Ptr(&n.Speed[0])); e != nil {
					s.logger.Printf("restore animator speed: %v", e)
				}
			}
			s.a.Free(n.Handle)
		}
		if len(f.Scale) > 0 {
			s.a.Static("UnityEngine", "UnityEngine", "Time", "set_timeScale", winapi.Ptr(&f.Scale[0]))
		}
		s.a.Static("UnityEngine", "UnityEngine", "AudioListener", "set_pause", winapi.Ptr(&f.Audio))
		s.a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&f.Background))
	}
	s.freeze = nil
	s.paused.Store(false)
	s.busy = false
}
func (s *agent) abort(e error) {
	if s.loading != nil {
		s.loading.rollback.Graph.Release()
		s.loading.rollback.Engine.Release()
		s.loading = nil
	}
	if s.job != nil {
		s.job.Save.Graph.Release()
		s.job.Save.Engine.Release()
		if s.job.Save.World != nil {
			s.job.Save.World.Release()
		}
		s.job = nil
	}
	if s.pickerCmd != nil && s.pickerCmd.Process != nil {
		s.pickerCmd.Process.Kill()
	}
	s.pickerCmd = nil
	s.picker = nil
	s.resume()
	s.status("error", e)
	// A separate process owns notification UI; never block the Unity thread with MessageBox.
	go func() {
		cmd := exec.Command(filepath.Join(s.root, "ICEYQuickSave.exe"), "--error", e.Error())
		cmd.Dir = s.root
		_ = cmd.Run()
	}()
}
func (s *agent) beginSave() {
	scene, e := s.scene()
	if e != nil {
		s.status("rejected", e)
		return
	}
	if e = s.pause(); e != nil {
		s.abort(e)
		return
	}
	id := time.Now().Format("20060102-150405.000") + "-" + s.session[:6]
	dir := filepath.Join(s.root, "savedata", ".pending-"+id)
	if e = os.MkdirAll(dir, 0755); e != nil {
		s.abort(e)
		return
	}
	save := &saved{Format: 2, Session: s.session, ID: id, Scene: scene, Created: time.Now().Format("2006-01-02 15:04:05"), Coverage: "portable scene graph; monster AI reset; experimental", Graph: snapshot.NewGraph(s.a), Engine: snapshot.NewEngine(s.a), World: snapshot.NewWorld(s.a), Scale: append([]byte(nil), s.freeze.Scale...), animators: map[uintptr][]byte{}}
	for _, n := range s.freeze.Animators {
		save.animators[s.a.Target(n.Handle)] = append([]byte(nil), n.Speed...)
	}
	s.job = &captureJob{Save: save, Dir: dir, Started: time.Now()}
	path := s.a.NewString(filepath.Join(dir, "screen.png"))
	ph := s.a.Pin(path)
	defer s.a.Free(ph)
	supersize := int32(1)
	if _, e = s.a.Static("UnityEngine", "UnityEngine", "Application", "CaptureScreenshot", path, winapi.Ptr(&supersize)); e != nil {
		s.abort(e)
		return
	}
	s.status("capturing", nil)
}
func (s *agent) stepSave() {
	j := s.job
	if time.Since(j.Started) > 45*time.Second {
		s.abort(fmt.Errorf("快照超时"))
		return
	}
	switch j.stage {
	case 0:
		if e := s.captureRoots(j.Save); e != nil {
			s.abort(e)
			return
		}
		j.stage = 1
	case 1:
		done, e := j.Save.Graph.Step(5 * time.Millisecond)
		if e != nil {
			s.abort(e)
			return
		}
		if done {
			j.stage = 2
		}
	case 2:
		f, e := os.Open(filepath.Join(j.Dir, "screen.png"))
		if e != nil {
			return
		}
		_, e = png.DecodeConfig(f)
		f.Close()
		if e != nil {
			return
		}
		j.stage = 3
		// Expensive compression and disk IO happen outside Unity's main thread.
		go func() {
			err := archive.Write(filepath.Join(j.Dir, "state.iceyqs"), j.Save)
			if err == nil {
				slot := winui.Slot{ID: j.Save.ID, Session: s.session, Created: j.Save.Created, Scene: j.Save.Scene, Status: "experimental", Objects: len(j.Save.Graph.Nodes)}
				b, _ := json.MarshalIndent(slot, "", "  ")
				err = os.WriteFile(filepath.Join(j.Dir, "slot.json"), b, 0644)
			}
			if err == nil {
				err = os.Rename(j.Dir, filepath.Join(s.root, "savedata", j.Save.ID))
			}
			jWriteResult.Store(j.Save.ID, err)
		}()
	case 3:
		result, ok := jWriteResult.LoadAndDelete(j.Save.ID)
		if !ok {
			return
		}
		if result.err != nil {
			s.abort(result.err)
			return
		}
		s.retained[j.Save.ID] = j.Save
		s.order = append(s.order, j.Save.ID)
		for len(s.order) > 8 {
			id := s.order[0]
			s.order = s.order[1:]
			old := s.retained[id]
			old.Graph.Release()
			old.Engine.Release()
			if old.World != nil {
				old.World.Release()
			}
			delete(s.retained, id)
		}
		s.job = nil
		s.resume()
		s.status("saved "+j.Save.ID, nil)
	}
}

func (s *agent) beginPicker() {
	if e := s.pause(); e != nil {
		s.abort(e)
		return
	}
	s.picker = make(chan pickerResult, 1)
	cmd := exec.Command(filepath.Join(s.root, "ICEYQuickSave.exe"), "--picker", "--root", s.root, "--session", s.session)
	cmd.Dir = s.root
	s.pickerCmd = cmd
	ch := s.picker
	go func() { b, e := cmd.Output(); ch <- pickerResult{strings.TrimSpace(string(b)), e} }()
	s.status("choosing", nil)
}
func (s *agent) load(id string) {
	s.startCross(id)
}

func (s *agent) loadRetained(id string) {
	save := s.retained[id]
	if save == nil {
		s.abort(fmt.Errorf("该快照不在当前会话的最近 8 个保留快照中"))
		return
	}
	scene, e := s.scene()
	if e != nil || scene != save.Scene {
		s.abort(fmt.Errorf("跨场景恢复尚未实现"))
		return
	}
	var disk saved
	if e = archive.Read(filepath.Join(s.root, "savedata", id, "state.iceyqs"), &disk); e != nil {
		s.abort(e)
		return
	}
	if disk.Session != s.session || disk.ID != id {
		s.abort(fmt.Errorf("snapshot session mismatch"))
		return
	}
	if e = save.Graph.Validate(); e != nil {
		s.abort(e)
		return
	}
	if e = save.Engine.Validate(); e != nil {
		s.abort(e)
		return
	}
	g, e := save.Graph.Rollback()
	if e != nil {
		s.abort(e)
		return
	}
	rollback := &saved{Graph: g, Engine: snapshot.NewEngine(s.a)}
	s.loading = &loadJob{target: save, rollback: rollback, started: time.Now()}
	if e = rollback.Engine.Capture(); e != nil {
		s.abort(e)
		return
	}
	s.status("validating restore", nil)
}

func (s *agent) stepLoad() {
	j := s.loading
	if time.Since(j.started) > 45*time.Second {
		s.abort(fmt.Errorf("读档回滚快照超时"))
		return
	}
	done, e := j.rollback.Graph.Step(5 * time.Millisecond)
	if e != nil {
		s.abort(e)
		return
	}
	if !done {
		return
	}
	if e = j.rollback.Graph.Validate(); e != nil {
		s.abort(e)
		return
	}
	// Use retained metadata exclusively; serialized files contain no raw addresses.
	e = j.target.Graph.Restore()
	if e == nil {
		e = j.target.Engine.Restore()
	}
	if e != nil {
		re := j.rollback.Graph.Restore()
		ne := j.rollback.Engine.Restore()
		if re != nil || ne != nil {
			s.logger.Printf("rollback failed: managed=%v native=%v", re, ne)
			s.status("rollback failed; logic remains frozen", fmt.Errorf("restore=%v; rollback=%v/%v", e, re, ne))
			s.loading = nil
			return
		}
		s.abort(fmt.Errorf("读档失败，已回滚：%w", e))
		return
	}
	save := j.target
	s.freeze.Scale = append([]byte(nil), save.Scale...)
	for i := range s.freeze.Animators {
		n := &s.freeze.Animators[i]
		if b := save.animators[s.a.Target(n.Handle)]; b != nil {
			n.Speed = append([]byte(nil), b...)
		}
	}
	j.rollback.Graph.Release()
	j.rollback.Engine.Release()
	s.loading = nil
	s.resume()
	s.status("loaded "+save.ID, nil)
}

func (s *agent) status(state string, e error) {
	v := map[string]any{"state": state, "session": s.session, "pid": os.Getpid(), "paused": s.paused.Load(), "runtime_invocations": s.invoked.Load(), "blocked_invocations": s.skipped.Load(), "at": time.Now().Format(time.RFC3339), "coverage": "experimental; not a complete game-state restore"}
	if e != nil {
		v["error"] = e.Error()
		s.logger.Printf("%s: %v", state, e)
	} else {
		s.logger.Println(state)
	}
	if s.a != nil && s.originalInvoke != 0 {
		for _, p := range []string{"frameCount", "time", "realtimeSinceStartup"} {
			if box, e := s.a.Static("UnityEngine", "UnityEngine", "Time", "get_"+p); e == nil {
				data, _ := s.a.Data(box, 4)
				if len(data) == 4 {
					if p == "frameCount" {
						v[p] = *(*int32)(unsafe.Pointer(&data[0]))
					} else {
						v[p] = *(*float32)(unsafe.Pointer(&data[0]))
					}
				}
			}
		}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	path := filepath.Join(s.root, "savedata", "status.json")
	temp := path + ".tmp"
	if os.WriteFile(temp, b, 0644) == nil {
		os.Rename(temp, path)
	}
}

func (s *agent) debugScreenshot() {
	one := byte(1)
	s.a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&one))
	p := s.a.NewString(filepath.Join(s.root, "savedata", "probe.png"))
	h := s.a.Pin(p)
	defer s.a.Free(h)
	scale := int32(1)
	_, e := s.a.Static("UnityEngine", "UnityEngine", "Application", "CaptureScreenshot", p, winapi.Ptr(&scale))
	s.status("debug screenshot requested", e)
}
func (s *agent) debugContinue() {
	if s.busy {
		return
	}
	a := s.a
	scene, e := a.Static("Assembly-CSharp", "", "LevelManager", "get_SceneName")
	if e != nil || a.String(scene) != "ui_start" {
		s.status("debug continue refused: not at title", e)
		return
	}
	exists, e := a.Static("Assembly-CSharp", "", "SaveManager", "get_IsAutoSaveDataExists")
	if e != nil || !a.Bool(exists) {
		s.status("debug continue refused: no existing save", e)
		return
	}
	objs, e := a.Find("Assembly-CSharp", "", "UIStartController")
	if e != nil || len(objs) != 1 {
		s.status("debug continue: controller unavailable", e)
		return
	}
	one := byte(1)
	a.Static("UnityEngine", "UnityEngine", "Application", "set_runInBackground", winapi.Ptr(&one))
	_, e = a.Invoke(a.Method(a.Class("Assembly-CSharp", "", "UIStartController"), "OnStartClick", 0), objs[0])
	s.status("debug continue invoked", e)
}
