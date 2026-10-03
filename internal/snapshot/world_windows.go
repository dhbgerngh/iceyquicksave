package snapshot

import (
	"fmt"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
	"sort"
	"strconv"
	"strings"
	"time"
)

// World records stable hierarchy paths, and retains inactive templates for objects
// instantiated at runtime. Scene assets are reconstructed by loading the scene.
type WorldObject struct {
	Key, Parent, Name, Prefab string
	Sibling                   int32
	Active                    bool
	Layer                     int32
	Position, Rotation, Scale []byte
	Components                []string
	handle                    uint32
	template                  uint32
}
type ObjectRef struct {
	Key, Type, Asset string
	Index            int
	Instance         int32
}
type World struct {
	Objects  []*WorldObject
	a        *mono.API
	byObject map[uintptr]*WorldObject
	byKey    map[string]*WorldObject
	assets   map[string][]uintptr
	holder   uint32
}

func NewWorld(a *mono.API) *World {
	return &World{a: a, byObject: map[uintptr]*WorldObject{}, byKey: map[string]*WorldObject{}, assets: map[string][]uintptr{}}
}
func (a *World) componentType(c uintptr) string { return a.a.ClassName(c) }
func (w *World) components(goj uintptr) ([]uintptr, error) {
	a := w.a
	t := a.Call("mono_type_get_object", a.Domain, a.Call("mono_class_get_type", a.Class("UnityEngine", "UnityEngine", "Component")))
	h := a.Pin(t)
	defer a.Free(h)
	m := a.Exact(a.Class("UnityEngine", "UnityEngine", "GameObject"), "GetComponents", "System.Type")
	arr, e := a.Invoke(m, goj, t)
	if e != nil {
		return nil, e
	}
	ah := a.Pin(arr)
	defer a.Free(ah)
	n, e := a.ArrayLen(arr)
	if e != nil {
		return nil, e
	}
	r := make([]uintptr, n)
	for i := range r {
		r[i] = winapi.ReadPtr(a.Call("mono_array_addr_with_size", arr, 8, uintptr(i)))
	}
	return r, nil
}
func (w *World) Capture() error {
	a := w.a
	all, e := a.Find("UnityEngine", "UnityEngine", "Transform")
	if e != nil {
		return e
	}
	temps := map[uintptr]uintptr{}
	counts := map[string]int{}
	for _, t := range all {
		if !a.Alive(t) || !SceneObject(a, t) {
			continue
		}
		goj, e := a.Get(t, "gameObject")
		if e != nil {
			return e
		}
		name, e := a.Get(goj, "name")
		if e != nil {
			return e
		}
		if strings.HasPrefix(a.String(name), "__ICEYQS_") {
			continue
		}
		temps[t] = goj
	}
	var visit func(uintptr) (*WorldObject, error)
	visit = func(t uintptr) (*WorldObject, error) {
		goj := temps[t]
		if goj == 0 {
			return nil, fmt.Errorf("parent not present in captured scene")
		}
		if v := w.byObject[goj]; v != nil {
			return v, nil
		}
		p, e := a.Get(t, "parent")
		if e != nil {
			return nil, e
		}
		parent := ""
		if p != 0 && a.Alive(p) {
			if temps[p] == 0 {
				return nil, nil
			}
			pn, e := visit(p)
			if e != nil {
				return nil, e
			}
			if pn == nil {
				return nil, nil
			}
			parent = pn.Key
		}
		name, _ := a.Get(goj, "name")
		n := a.String(name)
		if strings.HasPrefix(n, "__ICEYQS_") {
			return nil, nil
		}
		index, e := a.Invoke(a.Method(a.Class("UnityEngine", "UnityEngine", "Transform"), "GetSiblingIndex", 0), t)
		if e != nil {
			return nil, e
		}
		stem := parent + "/" + strings.ReplaceAll(strings.ReplaceAll(n, "%", "%25"), "/", "%2F")
		ordinal := counts[stem]
		counts[stem]++
		key := stem + "#" + strconv.Itoa(ordinal)
		obj := &WorldObject{Key: key, Parent: parent, Name: n, Sibling: a.Int(index), handle: a.Pin(goj)}
		w.Objects = append(w.Objects, obj)
		w.byObject[goj] = obj
		w.byKey[key] = obj
		active, e := a.Get(goj, "activeSelf")
		if e != nil {
			return nil, e
		}
		obj.Active = a.Bool(active)
		layer, e := a.Get(goj, "layer")
		if e != nil {
			return nil, e
		}
		obj.Layer = a.Int(layer)
		for _, p := range []struct {
			name string
			size int
			dst  *[]byte
		}{{"localPosition", 12, &obj.Position}, {"localRotation", 16, &obj.Rotation}, {"localScale", 12, &obj.Scale}} {
			v, e := a.Get(t, p.name)
			if e != nil {
				return nil, e
			}
			*p.dst, e = a.Data(v, p.size)
			if e != nil {
				return nil, e
			}
		}
		comps, e := w.components(goj)
		if e != nil {
			return nil, e
		}
		for _, c := range comps {
			if c != 0 {
				obj.Components = append(obj.Components, a.ClassName(a.Call("mono_object_get_class", c)))
			}
		}
		return obj, nil
	}
	// Sort by native sibling order, so path ordinals are stable within each parent.
	sort.SliceStable(all, func(i, j int) bool {
		mi := a.Method(a.Class("UnityEngine", "UnityEngine", "Transform"), "GetSiblingIndex", 0)
		x, _ := a.Invoke(mi, all[i])
		y, _ := a.Invoke(mi, all[j])
		return a.Int(x) < a.Int(y)
	})
	for _, t := range all {
		if temps[t] != 0 {
			if _, e = visit(t); e != nil {
				return e
			}
		}
	}
	return nil
}
func (w *World) Ref(obj uintptr) (*ObjectRef, error) {
	a := w.a
	c := a.Call("mono_object_get_class", obj)
	kind := a.ClassName(c)
	id, e := a.Invoke(a.Method(a.Class("UnityEngine", "UnityEngine", "Object"), "GetInstanceID", 0), obj)
	if e != nil {
		return nil, e
	}
	r := &ObjectRef{Type: kind, Instance: a.Int(id)}
	goj := obj
	if kind != "UnityEngine.GameObject" {
		goj, e = a.Get(obj, "gameObject")
		if e != nil {
			goj = 0
		}
	}
	if info := w.byObject[goj]; info != nil {
		r.Key = info.Key
		if kind != "UnityEngine.GameObject" {
			comps, e := w.components(goj)
			if e != nil {
				return nil, e
			}
			for _, o := range comps {
				if o == 0 {
					continue
				}
				if a.Call("mono_object_get_class", o) == c {
					if o == obj {
						return r, nil
					}
					r.Index++
				}
			}
			return nil, fmt.Errorf("component identity missing: %s", kind)
		}
		return r, nil
	}
	n, e := a.Get(obj, "name")
	if e != nil {
		return nil, e
	}
	r.Asset = a.String(n)
	return r, nil
}
func (w *World) Resolve(r *ObjectRef) (uintptr, error) {
	a := w.a
	if r.Key != "" {
		n := w.byKey[r.Key]
		if n == nil {
			return 0, fmt.Errorf("scene object missing: %s", r.Key)
		}
		goj := a.Target(n.handle)
		if r.Type == "UnityEngine.GameObject" {
			return goj, nil
		}
		cs, e := w.components(goj)
		if e != nil {
			return 0, e
		}
		idx := 0
		for _, c := range cs {
			if c != 0 && a.ClassName(a.Call("mono_object_get_class", c)) == r.Type {
				if idx == r.Index {
					return c, nil
				}
				idx++
			}
		}
		return 0, fmt.Errorf("component missing: %s %s[%d]", r.Key, r.Type, r.Index)
	}
	if len(w.assets) == 0 {
		all, e := a.Find("UnityEngine", "UnityEngine", "Object")
		if e != nil {
			return 0, e
		}
		for _, o := range all {
			if o == 0 || !a.Alive(o) {
				continue
			}
			n, e := a.Get(o, "name")
			if e != nil {
				continue
			}
			key := a.ClassName(a.Call("mono_object_get_class", o)) + "|" + a.String(n)
			w.assets[key] = append(w.assets[key], o)
		}
	}
	list := w.assets[r.Type+"|"+r.Asset]
	for _, o := range list {
		id, e := a.Invoke(a.Method(a.Class("UnityEngine", "UnityEngine", "Object"), "GetInstanceID", 0), o)
		if e == nil && a.Int(id) == r.Instance {
			return o, nil
		}
	}
	if len(list) == 1 {
		return list[0], nil
	}
	return 0, fmt.Errorf("asset cannot be resolved unambiguously: %s %q (%d matches)", r.Type, r.Asset, len(list))
}
func (w *World) ApplyTransforms() error {
	for _, n := range w.Objects {
		obj := w.a.Target(n.handle)
		if obj == 0 || !w.a.Alive(obj) {
			return fmt.Errorf("missing object %s", n.Key)
		}
		t, e := w.a.Get(obj, "transform")
		if e != nil {
			return e
		}
		for _, p := range []struct {
			name string
			b    []byte
		}{{"localPosition", n.Position}, {"localRotation", n.Rotation}, {"localScale", n.Scale}} {
			if len(p.b) > 0 {
				if e = w.a.Set(t, p.name, winapi.Ptr(&p.b[0])); e != nil {
					return e
				}
			}
		}
		if e = w.a.Set(obj, "layer", winapi.Ptr(&n.Layer)); e != nil {
			return e
		}
	}
	for _, n := range w.Objects {
		v := byte(0)
		if n.Active {
			v = 1
		}
		if _, e := w.a.Invoke(w.a.Method(w.a.Class("UnityEngine", "UnityEngine", "GameObject"), "SetActive", 1), w.a.Target(n.handle), winapi.Ptr(&v)); e != nil {
			return e
		}
	}
	return nil
}
func (w *World) Release() {
	for _, n := range w.Objects {
		w.a.Free(n.handle)
		w.a.Free(n.template)
	}
	if w.holder != 0 {
		o := w.a.Target(w.holder)
		if w.a.Alive(o) {
			w.a.Static("UnityEngine", "UnityEngine", "Object", "Destroy", o)
		}
		w.a.Free(w.holder)
	}
}

// Reconcile maps the saved hierarchy to a freshly loaded scene. Missing roots are
// recreated from matching loaded prefabs. Unknown or ambiguous templates fail.
func (w *World) Reconcile(a *mono.API) (*World, error) {
	current := NewWorld(a)
	if e := current.Capture(); e != nil {
		return nil, e
	}
	out := NewWorld(a)
	all, e := a.Find("UnityEngine", "UnityEngine", "GameObject")
	if e != nil {
		current.Release()
		return nil, e
	}
	prefab := map[string][]uintptr{}
	for _, o := range all {
		scene, e := a.Get(o, "scene")
		if e != nil || a.Int(scene) != 0 {
			continue
		}
		n, e := a.Get(o, "name")
		if e == nil {
			prefab[a.String(n)] = append(prefab[a.String(n)], o)
		}
	}
	for _, saved := range w.Objects {
		n := current.byKey[saved.Key]
		var obj uintptr
		if n != nil {
			obj = a.Target(n.handle)
		} else {
			candidates := prefab[strings.TrimSuffix(saved.Name, "(Clone)")]
			if len(candidates) != 1 {
				out.Release()
				current.Release()
				return nil, fmt.Errorf("无法重建对象 %s：预制体匹配 %d 个", saved.Key, len(candidates))
			}
			method := a.Exact(a.Class("UnityEngine", "UnityEngine", "Object"), "Instantiate", "UnityEngine.Object")
			obj, e = a.Invoke(method, 0, candidates[0])
			if e != nil {
				out.Release()
				current.Release()
				return nil, e
			}
			name := a.NewString(saved.Name)
			nh := a.Pin(name)
			e = a.Set(obj, "name", name)
			a.Free(nh)
			if e != nil {
				return nil, e
			}
			if saved.Parent != "" {
				pn := out.byKey[saved.Parent]
				if pn == nil {
					return nil, fmt.Errorf("missing reconstructed parent")
				}
				p, _ := a.Get(a.Target(pn.handle), "transform")
				t, _ := a.Get(obj, "transform")
				if e = a.Set(t, "parent", p); e != nil {
					return nil, e
				}
			}
			// Refresh the subtree after Instantiate; this discovers its child components.
			current.Release()
			current = NewWorld(a)
			if e = current.Capture(); e != nil {
				return nil, e
			}
		}
		cp := *saved
		cp.handle = a.Pin(obj)
		cp.template = 0
		out.Objects = append(out.Objects, &cp)
		out.byKey[cp.Key] = &cp
		out.byObject[obj] = &cp
	}
	// Objects spawned since the save are removed through Unity's lifecycle APIs.
	for _, n := range current.Objects {
		if out.byKey[n.Key] == nil {
			if n.Parent != "" && out.byKey[n.Parent] == nil {
				continue
			}
			a.Static("UnityEngine", "UnityEngine", "Object", "Destroy", a.Target(n.handle))
		}
	}
	current.Release()
	return out, nil
}

var _ = time.Second
