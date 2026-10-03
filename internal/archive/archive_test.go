package archive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndCorruption(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.iceyqs")
	want := map[string]string{"state": strings.Repeat("敌人状态", 1000)}
	if e := Write(p, want); e != nil {
		t.Fatal(e)
	}
	var got map[string]string
	if e := Read(p, &got); e != nil {
		t.Fatal(e)
	}
	if got["state"] != want["state"] {
		t.Fatal("state mismatch")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	b[16] ^= 1
	if e = os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	if Read(p, &got) == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}
func TestDoesNotReplaceSlot(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.iceyqs")
	if e := Write(p, "first"); e != nil {
		t.Fatal(e)
	}
	if Write(p, "second") == nil {
		t.Fatal("existing slot replaced")
	}
	var got string
	if e := Read(p, &got); e != nil || got != "first" {
		t.Fatalf("original changed: %q %v", got, e)
	}
}
func TestTruncatedAndOversized(t *testing.T) {
	for _, b := range [][]byte{nil, []byte("ICEYQS01"), append(append(append([]byte{}, magic...), []byte{255, 255, 255, 255, 255, 255, 255, 255}...), make([]byte, 32)...)} {
		p := filepath.Join(t.TempDir(), "bad")
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
		var v any
		if Read(p, &v) == nil {
			t.Fatal("malformed snapshot accepted")
		}
	}
}
