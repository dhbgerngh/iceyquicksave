package agent

import (
	"fmt"
	"iceyquicksave/internal/snapshot"
	"strings"
	"unsafe"
)

type observedCoroutine struct {
	handle  uint32
	at      float32
	current uintptr
}
type coroutineState struct {
	Iterator, Owner, Child int
	Wait                   float32
	Realtime               bool
	Kind                   string
}
type resumedCoroutine struct {
	iterator, owner uint32
	at              float32
	realtime        bool
	child           uintptr
}

func (s *agent) gameTime(realtime bool) float32 {
	method := "get_time"
	if realtime {
		method = "get_realtimeSinceStartup"
	}
	v, e := s.a.Static("UnityEngine", "UnityEngine", "Time", method)
	if e != nil || v == 0 {
		return 0
	}
	return *(*float32)(unsafe.Pointer(s.a.Call("mono_object_unbox", v)))
}
func (s *agent) observeCoroutine(iterator uintptr, alive bool) {
	if iterator == 0 {
		return
	}
	if !alive {
		if old := s.coroutines[iterator]; old != nil {
			s.a.Free(old.handle)
			delete(s.coroutines, iterator)
		}
		return
	}
	c := s.a.Call("mono_object_get_class", iterator)
	name := s.a.ClassName(c)
	if !strings.Contains(name, "Iterator") && !strings.Contains(name, "d__") {
		return
	}
	old := s.coroutines[iterator]
	if old == nil {
		old = &observedCoroutine{handle: s.a.Pin(iterator)}
		s.coroutines[iterator] = old
	}
	old.at = s.gameTime(false)
	old.current = 0
	for _, f := range s.a.Fields(c) {
		if f.Name == "$current" || f.Name == "<>2__current" {
			old.current = s.a.FieldValue(f, iterator)
			break
		}
	}
}
func (s *agent) captureCoroutines(v *saved) error {
	a := s.a
	ids := map[uintptr]int{}
	for o, c := range s.coroutines {
		klass := a.Call("mono_object_get_class", o)
		var owner uintptr
		for _, f := range a.Fields(klass) {
			if f.Name == "$this" || f.Name == "<>4__this" {
				owner = a.FieldValue(f, o)
				break
			}
		}
		if owner == 0 || !a.IsUnity(a.Call("mono_object_get_class", owner)) || !a.Alive(owner) {
			continue
		}
		if !snapshot.SceneObject(a, owner) {
			continue
		}
		it, e := v.Graph.Add(o)
		if e != nil {
			return e
		}
		own, e := v.Graph.Add(owner)
		if e != nil {
			return e
		}
		st := coroutineState{Iterator: it, Owner: own, Kind: "next-frame"}
		if c.current != 0 {
			kind := a.ClassName(a.Call("mono_object_get_class", c.current))
			st.Kind = kind
			if kind == "UnityEngine.WaitForSeconds" {
				for _, f := range a.Fields(a.Call("mono_object_get_class", c.current)) {
					if f.Name == "m_Seconds" {
						value := a.FieldValue(f, c.current)
						seconds := *(*float32)(unsafe.Pointer(a.Call("mono_object_unbox", value)))
						st.Wait = max(0, seconds-(s.gameTime(false)-c.at))
						break
					}
				}
			}
			if kind == "UnityEngine.WaitForSecondsRealtime" {
				return fmt.Errorf("实时等待协程需要单独适配：%s", a.ClassName(klass))
			}
		}
		v.Coroutines = append(v.Coroutines, st)
		ids[o] = len(v.Coroutines) - 1
	}
	// Resolve nested iterator dependencies after every active iterator has an ID.
	for o, index := range ids {
		current := s.coroutines[o].current
		if child, ok := ids[current]; ok {
			v.Coroutines[index].Child = v.Coroutines[child].Iterator
		}
	}
	return nil
}
func (s *agent) clearCoroutines() {
	for _, c := range s.coroutines {
		s.a.Free(c.handle)
	}
	s.coroutines = map[uintptr]*observedCoroutine{}
	for _, c := range s.resuming {
		s.a.Free(c.iterator)
		s.a.Free(c.owner)
	}
	s.resuming = nil
}
func (s *agent) queueCoroutines(v *saved, g *snapshot.Graph) error {
	a := s.a
	now := s.gameTime(false)
	for _, st := range v.Coroutines {
		it, owner := g.Object(st.Iterator), g.Object(st.Owner)
		if it == 0 || owner == 0 {
			return fmt.Errorf("协程绑定缺失")
		}
		s.resuming = append(s.resuming, resumedCoroutine{iterator: a.Pin(it), owner: a.Pin(owner), at: now + st.Wait, realtime: st.Realtime, child: g.Object(st.Child)})
	}
	return nil
}
func (s *agent) stepCoroutines() {
	if s.paused.Load() || len(s.resuming) == 0 {
		return
	}
	a := s.a
	now := s.gameTime(false)
	pending := s.resuming
	s.resuming = nil
	for _, c := range pending {
		waitChild := false
		if c.child != 0 {
			if s.coroutines[c.child] != nil {
				waitChild = true
			}
			for _, other := range pending {
				if a.Target(other.iterator) == c.child {
					waitChild = true
					break
				}
			}
		}
		if now < c.at || waitChild {
			s.resuming = append(s.resuming, c)
			continue
		}
		owner, it := a.Target(c.owner), a.Target(c.iterator)
		if a.Alive(owner) {
			m := a.Exact(a.Class("UnityEngine", "UnityEngine", "MonoBehaviour"), "StartCoroutine", "System.Collections.IEnumerator")
			if _, e := a.Invoke(m, owner, it); e != nil {
				s.logger.Printf("resume coroutine: %v", e)
			}
		}
		a.Free(c.iterator)
		a.Free(c.owner)
	}
}
