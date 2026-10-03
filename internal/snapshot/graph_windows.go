package snapshot

import (
	"fmt"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
	"strings"
	"time"
)

type Field struct {
	Class string
	Name  string
	Value int
	meta  mono.Field
}
type Anchor struct {
	Owner        int
	Class, Field string
	Index        int
	Static       bool
}
type Node struct {
	ID          int
	Class       string
	Kind        string
	Data        []byte     `json:",omitempty"`
	TypeName    string     `json:",omitempty"`
	Ref         *ObjectRef `json:",omitempty"`
	Anchor      *Anchor    `json:",omitempty"`
	Text        string     `json:",omitempty"`
	Fields      []Field    `json:",omitempty"`
	Elements    []int      `json:",omitempty"`
	handle      uint32
	klass       uintptr
	cursor      int
	elementSize uintptr
}
type Graph struct {
	Nodes                  []*Node
	Roots                  []Field
	a                      *mono.API
	seen                   map[uintptr]int
	next                   int
	getElement, setElement uintptr
	blittable              map[uintptr]bool
	dataBytes              int
	World                  *World `json:"-"`
	typeNames              map[uintptr]string
}

func NewGraph(a *mono.API) *Graph {
	arr := a.Class("mscorlib", "System", "Array")
	return &Graph{a: a, seen: map[uintptr]int{}, blittable: map[uintptr]bool{}, typeNames: map[uintptr]string{}, getElement: a.Exact(arr, "GetValue", "System.Int32"), setElement: a.Exact(arr, "SetValue", "System.Object", "System.Int32")}
}
func (g *Graph) Add(obj uintptr) (int, error) {
	if obj == 0 {
		return 0, nil
	}
	if n, ok := g.seen[obj]; ok {
		return n, nil
	}
	if len(g.Nodes) >= 300000 {
		return 0, fmt.Errorf("object graph exceeds 300000 objects")
	}
	c := g.a.Call("mono_object_get_class", obj)
	id := len(g.Nodes) + 1
	n := &Node{ID: id, Class: g.a.ClassName(c), handle: g.a.Pin(obj), klass: c}
	g.Nodes = append(g.Nodes, n)
	g.seen[obj] = id
	return id, nil
}
func (g *Graph) Object(id int) uintptr {
	if id <= 0 || id > len(g.Nodes) {
		return 0
	}
	return g.a.Target(g.Nodes[id-1].handle)
}
func (g *Graph) Statics(c uintptr) error {
	for _, f := range g.a.Fields(c) {
		if f.Flags&0x10 == 0 || f.Flags&0x40 != 0 {
			continue
		}
		if e := g.supported(f); e != nil {
			return e
		}
		v := g.a.FieldValue(f, 0)
		id, e := g.Add(v)
		if e != nil {
			return e
		}
		g.Roots = append(g.Roots, Field{Class: g.a.ClassName(f.Class), Name: f.Name, Value: id, meta: f})
		g.anchor(id, &Anchor{Class: g.a.ClassName(f.Class), Field: f.Name, Static: true, Index: -1})
	}
	return nil
}

// Step keeps the main thread available for rendering between bounded chunks.
func (g *Graph) Step(budget time.Duration) (bool, error) {
	until := time.Now().Add(budget)
	for g.next < len(g.Nodes) {
		n := g.Nodes[g.next]
		if n.Kind == "" {
			if e := g.capture(n); e != nil {
				return false, fmt.Errorf("%s: %w", n.Class, e)
			}
		}
		if n.Kind == "array" {
			for n.cursor < len(n.Elements) {
				index := int32(n.cursor)
				v, e := g.a.Invoke(g.getElement, g.Object(n.ID), winapi.Ptr(&index))
				if e != nil {
					return false, e
				}
				id, e := g.Add(v)
				if e != nil {
					return false, e
				}
				n.Elements[n.cursor] = id
				g.anchor(id, &Anchor{Owner: n.ID, Index: n.cursor})
				n.cursor++
				if time.Now().After(until) {
					return false, nil
				}
			}
		}
		g.next++
		if time.Now().After(until) {
			return false, nil
		}
	}
	return true, nil
}
func (g *Graph) capture(n *Node) error {
	a := g.a
	o := a.Target(n.handle)
	c := n.klass
	if tn, ok := g.typeNames[c]; ok {
		n.TypeName = tn
	} else {
		rt := a.Call("mono_type_get_object", a.Domain, a.Call("mono_class_get_type", c))
		rh := a.Pin(rt)
		boxed, e := a.Get(rt, "AssemblyQualifiedName")
		if e != nil {
			a.Free(rh)
			return e
		}
		n.TypeName = a.String(boxed)
		a.Free(rh)
		g.typeNames[c] = n.TypeName
	}
	if a.IsUnity(c) && g.World != nil {
		var e error
		n.Ref, e = g.World.Ref(o)
		if e != nil {
			return e
		}
	}
	for k := c; k != 0; k = a.Call("mono_class_get_parent", k) {
		if a.ClassName(k) == "System.MulticastDelegate" {
			n.Kind = "delegate"
			return nil
		}
	}
	if strings.HasPrefix(n.Class, "BehaviorDesigner.") {
		n.Kind = "ai-reset"
		return nil
	}
	t := a.Call("mono_type_get_type", a.Call("mono_class_get_type", c))
	if n.Class == "System.String" {
		n.Kind = "string"
		n.Text = a.String(o)
		return nil
	}
	// Spine definitions are shared loaded animation assets, not live track state.
	// Rewinding live Skeleton/AnimationState/TrackEntry is distinct from copying
	// every immutable curve and attachment in every loaded enemy prefab.
	if strings.HasPrefix(n.Class, "Spine.") && (strings.HasSuffix(n.Class, "Timeline") || strings.HasSuffix(n.Class, "Attachment") || strings.HasSuffix(n.Class, "Data") || n.Class == "Spine.Animation" || n.Class == "Spine.Skin") {
		n.Kind = "asset"
		return nil
	}
	for k := c; k != 0; k = a.Call("mono_class_get_parent", k) {
		if k == a.Class("UnityEngine", "UnityEngine", "ScriptableObject") {
			n.Kind = "asset"
			return nil
		}
	}
	// These identities belong to the runtime/OS and must never be rewound.
	if strings.HasPrefix(n.Class, "System.Reflection.") || strings.HasPrefix(n.Class, "System.RuntimeType") || strings.HasPrefix(n.Class, "System.Threading.") || strings.HasPrefix(n.Class, "System.IO.") || n.Class == "System.MonoType" || n.Class == "UnityEngine.Coroutine" || n.Class == "UnityEngine.AsyncOperation" {
		n.Kind = "opaque"
		return nil
	}
	if a.Call("mono_class_is_enum", c) != 0 || (t >= 2 && t <= 13) || t == 0x18 || t == 0x19 {
		n.Kind = "raw"
		var alignment uint32
		size := a.Call("mono_class_value_size", c, winapi.Ptr(&alignment))
		if size > 4096 {
			return fmt.Errorf("invalid scalar size")
		}
		d, e := a.Data(o, int(size))
		n.Data = d
		return e
	}
	if a.Call("mono_class_get_rank", c) != 0 {
		if a.Call("mono_class_get_rank", c) != 1 {
			return fmt.Errorf("multidimensional array is unsupported")
		}
		n.Kind = "array"
		count, e := a.ArrayLen(o)
		if e != nil {
			return e
		}
		if count > 2000000 {
			return fmt.Errorf("array too large")
		}
		elem := a.Call("mono_class_get_element_class", c)
		if g.isBlittable(elem) {
			var alignment uint32
			n.elementSize = a.Call("mono_class_value_size", elem, winapi.Ptr(&alignment))
			size := count * n.elementSize
			if size > 64<<20 || g.dataBytes+int(size) > 64<<20 {
				return fmt.Errorf("raw array budget exceeded")
			}
			g.dataBytes += int(size)
			n.Kind = "rawarray"
			if size > 0 {
				p := a.Call("mono_array_addr_with_size", o, n.elementSize, 0)
				n.Data = append([]byte(nil), winapi.Bytes(p, int(size))...)
			}
			return nil
		}
		n.Elements = make([]int, count)
		return nil
	}
	n.Kind = "object"
	if a.Call("mono_class_is_valuetype", c) != 0 {
		n.Kind = "value"
	}
	for _, f := range a.Fields(c) {
		if f.Flags&(0x10|0x40) != 0 {
			continue
		}
		// Native Unity object state is captured through properties, never m_CachedPtr.
		if a.Call("mono_class_get_image", f.Class) == a.Images["UnityEngine"] && a.IsUnity(c) {
			continue
		}
		if e := g.supported(f); e != nil {
			return e
		}
		v := a.FieldValue(f, o)
		id, e := g.Add(v)
		if e != nil {
			return e
		}
		n.Fields = append(n.Fields, Field{Class: a.ClassName(f.Class), Name: f.Name, Value: id, meta: f})
		g.anchor(id, &Anchor{Owner: n.ID, Class: a.ClassName(f.Class), Field: f.Name, Index: -1})
	}
	return nil
}
func (g *Graph) anchor(id int, a *Anchor) {
	if id > 0 && g.Nodes[id-1].Anchor == nil {
		g.Nodes[id-1].Anchor = a
	}
}

func (g *Graph) isBlittable(c uintptr) bool {
	if v, ok := g.blittable[c]; ok {
		return v
	}
	g.blittable[c] = false
	a := g.a
	if a.Call("mono_class_is_valuetype", c) == 0 {
		return false
	}
	t := a.Call("mono_type_get_type", a.Call("mono_class_get_type", c))
	if a.Call("mono_class_is_enum", c) != 0 || (t >= 2 && t <= 13) || t == 0x18 || t == 0x19 {
		g.blittable[c] = true
		return true
	}
	for _, f := range a.Fields(c) {
		if f.Flags&0x10 != 0 {
			continue
		}
		if !g.isBlittable(a.Call("mono_class_from_mono_type", f.Type)) {
			return false
		}
	}
	g.blittable[c] = true
	return true
}

func (g *Graph) supported(f mono.Field) error {
	t := g.a.Call("mono_type_get_type", f.Type)
	switch t {
	case 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 0x11, 0x12, 0x14, 0x15, 0x18, 0x19, 0x1c, 0x1d:
	default:
		return fmt.Errorf("unsupported field %s.%s (Mono type 0x%x)", g.a.ClassName(f.Class), f.Name, t)
	}
	return nil
}
func (g *Graph) Release() {
	for _, n := range g.Nodes {
		g.a.Free(n.handle)
		n.handle = 0
	}
}
func (g *Graph) Validate() error {
	if g.setElement == 0 {
		return fmt.Errorf("Array.SetValue unavailable")
	}
	for _, n := range g.Nodes {
		o := g.a.Target(n.handle)
		if o == 0 {
			if n.Kind == "opaque" || n.Kind == "delegate" {
				continue
			}
			return fmt.Errorf("retained object lost: %s", n.Class)
		}
		if g.a.IsUnity(n.klass) && !g.a.Alive(o) {
			return fmt.Errorf("对象已销毁，拒绝不完整恢复：%s", n.Class)
		}
		for _, f := range n.Fields {
			if e := g.validateField(f); e != nil {
				return e
			}
		}
	}
	for _, f := range g.Roots {
		if e := g.validateField(f); e != nil {
			return e
		}
	}
	return nil
}
func (g *Graph) validateField(f Field) error {
	a := g.a
	c := a.Call("mono_class_from_mono_type", f.meta.Type)
	if strings.HasPrefix(a.ClassName(c), "System.Nullable`") {
		fi := a.Call("mono_field_get_object", a.Domain, f.meta.Class, f.meta.Ptr)
		h := a.Pin(fi)
		defer a.Free(h)
		if fi == 0 || a.Method(a.Call("mono_object_get_class", fi), "SetValue", 2) == 0 {
			return fmt.Errorf("nullable setter unavailable: %s.%s", f.Class, f.Name)
		}
	}
	return nil
}

// Rollback roots include every original identity, even ones detached since save.
func (g *Graph) Rollback() (*Graph, error) {
	r := NewGraph(g.a)
	for _, f := range g.Roots {
		v := g.a.FieldValue(f.meta, 0)
		id, e := r.Add(v)
		if e != nil {
			r.Release()
			return nil, e
		}
		r.Roots = append(r.Roots, Field{Class: f.Class, Name: f.Name, Value: id, meta: f.meta})
	}
	for _, n := range g.Nodes {
		if _, e := r.Add(g.a.Target(n.handle)); e != nil {
			r.Release()
			return nil, e
		}
	}
	return r, nil
}
func (g *Graph) applyField(f Field, object uintptr) error {
	a := g.a
	v := g.Object(f.Value)
	typ := a.Call("mono_class_from_mono_type", f.meta.Type)
	if strings.HasPrefix(a.ClassName(typ), "System.Nullable`") {
		// Mono boxes Nullable<T> as T (or null), so unboxing it as Nullable<T>
		// would copy the wrong layout. Reflection supplies Mono's nullable conversion.
		fi := a.Call("mono_field_get_object", a.Domain, f.meta.Class, f.meta.Ptr)
		h := a.Pin(fi)
		defer a.Free(h)
		_, e := a.Invoke(a.Method(a.Call("mono_object_get_class", fi), "SetValue", 2), fi, object, v)
		if e != nil {
			return fmt.Errorf("nullable %s.%s: %w", f.Class, f.Name, e)
		}
		return nil
	}
	value := v
	if a.Call("mono_class_is_valuetype", typ) != 0 {
		if v == 0 {
			return fmt.Errorf("null value type %s", f.Name)
		}
		value = a.Call("mono_object_unbox", v)
	}
	if f.meta.Flags&0x10 != 0 {
		vt := a.Call("mono_class_vtable", a.Domain, f.meta.Class)
		a.Call("mono_field_static_set_value", vt, f.meta.Ptr, value)
	} else {
		a.Call("mono_field_set_value", object, f.meta.Ptr, value)
	}
	return nil
}
func (g *Graph) Restore() error {
	for _, n := range g.Nodes {
		if n.Kind == "raw" && len(n.Data) > 0 {
			p := g.a.Call("mono_object_unbox", g.Object(n.ID))
			copy(winapi.Bytes(p, len(n.Data)), n.Data)
		}
	}
	// Boxed value-type copies must be reconstructed before their containing field/array.
	done := map[int]bool{}
	var restoreValue func(int) error
	restoreValue = func(id int) error {
		if id == 0 || done[id] {
			return nil
		}
		n := g.Nodes[id-1]
		if n.Kind != "value" {
			return nil
		}
		done[id] = true
		for _, f := range n.Fields {
			if e := restoreValue(f.Value); e != nil {
				return e
			}
			if e := g.applyField(f, g.Object(id)); e != nil {
				return e
			}
		}
		return nil
	}
	for _, n := range g.Nodes {
		if e := restoreValue(n.ID); e != nil {
			return e
		}
	}
	for _, n := range g.Nodes {
		o := g.Object(n.ID)
		switch n.Kind {
		case "rawarray":
			if len(n.Data) > 0 {
				p := g.a.Call("mono_array_addr_with_size", o, n.elementSize, 0)
				copy(winapi.Bytes(p, len(n.Data)), n.Data)
			}
		case "object":
			for _, f := range n.Fields {
				if e := g.applyField(f, o); e != nil {
					return e
				}
			}
		case "array":
			for i, id := range n.Elements {
				index := int32(i)
				if _, e := g.a.Invoke(g.setElement, o, g.Object(id), winapi.Ptr(&index)); e != nil {
					return e
				}
			}
		}
	}
	for _, f := range g.Roots {
		if e := g.applyField(f, 0); e != nil {
			return e
		}
	}
	return nil
}
