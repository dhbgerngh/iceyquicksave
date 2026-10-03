package agent

import "sync"

type result struct{ err error }
type resultMap struct{ sync.Map }

func (m *resultMap) Store(k string, e error) { m.Map.Store(k, result{e}) }
func (m *resultMap) LoadAndDelete(k string) (result, bool) {
	v, ok := m.Map.LoadAndDelete(k)
	if !ok {
		return result{}, false
	}
	return v.(result), true
}

var jWriteResult resultMap
