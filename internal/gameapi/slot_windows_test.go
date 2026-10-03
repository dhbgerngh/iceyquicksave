package gameapi

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
)

func validSlot() Slot {
	return Slot{Format: SlotFormat, ID: "20261003-120000.000-api", AssemblySHA256: "test-hash", Scene: "C1L2S1", Position: Vec3{1, 2, 3}, Face: 1, PlayerHP: 50, PlayerEnergy: 3,
		GameData: json.RawMessage(`{"SceneName":"C1L2S1","PlayerPosition":{"x":1,"y":2,"z":3},"PlayerAttributeGameData":{"currentHP":50,"CurrentEnergy":3,"faceDir":1,"maxHP":100}}`)}
}

func TestSlotValidationRejectsUnsafeLoads(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Slot)
	}{
		{"old object graph", func(s *Slot) { s.Format = 2 }},
		{"other assembly", func(s *Slot) { s.AssemblySHA256 = "other" }},
		{"mismatched ID", func(s *Slot) { s.ID = "other" }},
		{"main menu", func(s *Slot) { s.Scene = "ui_start" }},
		{"invalid position", func(s *Slot) { s.Position.X = float32(math.NaN()) }},
		{"dead player", func(s *Slot) { s.PlayerHP = 0 }},
		{"null game data", func(s *Slot) { s.GameData = json.RawMessage(`null`) }},
		{"corrupt data", func(s *Slot) { s.GameData = json.RawMessage(`{`) }},
		{"different game data", func(s *Slot) { s.Position.X++ }},
		{"invalid enemy position", func(s *Slot) { s.Enemies = []Enemy{{Position: Vec3{X: float32(math.Inf(1))}}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := validSlot()
			id := s.ID
			tc.edit(&s)
			if s.Validate(id, "test-hash") == nil {
				t.Fatal("invalid slot accepted")
			}
		})
	}
	s := validSlot()
	if e := s.Validate(s.ID, "test-hash"); e != nil {
		t.Fatal(e)
	}
	s.Format = 3
	if e := s.Validate(s.ID, "test-hash"); e != nil {
		t.Fatal("API diagnostic compatibility:", e)
	}
	for _, id := range []string{"", ".", "..", "../outside", `..\outside`, `C:\slot`, "slot:stream", ".pending-slot", "CON"} {
		if ValidSlotID(id) {
			t.Fatalf("invalid path accepted: %q", id)
		}
	}
}

func TestGameBufferEncodingMatchesBinaryWriterString(t *testing.T) {
	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 65520} {
		input := bytes.Repeat([]byte("x"), n)
		buf, e := EncodeGameBuffer(input)
		if e != nil {
			t.Fatal(e)
		}
		// GetBuffer returns unused capacity, not just the written string.
		buf = append(buf, make([]byte, 7)...)
		got, e := DecodeGameBuffer(buf)
		if e != nil || !bytes.Equal(got, input) {
			t.Fatalf("length %d: %v", n, e)
		}
	}
	for _, b := range [][]byte{{}, {0x80}, {0xff, 0xff, 0xff, 0xff, 0x10}, {4, 'x'}} {
		if _, e := DecodeGameBuffer(b); e == nil {
			t.Fatalf("corrupt length accepted: %v", b)
		}
	}
}
