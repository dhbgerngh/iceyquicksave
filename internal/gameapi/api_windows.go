// Package gameapi restores state only by calling existing game/Unity methods.
// Reading fields via Mono is permitted; field setters and raw memory writes are not.
package gameapi

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
	"math"
	"strings"
)

type API struct{ M *mono.API }
type Vec2 struct{ X, Y float32 }
type Vec3 struct{ X, Y, Z float32 }
type Slot struct {
	Format                             int
	ID, Created, Scene, AssemblySHA256 string
	GameData                           json.RawMessage
	Position                           Vec3
	Velocity                           Vec2
	PlayerHP, PlayerEnergy, Face       int32
	PlayerState                        string
	Enemies                            []Enemy
	Gaps                               []string
}

func (g *API) ByteArray(b []byte) (uintptr, error) {
	a := g.M
	c := a.Class("mscorlib", "System", "Byte")
	if c == 0 {
		return 0, fmt.Errorf("System.Byte unavailable")
	}
	arr := a.Call("mono_array_new", a.Domain, c, uintptr(len(b)))
	if arr == 0 {
		return 0, fmt.Errorf("byte array allocation failed")
	}
	if len(b) > 0 {
		p := a.Call("mono_array_addr_with_size", arr, 1, 0)
		copy(winapi.Bytes(p, len(b)), b)
	}
	return arr, nil
}

func (g *API) GameDataFromJSON(raw []byte) (uintptr, error) {
	b, e := EncodeGameBuffer(raw)
	if e != nil {
		return 0, e
	}
	arr, e := g.ByteArray(b)
	if e != nil {
		return 0, e
	}
	h := g.M.Pin(arr)
	defer g.M.Free(h)
	// Mono reference parameters are object pointers; only ref/out parameters
	// receive the address of an object pointer.
	return g.M.Static("Assembly-CSharp", "", "SaveData", "GetObject", arr)
}

func (g *API) gameDataJSON(gd uintptr) ([]byte, error) {
	a := g.M
	buffer, e := a.Static("Assembly-CSharp", "", "SaveData", "GetBuffer", gd)
	if e != nil {
		return nil, e
	}
	h := a.Pin(buffer)
	defer a.Free(h)
	n, e := a.ArrayLen(buffer)
	if e != nil {
		return nil, e
	}
	if n == 0 || n > 65536 {
		return nil, fmt.Errorf("invalid game save buffer size: %d", n)
	}
	return DecodeGameBuffer(winapi.Bytes(a.Call("mono_array_addr_with_size", buffer, 1, 0), int(n)))
}

type Enemy struct {
	Name, ID, State string
	Position        Vec3
	Velocity        Vec2
	HP, SP          int32
}

func (g *API) Field(obj uintptr, name string) (uintptr, error) {
	if obj == 0 {
		return 0, fmt.Errorf("nil object for field %s", name)
	}
	for _, f := range g.M.Fields(g.M.Call("mono_object_get_class", obj)) {
		if f.Name == name {
			return g.M.FieldValue(f, obj), nil
		}
	}
	return 0, fmt.Errorf("field %s missing", name)
}
func (g *API) StaticField(class, name string) (uintptr, error) {
	for _, f := range g.M.Fields(g.M.Class("Assembly-CSharp", "", class)) {
		if f.Name == name {
			return g.M.FieldValue(f, 0), nil
		}
	}
	return 0, fmt.Errorf("static field %s.%s missing", class, name)
}
func (g *API) Component(goj uintptr, name string) (uintptr, error) {
	a := g.M
	c := a.Class("Assembly-CSharp", "", name)
	if c == 0 {
		c = a.Class("UnityEngine", "UnityEngine", name)
	}
	if c == 0 {
		return 0, fmt.Errorf("component type missing: %s", name)
	}
	t := a.Call("mono_type_get_object", a.Domain, a.Call("mono_class_get_type", c))
	h := a.Pin(t)
	defer a.Free(h)
	return a.Invoke(a.Exact(a.Class("UnityEngine", "UnityEngine", "GameObject"), "GetComponent", "System.Type"), goj, t)
}
func (g *API) Call(obj uintptr, name string, args ...uintptr) (uintptr, error) {
	if obj == 0 {
		return 0, fmt.Errorf("nil receiver %s", name)
	}
	return g.M.Invoke(g.M.Method(g.M.Call("mono_object_get_class", obj), name, len(args)), obj, args...)
}
func (g *API) Scene() (string, error) {
	o, e := g.M.Static("Assembly-CSharp", "", "LevelManager", "get_SceneName")
	if e != nil {
		return "", e
	}
	return g.M.String(o), nil
}
func (g *API) Vector3(box uintptr) (Vec3, error) {
	b, e := g.M.Data(box, 12)
	if e != nil {
		return Vec3{}, e
	}
	return Vec3{math.Float32frombits(binary.LittleEndian.Uint32(b)), math.Float32frombits(binary.LittleEndian.Uint32(b[4:])), math.Float32frombits(binary.LittleEndian.Uint32(b[8:]))}, nil
}
func (g *API) Vector2(box uintptr) (Vec2, error) {
	b, e := g.M.Data(box, 8)
	if e != nil {
		return Vec2{}, e
	}
	return Vec2{math.Float32frombits(binary.LittleEndian.Uint32(b)), math.Float32frombits(binary.LittleEndian.Uint32(b[4:]))}, nil
}
func (g *API) GameObjectPosition(goj uintptr) (Vec3, error) {
	t, e := g.M.Get(goj, "transform")
	if e != nil {
		return Vec3{}, e
	}
	b, e := g.M.Get(t, "position")
	if e != nil {
		return Vec3{}, e
	}
	return g.Vector3(b)
}

func (g *API) Capture() (s *Slot, err error) {
	a := g.M
	scene, e := g.Scene()
	if e != nil {
		return nil, e
	}
	if scene == "ui_start" || scene == "" {
		return nil, fmt.Errorf("请先进入游戏关卡")
	}
	s = &Slot{Format: SlotFormat, Scene: scene}
	p, e := g.StaticField("R", "Player")
	if e != nil {
		return nil, e
	}
	goj, e := a.Get(p, "gameObject")
	if e != nil || !a.Alive(goj) {
		return nil, fmt.Errorf("玩家尚未生成")
	}
	s.Position, e = g.GameObjectPosition(goj)
	if e != nil {
		return nil, e
	}
	attr, e := g.Field(p, "Attribute")
	if e != nil {
		return nil, e
	}
	hp, e := a.Get(attr, "currentHP")
	if e != nil {
		return nil, e
	}
	s.PlayerHP = a.Int(hp)
	energy, e := a.Get(attr, "currentEnergy")
	if e != nil {
		return nil, e
	}
	s.PlayerEnergy = a.Int(energy)
	face, e := g.Field(attr, "faceDir")
	if e != nil {
		return nil, e
	}
	s.Face = a.Int(face)
	tc, e := a.Get(p, "TimeController")
	if e != nil {
		return nil, e
	}
	speed, e := g.Call(tc, "GetCurrentSpeed")
	if e != nil {
		return nil, e
	}
	s.Velocity, e = g.Vector2(speed)
	if e != nil {
		return nil, e
	}
	sm, e := a.Get(p, "StateMachine")
	if e != nil {
		return nil, e
	}
	st, e := a.Get(sm, "currentState")
	if e != nil {
		return nil, e
	}
	s.PlayerState = a.String(st)
	gd, e := a.Static("Assembly-CSharp", "", "R", "get_GameData")
	if e != nil {
		return nil, e
	}
	gh := a.Pin(gd)
	defer a.Free(gh)
	// Extract attributes into a clone so saving a quick slot does not mutate
	// the active checkpoint data or write the Steam save.
	raw, e := g.gameDataJSON(gd)
	if e != nil {
		return nil, e
	}
	gd, e = g.GameDataFromJSON(raw)
	if e != nil {
		return nil, e
	}
	clone := a.Pin(gd)
	defer a.Free(clone)
	if _, e = a.Invoke(a.Method(a.Class("Assembly-CSharp", "", "GameData"), "SavePlayerAttribute", 1), gd, attr); e != nil {
		return nil, e
	}
	text, e := g.gameDataJSON(gd)
	if e != nil {
		return nil, e
	}
	var data map[string]json.RawMessage
	if e = json.Unmarshal(text, &data); e != nil {
		return nil, e
	}
	data["SceneName"], _ = json.Marshal(scene)
	data["PlayerPosition"], _ = json.Marshal(map[string]float32{"x": s.Position.X, "y": s.Position.Y, "z": s.Position.Z})
	s.GameData, e = json.Marshal(data)
	if e != nil {
		return nil, e
	}
	enemies, e := a.Find("Assembly-CSharp", "", "EnemyAttribute")
	if e != nil {
		return nil, e
	}
	for _, o := range enemies {
		if !a.Alive(o) {
			continue
		}
		eg, e := a.Get(o, "gameObject")
		if e != nil {
			return nil, e
		}
		sc, e := a.Get(eg, "scene")
		if e != nil || a.Int(sc) == 0 {
			continue
		}
		active, e := a.Get(eg, "activeInHierarchy")
		if e != nil || !a.Bool(active) {
			continue
		}
		name, e := a.Get(eg, "name")
		if e != nil {
			return nil, e
		}
		enemy := Enemy{Name: a.String(name)}
		enemy.Position, e = g.GameObjectPosition(eg)
		if e != nil {
			return nil, e
		}
		id, e := g.Field(o, "id")
		if e != nil {
			return nil, e
		}
		enemy.ID = a.String(id)
		ehp, e := a.Get(o, "currentHp")
		if e != nil {
			return nil, e
		}
		enemy.HP = a.Int(ehp)
		esp, e := a.Get(o, "currentSp")
		if e != nil {
			return nil, e
		}
		enemy.SP = a.Int(esp)
		tc, e := g.Component(eg, "TimeController")
		if e != nil {
			return nil, e
		}
		if tc != 0 {
			sp, e := g.Call(tc, "GetCurrentSpeed")
			if e != nil {
				return nil, e
			}
			enemy.Velocity, e = g.Vector2(sp)
			if e != nil {
				return nil, e
			}
		}
		state, e := g.Component(eg, "StateMachine")
		if e != nil {
			return nil, e
		}
		if state != 0 {
			st, e := a.Get(state, "currentState")
			if e != nil {
				return nil, e
			}
			enemy.State = a.String(st)
		}
		s.Enemies = append(s.Enemies, enemy)
	}
	// These gaps are persisted, not hidden behind a successful file write.
	s.Gaps = []string{"恢复地图、原版进度及角色状态；战斗由游戏重新初始化，并非完整战斗回滚", "只恢复新场景中能唯一匹配的敌人属性；波次、协程、子弹及掉落物未保存完整运行状态"}
	return s, nil
}

func DecodeGameBuffer(b []byte) ([]byte, error) {
	var length uint32
	shift := uint(0)
	for i := 0; i < 5 && i < len(b); i++ {
		c := b[i]
		if i == 4 && c > 15 {
			return nil, fmt.Errorf("invalid 7-bit length")
		}
		length |= uint32(c&127) << shift
		if c&128 == 0 {
			start := i + 1
			if uint64(start)+uint64(length) > uint64(len(b)) {
				return nil, fmt.Errorf("truncated game save")
			}
			return append([]byte(nil), b[start:start+int(length)]...), nil
		}
		shift += 7
	}
	return nil, fmt.Errorf("invalid game save length")
}
func EncodeGameBuffer(text []byte) ([]byte, error) {
	if len(text) > 65520 {
		return nil, fmt.Errorf("game save too large")
	}
	n := uint32(len(text))
	b := make([]byte, 0, len(text)+5)
	for n >= 128 {
		b = append(b, byte(n)|128)
		n >>= 7
	}
	b = append(b, byte(n))
	return append(b, text...), nil
}

type Surface struct {
	Class, Method string
	Args          int
	Purpose       string
	Found         bool
}

func (g *API) Audit() []Surface {
	r := []Surface{{"SaveData", "GetBuffer", 1, "原版存档编码", false}, {"SaveData", "GetObject", 1, "原版存档解码", false}, {"GameData", "SavePlayerAttribute", 1, "提取原版支持的角色属性", false}, {"GameData", "LoadPlayerAttribute", 2, "恢复原版支持的角色属性", false}, {"R", "set_GameData", 1, "替换已解码的原版进度对象", false}, {"LevelManager", "LoadLevelByPosition", 3, "跨地图加载与定位", false}, {"EnemyGenerator", "GenerateEnemy", 4, "按预制体生成敌人", false}, {"EnemyAttribute", "set_currentHp", 1, "敌人血量", false}, {"EnemyAttribute", "set_currentSp", 1, "敌人护盾", false}, {"EnemyAttribute", "SetBasicData", 1, "初始化敌人基础数据", false}, {"TimeController", "SetSpeed", 1, "敌人速度", false}, {"PlayerTimeController", "SetSpeed", 1, "玩家速度", false}, {"PlayerTimeController", "NextPosition", 1, "玩家位置", false}, {"PlayerAction", "ChangeState", 2, "玩家动作入口", false}, {"PlayerAction", "TurnRound", 1, "角色朝向", false}, {"StateMachine", "SetState", 1, "状态机正常转换", false}, {"BattleCheckPoint", "InitGameArea", 0, "战斗区域边界", false}, {"BattleCheckPoint", "GenerateEnemyRushOneAfterOne", 2, "启动指定一波敌人，不能设置当前波次索引", false}, {"EffectController", "Generate", 6, "特效/子弹生成入口", false}, {"SaveDebugGUI", "LoadGame", 0, "开发用读档，仅原版存档范围", false}}
	for i := range r {
		ns, name := "", r[i].Class
		if p := strings.LastIndex(name, "."); p >= 0 {
			ns, name = name[:p], name[p+1:]
		}
		r[i].Found = g.M.Method(g.M.Class("Assembly-CSharp", ns, name), r[i].Method, r[i].Args) != 0
	}
	return r
}
