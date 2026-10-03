package snapshot

import (
	"fmt"
	"iceyquicksave/internal/mono"
	"iceyquicksave/internal/winapi"
	"strings"
)

func resolveClass(a *mono.API, qualified string) (uintptr, error) {
	t := a.Call("mono_reflection_type_from_name", winapi.Ptr(winapi.Z(qualified)), a.Images["Assembly-CSharp"])
	if t == 0 {
		return 0, fmt.Errorf("saved type unavailable: %s", qualified)
	}
	return a.Call("mono_class_from_mono_type", t), nil
}
func findField(a *mono.API, c uintptr, class, name string) (mono.Field, error) {
	for _, f := range a.Fields(c) {
		if f.Name == name && a.ClassName(f.Class) == class {
			return f, nil
		}
	}
	return mono.Field{}, fmt.Errorf("field unavailable: %s.%s", class, name)
}

// Rebind builds a graph in the new scene in two passes. No pointer from the file
// is ever trusted. Shared assets and delegates bind to the fresh scene's objects.
func (g *Graph) Rebind(a *mono.API, w *World) (*Graph, error) {
	out := NewGraph(a)
	out.World = w
	out.Nodes = make([]*Node, len(g.Nodes))
	types := map[string]uintptr{}
	for i, n := range g.Nodes {
		if n.ID != i+1 {
			return nil, fmt.Errorf("invalid node ordering")
		}
		c := types[n.TypeName]
		if c == 0 {
			var e error
			c, e = resolveClass(a, n.TypeName)
			if e != nil {
				return nil, e
			}
			types[n.TypeName] = c
		}
		cp := *n
		cp.handle = 0
		cp.klass = c
		cp.Fields = append([]Field(nil), n.Fields...)
		for j, f := range cp.Fields {
			meta, e := findField(a, c, f.Class, f.Name)
			if e != nil {
				return nil, e
			}
			cp.Fields[j].meta = meta
		}
		out.Nodes[i] = &cp
	}
	out.Roots = append([]Field(nil), g.Roots...)
	for i, f := range out.Roots {
		var c uintptr
		for _, image := range []string{"Assembly-CSharp", "Assembly-CSharp-firstpass", "UnityEngine"} {
			ns, name := "", f.Class
			if at := strings.LastIndex(name, "."); at >= 0 {
				ns, name = name[:at], name[at+1:]
			}
			c = a.Class(image, ns, name)
			if c != 0 {
				break
			}
		}
		meta, e := findField(a, c, f.Class, f.Name)
		if e != nil {
			return nil, e
		}
		out.Roots[i].meta = meta
	}
	// Resolve anchored current values before allocating or overwriting containers.
	existing := map[int]uintptr{}
	resolving := map[int]bool{}
	var current func(int) (uintptr, error)
	current = func(id int) (uintptr, error) {
		if id == 0 {
			return 0, nil
		}
		if id < 1 || id > len(out.Nodes) {
			return 0, fmt.Errorf("bad reference ID")
		}
		if o, ok := existing[id]; ok {
			return o, nil
		}
		if resolving[id] {
			return 0, fmt.Errorf("cyclic unresolved binding: %d", id)
		}
		resolving[id] = true
		defer delete(resolving, id)
		n := out.Nodes[id-1]
		var obj uintptr
		var e error
		if n.Ref != nil {
			obj, e = w.Resolve(n.Ref)
		} else if n.Anchor != nil {
			an := n.Anchor
			if an.Static {
				for _, f := range out.Roots {
					if f.Class == an.Class && f.Name == an.Field {
						obj = a.FieldValue(f.meta, 0)
						break
					}
				}
			} else {
				owner, err := current(an.Owner)
				if err != nil {
					return 0, err
				}
				if owner != 0 {
					if an.Index >= 0 {
						ln, err := a.ArrayLen(owner)
						if err != nil {
							return 0, err
						}
						if uintptr(an.Index) < ln {
							index := int32(an.Index)
							obj, e = a.Invoke(out.getElement, owner, winapi.Ptr(&index))
						}
					} else {
						f, err := findField(a, a.Call("mono_object_get_class", owner), an.Class, an.Field)
						if err != nil {
							return 0, err
						}
						obj = a.FieldValue(f, owner)
					}
				}
			}
		}
		if e != nil {
			return 0, e
		}
		existing[id] = obj
		return obj, nil
	}
	for _, n := range out.Nodes {
		if n.Ref != nil || n.Kind == "asset" || n.Kind == "delegate" || n.Kind == "opaque" || n.Kind == "ai-reset" {
			obj, e := current(n.ID)
			if e != nil {
				out.Release()
				return nil, fmt.Errorf("bind %s: %w", n.Class, e)
			}
			if obj == 0 && n.Kind != "opaque" && n.Kind != "delegate" {
				out.Release()
				return nil, fmt.Errorf("binding missing: %s", n.Class)
			}
			n.handle = a.Pin(obj)
		}
	}
	for _, n := range out.Nodes {
		if n.handle != 0 || n.Kind == "opaque" || n.Kind == "delegate" || n.Kind == "ai-reset" {
			continue
		}
		var obj uintptr
		switch n.Kind {
		case "string":
			obj = a.NewString(n.Text)
		case "rawarray", "array":
			elem := a.Call("mono_class_get_element_class", n.klass)
			var count uintptr
			if n.Kind == "array" {
				count = uintptr(len(n.Elements))
			} else {
				var align uint32
				n.elementSize = a.Call("mono_class_value_size", elem, winapi.Ptr(&align))
				if n.elementSize == 0 || len(n.Data)%int(n.elementSize) != 0 {
					out.Release()
					return nil, fmt.Errorf("invalid raw array size")
				}
				count = uintptr(len(n.Data)) / n.elementSize
			}
			obj = a.Call("mono_array_new", a.Domain, elem, count)
		default:
			obj = a.Call("mono_object_new", a.Domain, n.klass)
		}
		if obj == 0 {
			out.Release()
			return nil, fmt.Errorf("allocation failed: %s", n.Class)
		}
		n.handle = a.Pin(obj)
	}
	return out, nil
}

func (e *Engine) Bind(a *mono.API, graph *Graph) (*Engine, error) {
	out := NewEngine(a)
	out.Random = append([]byte(nil), e.Random...)
	for _, n := range e.Items {
		if n.Ref == nil {
			out.Release()
			return nil, fmt.Errorf("native state lacks portable identity")
		}
		o, err := graph.World.Resolve(n.Ref)
		if err != nil {
			out.Release()
			return nil, err
		}
		cp := n
		cp.handle = a.Pin(o)
		out.Items = append(out.Items, cp)
	}
	return out, nil
}
