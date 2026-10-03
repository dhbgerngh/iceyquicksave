package snapshot

import (
	"fmt"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
)

type Property struct {
	Name string
	Data []byte
}
type Native struct {
	Kind       string
	InstanceID int32
	Properties []Property
	handle     uint32
	Ref        *ObjectRef
}

func (s *Engine) Identify(w *World) error {
	var keep []Native
	for i := range s.Items {
		r, e := w.Ref(s.a.Target(s.Items[i].handle))
		if e != nil {
			return e
		}
		if r.Key == "" {
			s.a.Free(s.Items[i].handle)
			continue
		}
		s.Items[i].Ref = r
		keep = append(keep, s.Items[i])
	}
	s.Items = keep
	return nil
}

type Engine struct {
	Items  []Native
	Random []byte
	a      *mono.API
}

func SceneObject(a *mono.API, component uintptr) bool {
	goj, e := a.Get(component, "gameObject")
	if e != nil || goj == 0 {
		return false
	}
	scene, e := a.Get(goj, "scene")
	if e != nil || scene == 0 {
		return false
	}
	// Unity 5.4 Scene consists of its scene handle; zero identifies prefab assets.
	return a.Int(scene) != 0
}
func NewEngine(a *mono.API) *Engine { return &Engine{a: a} }
func (s *Engine) Capture() error {
	a := s.a
	for _, kind := range []string{"Transform", "Rigidbody2D", "Animator"} {
		objs, e := a.Find("UnityEngine", "UnityEngine", kind)
		if e != nil {
			return e
		}
		for _, o := range objs {
			if !a.Alive(o) || !SceneObject(a, o) {
				continue
			}
			id, e := a.Invoke(a.Method(a.Class("UnityEngine", "UnityEngine", "Object"), "GetInstanceID", 0), o)
			if e != nil {
				return e
			}
			n := Native{Kind: kind, InstanceID: a.Int(id), handle: a.Pin(o)}
			s.Items = append(s.Items, n)
			dst := &s.Items[len(s.Items)-1]
			var props []struct {
				name string
				size int
			}
			switch kind {
			case "Transform":
				props = []struct {
					name string
					size int
				}{{"localPosition", 12}, {"localRotation", 16}, {"localScale", 12}}
			case "Rigidbody2D":
				props = []struct {
					name string
					size int
				}{{"position", 8}, {"rotation", 4}, {"velocity", 8}, {"angularVelocity", 4}, {"isKinematic", 1}, {"gravityScale", 4}}
			case "Animator": // Animation transitions and native state-machine internals cannot be serialized by this API.
				props = []struct {
					name string
					size int
				}{{"speed", 4}}
			}
			for _, p := range props {
				v, e := a.Get(o, p.name)
				if e != nil {
					return e
				}
				b, e := a.Data(v, p.size)
				if e != nil {
					return e
				}
				dst.Properties = append(dst.Properties, Property{p.name, b})
			}
		}
	}
	r, e := a.Static("UnityEngine", "UnityEngine", "Random", "get_state")
	if e == nil && r != 0 {
		c := a.Call("mono_object_get_class", r)
		var align uint32
		size := a.Call("mono_class_value_size", c, winapi.Ptr(&align))
		if size <= 1024 {
			s.Random, e = a.Data(r, int(size))
		}
	}
	return e
}
func (s *Engine) Validate() error {
	for _, n := range s.Items {
		if !s.a.Alive(s.a.Target(n.handle)) {
			return fmt.Errorf("场景对象已销毁：%s #%d", n.Kind, n.InstanceID)
		}
	}
	// Refuse changed native object topology before writing anything.
	for _, kind := range []string{"Transform", "Rigidbody2D", "Animator"} {
		objs, e := s.a.Find("UnityEngine", "UnityEngine", kind)
		if e != nil {
			return e
		}
		count := 0
		for _, o := range objs {
			if s.a.Alive(o) && SceneObject(s.a, o) {
				count++
			}
		}
		want := 0
		for _, n := range s.Items {
			if n.Kind == kind {
				want++
			}
		}
		if count != want {
			return fmt.Errorf("场景对象数量已变化（%s %d→%d），拒绝部分恢复", kind, want, count)
		}
	}
	return nil
}
func (s *Engine) Restore() error {
	for _, n := range s.Items {
		for _, p := range n.Properties {
			if e := s.a.Set(s.a.Target(n.handle), p.Name, winapi.Ptr(&p.Data[0])); e != nil {
				return e
			}
		}
	}
	if len(s.Random) > 0 {
		_, e := s.a.Static("UnityEngine", "UnityEngine", "Random", "set_state", winapi.Ptr(&s.Random[0]))
		return e
	}
	return nil
}
func (s *Engine) Release() {
	for _, n := range s.Items {
		s.a.Free(n.handle)
	}
}
