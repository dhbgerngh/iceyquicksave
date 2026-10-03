// Package mono uses only the game's exported Mono embedding API and Unity methods.
package mono

import (
	"fmt"
	"iceyquicksave/internal/winapi"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

type API struct {
	DLL        *syscall.DLL
	Domain     uintptr
	procs      map[string]*syscall.Proc
	Images     map[string]uintptr
	methods    map[string]uintptr
	classes    map[string]uintptr
	InvokeFunc uintptr
}

func Open(path string) (*API, error) {
	d, e := syscall.LoadDLL(path)
	if e != nil {
		return nil, e
	}
	a := &API{DLL: d, procs: map[string]*syscall.Proc{}, Images: map[string]uintptr{}, methods: map[string]uintptr{}, classes: map[string]uintptr{}}
	names := strings.Fields(`mono_get_root_domain mono_domain_get mono_thread_current mono_thread_attach mono_image_loaded mono_class_from_name mono_class_get_method_from_name mono_runtime_invoke mono_object_unbox mono_string_to_utf8 mono_object_get_class mono_class_get_name mono_class_get_namespace mono_class_get_parent mono_class_get_image mono_image_get_name mono_class_get_fields mono_field_get_name mono_field_get_flags mono_field_get_type mono_field_get_value_object mono_field_set_value mono_field_static_set_value mono_class_vtable mono_type_get_type mono_class_from_mono_type mono_class_is_valuetype mono_class_is_enum mono_class_value_size mono_gchandle_new mono_gchandle_get_target mono_gchandle_free mono_class_get_type mono_type_get_object mono_array_addr_with_size mono_class_get_element_class mono_class_get_rank mono_string_new mono_method_get_name mono_method_get_class mono_class_get_methods mono_method_signature mono_signature_get_param_count mono_signature_get_params mono_type_get_name g_free mono_get_boolean_class mono_value_box`)
	for _, n := range names {
		p, e := d.FindProc(n)
		if e != nil {
			return nil, fmt.Errorf("Mono API %s: %w", n, e)
		}
		a.procs[n] = p
	}
	for _, n := range []string{"mono_field_get_object", "mono_object_new", "mono_array_new", "mono_reflection_type_from_name", "mono_object_get_virtual_method"} {
		p, e := d.FindProc(n)
		if e != nil {
			return nil, e
		}
		a.procs[n] = p
	}
	a.Domain = a.Call("mono_get_root_domain")
	if a.Domain == 0 {
		return nil, fmt.Errorf("Mono domain not ready")
	}
	for _, n := range []string{"Assembly-CSharp", "Assembly-CSharp-firstpass", "UnityEngine", "mscorlib", "BehaviorDesignerRuntime", "UnityEngine.UI", "DOTween"} {
		im := a.Call("mono_image_loaded", winapi.Ptr(winapi.Z(n)))
		if im == 0 {
			im = a.Call("mono_image_loaded", winapi.Ptr(winapi.Z(n+".dll")))
		}
		a.Images[n] = im
	}
	if a.Images["Assembly-CSharp"] == 0 || a.Images["UnityEngine"] == 0 {
		return nil, fmt.Errorf("game assemblies not ready")
	}
	a.InvokeFunc = a.procs["mono_runtime_invoke"].Addr()
	return a, nil
}
func (a *API) Call(n string, args ...uintptr) uintptr { r, _, _ := a.procs[n].Call(args...); return r }
func (a *API) Addr(n string) uintptr                  { return a.procs[n].Addr() }
func CString(p uintptr) string {
	if p == 0 {
		return ""
	}
	b := make([]byte, 0, 64)
	for i := 0; i < 8192; i++ {
		v := *(*byte)(unsafe.Pointer(p + uintptr(i)))
		if v == 0 {
			break
		}
		b = append(b, v)
	}
	return string(b)
}
func (a *API) String(p uintptr) string {
	if p == 0 {
		return ""
	}
	c := a.Call("mono_string_to_utf8", p)
	s := CString(c)
	a.Call("g_free", c)
	return s
}
func (a *API) NewString(s string) uintptr {
	return a.Call("mono_string_new", a.Domain, winapi.Ptr(winapi.Z(s)))
}
func (a *API) Class(image, ns, name string) uintptr {
	k := image + ":" + ns + ":" + name
	if p := a.classes[k]; p != 0 {
		return p
	}
	p := a.Call("mono_class_from_name", a.Images[image], winapi.Ptr(winapi.Z(ns)), winapi.Ptr(winapi.Z(name)))
	a.classes[k] = p
	return p
}
func (a *API) ClassName(c uintptr) string {
	ns := CString(a.Call("mono_class_get_namespace", c))
	n := CString(a.Call("mono_class_get_name", c))
	if ns != "" {
		return ns + "." + n
	}
	return n
}
func (a *API) Method(c uintptr, name string, n int) uintptr {
	if c == 0 {
		return 0
	}
	k := fmt.Sprintf("%x/%s/%d", c, name, n)
	if p := a.methods[k]; p != 0 {
		return p
	}
	var p uintptr
	for at := c; at != 0; at = a.Call("mono_class_get_parent", at) {
		p = a.Call("mono_class_get_method_from_name", at, winapi.Ptr(winapi.Z(name)), uintptr(n))
		if p != 0 {
			break
		}
	}
	a.methods[k] = p
	return p
}

// Exact selects overloads by their complete parameter type names.
func (a *API) Exact(c uintptr, name string, types ...string) uintptr {
	var it uintptr
	for {
		m := a.Call("mono_class_get_methods", c, winapi.Ptr(&it))
		if m == 0 {
			break
		}
		if CString(a.Call("mono_method_get_name", m)) != name {
			continue
		}
		sig := a.Call("mono_method_signature", m)
		if int(a.Call("mono_signature_get_param_count", sig)) != len(types) {
			continue
		}
		var pit uintptr
		ok := true
		for _, t := range types {
			p := a.Call("mono_signature_get_params", sig, winapi.Ptr(&pit))
			s := a.Call("mono_type_get_name", p)
			got := CString(s)
			a.Call("g_free", s)
			if got != t {
				ok = false
			}
		}
		if ok {
			return m
		}
	}
	return 0
}
func (a *API) Invoke(m, obj uintptr, args ...uintptr) (uintptr, error) {
	if m == 0 {
		return 0, fmt.Errorf("managed method not found")
	}
	var ex, ap uintptr
	if len(args) > 0 {
		ap = winapi.Ptr(&args[0])
	}
	r, _, _ := syscall.SyscallN(a.InvokeFunc, m, obj, ap, winapi.Ptr(&ex))
	runtime.KeepAlive(args)
	if ex != 0 {
		return 0, fmt.Errorf("managed exception: %s", a.ClassName(a.Call("mono_object_get_class", ex)))
	}
	return r, nil
}
func (a *API) Get(obj uintptr, property string) (uintptr, error) {
	if obj == 0 {
		return 0, fmt.Errorf("nil receiver: %s", property)
	}
	return a.Invoke(a.Method(a.Call("mono_object_get_class", obj), "get_"+property, 0), obj)
}
func (a *API) Set(obj uintptr, property string, value uintptr) error {
	if obj == 0 {
		return fmt.Errorf("nil receiver: %s", property)
	}
	_, e := a.Invoke(a.Method(a.Call("mono_object_get_class", obj), "set_"+property, 1), obj, value)
	return e
}
func (a *API) Static(image, ns, class, method string, args ...uintptr) (uintptr, error) {
	return a.Invoke(a.Method(a.Class(image, ns, class), method, len(args)), 0, args...)
}
func (a *API) Data(box uintptr, n int) ([]byte, error) {
	if box == 0 {
		return nil, fmt.Errorf("null boxed value")
	}
	p := a.Call("mono_object_unbox", box)
	return append([]byte(nil), winapi.Bytes(p, n)...), nil
}
func (a *API) Int(box uintptr) int32 {
	if box == 0 {
		return 0
	}
	return *(*int32)(unsafe.Pointer(a.Call("mono_object_unbox", box)))
}
func (a *API) Bool(box uintptr) bool {
	if box == 0 {
		return false
	}
	return *(*byte)(unsafe.Pointer(a.Call("mono_object_unbox", box))) != 0
}
func (a *API) Pin(obj uintptr) uint32 {
	if obj == 0 {
		return 0
	}
	return uint32(a.Call("mono_gchandle_new", obj, 1))
}
func (a *API) Target(h uint32) uintptr {
	if h == 0 {
		return 0
	}
	return a.Call("mono_gchandle_get_target", uintptr(h))
}
func (a *API) Free(h uint32) {
	if h != 0 {
		a.Call("mono_gchandle_free", uintptr(h))
	}
}
func (a *API) Find(image, ns, name string) ([]uintptr, error) {
	c := a.Class(image, ns, name)
	if c == 0 {
		return nil, fmt.Errorf("class %s missing", name)
	}
	t := a.Call("mono_type_get_object", a.Domain, a.Call("mono_class_get_type", c))
	h := a.Pin(t)
	defer a.Free(h)
	arr, e := a.Static("UnityEngine", "UnityEngine", "Resources", "FindObjectsOfTypeAll", t)
	if e != nil {
		return nil, e
	}
	if arr == 0 {
		return nil, nil
	}
	ah := a.Pin(arr)
	defer a.Free(ah)
	n, e := a.ArrayLen(arr)
	if e != nil {
		return nil, e
	}
	if n > 500000 {
		return nil, fmt.Errorf("excessive objects: %d", n)
	}
	out := make([]uintptr, n)
	for i := range out {
		out[i] = winapi.ReadPtr(a.Call("mono_array_addr_with_size", arr, 8, uintptr(i)))
	}
	return out, nil
}

func (a *API) ArrayLen(arr uintptr) (uintptr, error) {
	b, e := a.Invoke(a.Method(a.Class("mscorlib", "System", "Array"), "get_Length", 0), arr)
	if e != nil {
		return 0, e
	}
	n := a.Int(b)
	if n < 0 {
		return 0, fmt.Errorf("negative array length")
	}
	return uintptr(n), nil
}
func (a *API) Fields(c uintptr) []Field {
	var fs []Field
	for c != 0 {
		var it uintptr
		for {
			p := a.Call("mono_class_get_fields", c, winapi.Ptr(&it))
			if p == 0 {
				break
			}
			fs = append(fs, Field{p, c, CString(a.Call("mono_field_get_name", p)), uint32(a.Call("mono_field_get_flags", p)), a.Call("mono_field_get_type", p)})
		}
		c = a.Call("mono_class_get_parent", c)
	}
	return fs
}

type Field struct {
	Ptr, Class uintptr
	Name       string
	Flags      uint32
	Type       uintptr
}

func (a *API) FieldValue(f Field, obj uintptr) uintptr {
	return a.Call("mono_field_get_value_object", a.Domain, f.Ptr, obj)
}
func (a *API) IsUnity(c uintptr) bool {
	base := a.Class("UnityEngine", "UnityEngine", "Object")
	for c != 0 {
		if c == base {
			return true
		}
		c = a.Call("mono_class_get_parent", c)
	}
	return false
}
func (a *API) Alive(obj uintptr) bool {
	r, e := a.Static("UnityEngine", "UnityEngine", "Object", "op_Implicit", obj)
	return e == nil && a.Bool(r)
}
