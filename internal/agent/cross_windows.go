package agent

import (
	"fmt"
	"iceyquicksave/internal/archive"
	"iceyquicksave/internal/snapshot"
	"iceyquicksave/internal/winapi"
	"path/filepath"
	"time"
)

type crossJob struct {
	target, rollback *saved
	stage            int
	started          time.Time
	async            uint32
	waitFrames       int
	recovering       bool
	cause            error
	world            *snapshot.World
	graph            *snapshot.Graph
	engine           *snapshot.Engine
}

func (s *agent) captureRoots(v *saved) error {
	a := s.a
	if e := v.World.Capture(); e != nil {
		return e
	}
	v.Graph.World = v.World
	if e := v.Engine.Capture(); e != nil {
		return e
	}
	if e := v.Engine.Identify(v.World); e != nil {
		return e
	}
	// Replace the temporary zero animator speeds with their pre-freeze values.
	for _, f := range s.freeze.Animators {
		obj := a.Target(f.Handle)
		r, e := v.World.Ref(obj)
		if e != nil {
			continue
		}
		for i := range v.Engine.Items {
			n := &v.Engine.Items[i]
			if n.Kind == "Animator" && n.Ref.Key == r.Key && n.Ref.Index == r.Index {
				for j := range n.Properties {
					if n.Properties[j].Name == "speed" {
						n.Properties[j].Data = append([]byte(nil), f.Speed...)
					}
				}
			}
		}
	}
	for _, name := range []string{"R", "WorldTime", "SaveManager", "BattleAssessmentManager", "Cliff", "GameArea"} {
		c := a.Class("Assembly-CSharp", "", name)
		if c != 0 {
			if e := v.Graph.Statics(c); e != nil {
				return e
			}
		}
	}
	objs, e := a.Find("UnityEngine", "UnityEngine", "MonoBehaviour")
	if e != nil {
		return e
	}
	for _, o := range objs {
		if a.Alive(o) && snapshot.SceneObject(a, o) {
			if _, e = v.Graph.Add(o); e != nil {
				return e
			}
		}
	}
	return nil
}
func releaseSave(v *saved) {
	if v == nil {
		return
	}
	if v.Graph != nil {
		v.Graph.Release()
	}
	if v.Engine != nil {
		v.Engine.Release()
	}
	if v.World != nil {
		v.World.Release()
	}
}

func (s *agent) startCross(id string) {
	if filepath.Base(id) != id {
		s.abort(fmt.Errorf("invalid slot ID"))
		return
	}
	var target saved
	if e := archive.Read(filepath.Join(s.root, "savedata", id, "state.iceyqs"), &target); e != nil {
		s.abort(e)
		return
	}
	if target.Format != 2 || target.World == nil || target.Graph == nil || target.Engine == nil {
		s.abort(fmt.Errorf("此存档属于旧实验格式，请重新按 F5 保存"))
		return
	}
	scene, e := s.scene()
	if e != nil {
		s.abort(e)
		return
	}
	rollback := &saved{Format: 2, Scene: scene, World: snapshot.NewWorld(s.a), Graph: snapshot.NewGraph(s.a), Engine: snapshot.NewEngine(s.a), Scale: append([]byte(nil), s.freeze.Scale...)}
	s.cross = &crossJob{target: &target, rollback: rollback, started: time.Now()}
	if e = s.captureRoots(rollback); e != nil {
		s.cross = nil
		releaseSave(rollback)
		s.abort(e)
		return
	}
	s.status("capturing rollback before scene restore", nil)
}
func (s *agent) stepCross() {
	j := s.cross
	if time.Since(j.started) > 90*time.Second {
		s.crossFailure(fmt.Errorf("跨场景恢复超时"))
		return
	}
	a := s.a
	switch j.stage {
	case 0:
		done, e := j.rollback.Graph.Step(5 * time.Millisecond)
		if e != nil {
			s.cross = nil
			releaseSave(j.rollback)
			s.abort(e)
			return
		}
		if done {
			j.stage = 1
		}
	case 1:
		// Stop old coroutine schedules through Unity, keeping rendering and scene loading alive.
		objs, e := a.Find("UnityEngine", "UnityEngine", "MonoBehaviour")
		if e != nil {
			s.crossFailure(e)
			return
		}
		m := a.Method(a.Class("UnityEngine", "UnityEngine", "MonoBehaviour"), "StopAllCoroutines", 0)
		for _, o := range objs {
			if a.Alive(o) && snapshot.SceneObject(a, o) {
				if _, e = a.Invoke(m, o); e != nil {
					s.crossFailure(e)
					return
				}
			}
		}
		name := a.NewString(j.target.Scene)
		h := a.Pin(name)
		op, e := a.Static("Assembly-CSharp", "", "LevelManager", "LoadScene", name)
		a.Free(h)
		if e != nil || op == 0 {
			s.crossFailure(fmt.Errorf("加载地图 %s 失败：%v", j.target.Scene, e))
			return
		}
		j.async = a.Pin(op)
		j.stage = 2
		s.status("loading scene "+j.target.Scene, nil)
	case 2:
		done, e := a.Get(a.Target(j.async), "isDone")
		if e != nil {
			s.crossFailure(e)
			return
		}
		if !a.Bool(done) {
			return
		}
		a.Free(j.async)
		j.async = 0
		j.stage = 3
		j.waitFrames = 4
	case 3:
		if j.waitFrames > 0 {
			j.waitFrames--
			return
		}
		w, e := j.target.World.Reconcile(a)
		if e != nil {
			s.crossFailure(e)
			return
		}
		j.world = w
		j.stage = 4
		j.waitFrames = 4
	case 4:
		if j.waitFrames > 0 {
			j.waitFrames--
			return
		}
		g, e := j.target.Graph.Rebind(a, j.world)
		if e != nil {
			s.crossFailure(e)
			return
		}
		j.graph = g
		en, e := j.target.Engine.Bind(a, g)
		if e != nil {
			s.crossFailure(e)
			return
		}
		j.engine = en
		if e = g.Validate(); e != nil {
			s.crossFailure(e)
			return
		}
		j.stage = 5
	case 5:
		if e := j.graph.Restore(); e != nil {
			s.crossFailure(e)
			return
		}
		if e := j.world.ApplyTransforms(); e != nil {
			s.crossFailure(e)
			return
		}
		if e := j.engine.Restore(); e != nil {
			s.crossFailure(e)
			return
		}
		// Monster decisions restart from their initialized behavior tree.
		trees, e := a.Find("BehaviorDesignerRuntime", "BehaviorDesigner.Runtime", "BehaviorTree")
		if e != nil {
			s.crossFailure(e)
			return
		}
		for _, o := range trees {
			if !a.Alive(o) || !snapshot.SceneObject(a, o) {
				continue
			}
			zero := byte(0)
			c := a.Call("mono_object_get_class", o)
			if _, e = a.Invoke(a.Method(c, "DisableBehavior", 1), o, winapi.Ptr(&zero)); e != nil {
				s.crossFailure(e)
				return
			}
			if _, e = a.Invoke(a.Method(c, "EnableBehavior", 0), o); e != nil {
				s.crossFailure(e)
				return
			}
		}
		for _, n := range s.freeze.Animators {
			a.Free(n.Handle)
		}
		s.freeze.Animators = nil
		s.freeze.Scale = append([]byte(nil), j.target.Scale...)
		// Rebuilt wrappers are now rooted by scene components and static fields.
		j.graph.Release()
		j.engine.Release()
		j.world.Release()
		releaseSave(j.rollback)
		s.cross = nil
		s.resume()
		if j.recovering {
			s.status("restore rejected; original scene restored", j.cause)
		} else {
			s.status("loaded across scenes "+j.target.ID, nil)
		}
	}
}
func (s *agent) crossFailure(e error) {
	j := s.cross
	s.logger.Printf("scene restore failure: %v", e)
	if j.async != 0 {
		s.a.Free(j.async)
		j.async = 0
	}
	if j.graph != nil {
		j.graph.Release()
		j.graph = nil
	}
	if j.engine != nil {
		j.engine.Release()
		j.engine = nil
	}
	if j.world != nil {
		j.world.Release()
		j.world = nil
	}
	if j.recovering {
		s.status("rollback failed; game logic remains frozen", e)
		s.cross = nil
		return
	}
	j.cause = e
	j.recovering = true
	j.target = j.rollback
	j.stage = 1
	j.started = time.Now()
	s.status("restoring original scene after failure", e)
}
