package winui

import (
	"iceyquicksave/internal/winapi"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// Exercise the actual Win32 visibility regression: STARTF_USESHOWWINDOW with
// SW_HIDE used to leave the game paused behind an invisible chooser forever.
func TestPickerVisibleWithHiddenStartup(t *testing.T) {
	if root := os.Getenv("ICEYQS_PICKER_TEST_ROOT"); root != "" {
		_, e := Picker(root, "")
		if e != nil {
			t.Fatal(e)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPickerVisibleWithHiddenStartup$")
	cmd.Env = append(os.Environ(), "ICEYQS_PICKER_TEST_ROOT="+t.TempDir())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	defer cmd.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var hwnd uintptr
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		hwnd = winapi.Window(uint32(cmd.Process.Pid))
		if hwnd != 0 {
			break
		}
		select {
		case e := <-done:
			t.Fatalf("picker exited before becoming visible: %v", e)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hwnd == 0 {
		t.Fatal("picker never became visible with hidden startup")
	}
	winapi.U("PostMessageW", hwnd, 0x10, 0, 0)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("picker did not exit after closing")
	}
}
