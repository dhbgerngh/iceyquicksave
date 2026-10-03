package gameapi

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

const SlotFormat = 4
const SlotStatus = "game-api"

func ValidSlotID(id string) bool {
	return id != "" && id != "." && !strings.HasPrefix(id, ".") &&
		filepath.IsLocal(id) && !strings.ContainsAny(id, `/\:`) && filepath.Base(id) == id
}

// Validate runs before any live game data is changed. Older object-graph
// snapshots cannot be interpreted as GameData and are deliberately rejected.
func (s *Slot) Validate(id, hash string) error {
	if !ValidSlotID(id) || s.ID != id {
		return fmt.Errorf("存档编号无效或不匹配")
	}
	if s.Format != SlotFormat && s.Format != 3 {
		return fmt.Errorf("旧版对象快照无法由游戏存档接口读取，请在新版中按 F5 重新保存")
	}
	if hash == "" || s.AssemblySHA256 != hash {
		return fmt.Errorf("存档与当前游戏版本不一致")
	}
	if s.Scene == "" || s.Scene == "ui_start" || strings.ContainsAny(s.Scene, "\x00/\\:") {
		return fmt.Errorf("存档地图无效")
	}
	for _, v := range []float32{s.Position.X, s.Position.Y, s.Position.Z, s.Velocity.X, s.Velocity.Y} {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return fmt.Errorf("存档坐标或速度无效")
		}
	}
	if s.Face != -1 && s.Face != 1 {
		return fmt.Errorf("存档角色朝向无效")
	}
	if s.PlayerHP <= 0 || s.PlayerEnergy < 0 {
		return fmt.Errorf("存档角色已死亡或属性无效")
	}
	for _, enemy := range s.Enemies {
		if enemy.HP < 0 || enemy.SP < 0 {
			return fmt.Errorf("存档敌人属性无效")
		}
		for _, v := range []float32{enemy.Position.X, enemy.Position.Y, enemy.Position.Z, enemy.Velocity.X, enemy.Velocity.Y} {
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return fmt.Errorf("存档敌人坐标或速度无效")
			}
		}
	}
	var data struct {
		SceneName               string
		PlayerPosition          struct{ X, Y, Z float32 }
		PlayerAttributeGameData *struct{ CurrentHP, CurrentEnergy, FaceDir, MaxHP int32 }
	}
	if len(s.GameData) == 0 || len(s.GameData) > 65520 || json.Unmarshal(s.GameData, &data) != nil {
		return fmt.Errorf("游戏存档数据损坏")
	}
	attr := data.PlayerAttributeGameData
	if data.SceneName != s.Scene || data.PlayerPosition.X != s.Position.X || data.PlayerPosition.Y != s.Position.Y || data.PlayerPosition.Z != s.Position.Z || attr == nil || attr.CurrentHP != s.PlayerHP || attr.CurrentEnergy != s.PlayerEnergy || attr.FaceDir != s.Face || attr.MaxHP < s.PlayerHP {
		return fmt.Errorf("游戏存档数据与快照信息不一致")
	}
	return nil
}
