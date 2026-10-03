package gameapi

import (
	"fmt"
	"iceyquicksave/internal/winapi"
)

// Load keeps all managed objects alive across the asynchronous scene switch.
// Close must run on the Unity thread, including on failure.
type Load struct {
	g                         *API
	Slot                      *Slot
	data, name                uint32
	oldScene                  int32
	setData, reset, loadScene uintptr
}

func (g *API) sceneHandle() (int32, error) {
	v, e := g.M.Static("UnityEngine", "UnityEngine.SceneManagement", "SceneManager", "GetActiveScene")
	if e != nil {
		return 0, e
	}
	return g.M.Int(v), nil
}

func (g *API) Ready() error {
	scene, e := g.Scene()
	if e != nil {
		return e
	}
	if scene == "" || scene == "ui_start" {
		return fmt.Errorf("请先进入游戏关卡")
	}
	gate, e := g.M.Static("Assembly-CSharp", "", "R", "get_SceneGate")
	if e != nil {
		return e
	}
	locked, e := g.Field(gate, "IsLocked")
	if e != nil {
		return e
	}
	if g.M.Bool(locked) {
		return fmt.Errorf("正在切换地图，请稍后再试")
	}
	p, e := g.StaticField("R", "Player")
	if e != nil {
		return e
	}
	goj, e := g.M.Get(p, "gameObject")
	if e != nil || !g.M.Alive(goj) {
		return fmt.Errorf("玩家尚未生成")
	}
	return nil
}

func (g *API) PrepareLoad(s *Slot) (*Load, error) {
	if e := g.Ready(); e != nil {
		return nil, e
	}
	a := g.M
	l := &Load{g: g, Slot: s}
	// Resolve the complete path before releasing the pause. In particular,
	// ChangeState has both enum and string overloads of the same arity.
	for _, method := range []struct {
		ns, class, name string
		args            []string
	}{
		{"", "SaveData", "GetObject", []string{"System.Byte[]"}},
		{"", "GameData", "LoadPlayerAttribute", []string{"PlayerAttribute&", "PlayerAttributeGameData"}},
		{"", "PlayerAction", "ChangeState", []string{"System.String", "System.Single"}},
		{"", "PlayerAction", "Reborn", nil},
		{"", "PlayerAction", "TurnRound", []string{"System.Int32"}},
		{"", "PlayerTimeController", "SetSpeed", []string{"UnityEngine.Vector2"}},
		{"GameWorld", "PlayerManager", "SetPosition", []string{"UnityEngine.Vector2"}},
		{"", "CameraController", "CameraResetPostionAfterSwitchScene", nil},
	} {
		if a.Exact(a.Class("Assembly-CSharp", method.ns, method.class), method.name, method.args...) == 0 {
			return nil, fmt.Errorf("游戏接口不可用：%s.%s", method.class, method.name)
		}
	}
	l.setData = a.Exact(a.Class("Assembly-CSharp", "", "R"), "set_GameData", "GameData")
	l.reset = a.Exact(a.Class("Assembly-CSharp", "", "R"), "DeadReset")
	l.loadScene = a.Exact(a.Class("Assembly-CSharp", "", "LevelManager"), "LoadLevelByPosition", "System.String", "UnityEngine.Vector3", "System.Boolean")
	if l.setData == 0 || l.reset == 0 || l.loadScene == 0 {
		return nil, fmt.Errorf("游戏场景加载接口不可用")
	}
	var e error
	l.oldScene, e = g.sceneHandle()
	if e != nil {
		return nil, e
	}
	data, e := g.GameDataFromJSON(s.GameData)
	if e != nil {
		return nil, e
	}
	if data == 0 {
		return nil, fmt.Errorf("游戏未能解码存档")
	}
	l.data = a.Pin(data)
	l.name = a.Pin(a.NewString(s.Scene))
	canLoad := a.Exact(a.Class("UnityEngine", "UnityEngine", "Application"), "CanStreamedLevelBeLoaded", "System.String")
	available, e := a.Invoke(canLoad, 0, a.Target(l.name))
	if e != nil || !a.Bool(available) {
		l.Close()
		if e != nil {
			return nil, fmt.Errorf("验证目标地图失败：%w", e)
		}
		return nil, fmt.Errorf("当前游戏不存在地图 %s", s.Scene)
	}
	return l, nil
}

// Start is called immediately after resume, in the same Unity-thread callback.
// The game's own coroutine must be allowed to run during the scene transition.
func (l *Load) Start() error {
	a := l.g.M
	if _, e := a.Invoke(l.setData, 0, a.Target(l.data)); e != nil {
		return e
	}
	if _, e := a.Invoke(l.reset, 0); e != nil {
		return e
	}
	pos := l.Slot.Position
	progress := byte(1)
	v, e := a.Invoke(l.loadScene, 0, a.Target(l.name), winapi.Ptr(&pos), winapi.Ptr(&progress))
	if e != nil {
		return e
	}
	if v == 0 {
		return fmt.Errorf("游戏未启动场景加载协程")
	}
	return nil
}

func (l *Load) Ready() (bool, error) {
	g, a := l.g, l.g.M
	handle, e := g.sceneHandle()
	if e != nil {
		return false, e
	}
	if handle == l.oldScene {
		return false, nil
	}
	scene, e := g.Scene()
	if e != nil {
		return false, e
	}
	if scene != l.Slot.Scene {
		return false, nil
	}
	gate, e := a.Static("Assembly-CSharp", "", "R", "get_SceneGate")
	if e != nil {
		return false, e
	}
	locked, e := g.Field(gate, "IsLocked")
	if e != nil {
		return false, e
	}
	if a.Bool(locked) {
		return false, nil
	}
	return true, g.Ready()
}

func (l *Load) ApplyPlayer() error {
	g, a, s := l.g, l.g.M, l.Slot
	p, e := g.StaticField("R", "Player")
	if e != nil {
		return e
	}
	// Clear death flags with the game's normal method before restoring saved HP.
	if _, e = a.Static("Assembly-CSharp", "", "PlayerAction", "Reborn"); e != nil {
		return e
	}
	attr, e := g.Field(p, "Attribute")
	if e != nil {
		return e
	}
	data := a.Target(l.data)
	savedAttr, e := g.Field(data, "PlayerAttributeGameData")
	if e != nil {
		return e
	}
	m := a.Exact(a.Class("Assembly-CSharp", "", "GameData"), "LoadPlayerAttribute", "PlayerAttribute&", "PlayerAttributeGameData")
	if _, e = a.Invoke(m, data, winapi.Ptr(&attr), savedAttr); e != nil {
		return e
	}
	pos := Vec2{s.Position.X, s.Position.Y}
	if _, e = g.Call(p, "SetPosition", winapi.Ptr(&pos)); e != nil {
		return e
	}
	action, e := a.Get(p, "Action")
	if e != nil {
		return e
	}
	if _, e = g.Call(action, "TurnRound", winapi.Ptr(&s.Face)); e != nil {
		return e
	}
	if s.PlayerState != "" {
		state := a.NewString(s.PlayerState)
		h := a.Pin(state)
		defer a.Free(h)
		speed := float32(1)
		m := a.Exact(a.Class("Assembly-CSharp", "", "PlayerAction"), "ChangeState", "System.String", "System.Single")
		if _, e = a.Invoke(m, action, state, winapi.Ptr(&speed)); e != nil {
			return e
		}
	}
	tc, e := a.Get(p, "TimeController")
	if e != nil {
		return e
	}
	if _, e = g.Call(tc, "SetSpeed", winapi.Ptr(&s.Velocity)); e != nil {
		return e
	}
	cam, e := g.StaticField("R", "Camera")
	if e != nil {
		return e
	}
	controller, e := a.Get(cam, "Controller")
	if e != nil {
		return e
	}
	_, e = g.Call(controller, "CameraResetPostionAfterSwitchScene")
	return e
}

func (l *Load) Close() {
	l.g.M.Free(l.data)
	l.g.M.Free(l.name)
	l.data, l.name = 0, 0
}

// Match only active scene instances with a unique nonempty ID. Prefabs and
// duplicate/missing IDs must never be silently treated as restored enemies.
func (g *API) ApplyEnemies(saved []Enemy) ([]string, error) {
	a := g.M
	objects, e := a.Find("Assembly-CSharp", "", "EnemyAttribute")
	if e != nil {
		return nil, e
	}
	byID := map[string][]uintptr{}
	counts := map[string]int{}
	for _, enemy := range saved {
		counts[enemy.ID]++
	}
	for _, o := range objects {
		if !a.Alive(o) {
			continue
		}
		goj, e := a.Get(o, "gameObject")
		if e != nil {
			return nil, e
		}
		scene, e := a.Get(goj, "scene")
		if e != nil {
			return nil, e
		}
		active, e := a.Get(goj, "activeInHierarchy")
		if e != nil {
			return nil, e
		}
		if a.Int(scene) == 0 || !a.Bool(active) {
			continue
		}
		id, e := g.Field(o, "id")
		if e != nil {
			return nil, e
		}
		byID[a.String(id)] = append(byID[a.String(id)], o)
	}
	var missing []string
	for _, want := range saved {
		matches := byID[want.ID]
		if want.ID == "" || len(matches) != 1 || counts[want.ID] != 1 {
			missing = append(missing, want.Name+"#"+want.ID)
			continue
		}
		o := matches[0]
		goj, e := a.Get(o, "gameObject")
		if e != nil {
			return missing, e
		}
		transform, e := a.Get(goj, "transform")
		if e != nil {
			return missing, e
		}
		if e = a.Set(transform, "position", winapi.Ptr(&want.Position)); e != nil {
			return missing, e
		}
		if _, e = g.Call(o, "set_currentHp", winapi.Ptr(&want.HP)); e != nil {
			return missing, e
		}
		if _, e = g.Call(o, "set_currentSp", winapi.Ptr(&want.SP)); e != nil {
			return missing, e
		}
		tc, e := g.Component(goj, "TimeController")
		if e != nil {
			return missing, e
		}
		if tc != 0 {
			if _, e = g.Call(tc, "SetSpeed", winapi.Ptr(&want.Velocity)); e != nil {
				return missing, e
			}
		}
	}
	return missing, nil
}
