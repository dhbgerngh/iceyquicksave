package agent

import (
	"fmt"
	"iceyquicksave/internal/archive"
	"iceyquicksave/internal/gameapi"
	"iceyquicksave/internal/winapi"
	"path/filepath"
	"time"
)

type loadOperation interface {
	Start() error
	Ready() (bool, error)
	ApplyPlayer() error
	Close()
}

// prepareAndStartLoad keeps disk I/O, validation and managed allocation inside
// the pause. Resume and Start are deliberately adjacent synchronous calls.
func prepareAndStartLoad(prepare func() (loadOperation, error), resume func() error) (loadOperation, error) {
	op, e := prepare()
	if e != nil {
		return nil, e
	}
	if e = resume(); e != nil {
		op.Close()
		return nil, fmt.Errorf("恢复游戏失败：%w", e)
	}
	if e = op.Start(); e != nil {
		op.Close()
		return nil, fmt.Errorf("游戏读档失败：%w", e)
	}
	return op, nil
}

func (s *agent) beginLoad(id string) {
	var slot gameapi.Slot
	op, e := prepareAndStartLoad(func() (loadOperation, error) {
		if !gameapi.ValidSlotID(id) {
			return nil, fmt.Errorf("存档编号无效")
		}
		if e := archive.Read(filepath.Join(s.root, "savedata", id, "state.iceyqs"), &slot); e != nil {
			return nil, fmt.Errorf("读取存档失败：%w", e)
		}
		if e := slot.Validate(id, s.hash); e != nil {
			return nil, e
		}
		return s.game.PrepareLoad(&slot)
	}, s.resume)
	if e != nil {
		s.fail(e)
		return
	}
	s.load = &pendingLoad{slot: &slot, op: op, started: time.Now()}
	winapi.U("SetForegroundWindow", s.hwnd)
	s.status("loading "+id+"; game logic resumed", nil)
}

func (s *agent) pollLoad() {
	j := s.load
	if time.Since(j.started) > 45*time.Second {
		s.fail(fmt.Errorf("游戏场景加载超时；暂停已解除，请检查当前地图状态"))
		return
	}
	ready, e := j.op.Ready()
	if e != nil {
		s.fail(e)
		return
	}
	if !ready {
		return
	}
	if e = j.op.ApplyPlayer(); e != nil {
		s.fail(e)
		return
	}
	missing, e := s.game.ApplyEnemies(j.slot.Enemies)
	if e != nil {
		s.fail(e)
		return
	}
	j.op.Close()
	s.load = nil
	for _, gap := range j.slot.Gaps {
		s.logger.Printf("restore scope: %s", gap)
	}
	if len(missing) > 0 {
		s.logger.Printf("enemies not restored: %v", missing)
	}
	s.status(fmt.Sprintf("loaded %s; partial battle restore; %d enemies unmatched", j.slot.ID, len(missing)), nil)
}
