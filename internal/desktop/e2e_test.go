package desktop

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestE2ETyping(t *testing.T) {
	if os.Getenv("KENOSM_E2E") == "" {
		t.Skip("set KENOSM_E2E=1 to run the desktop end-to-end test (needs a live session)")
	}
	d, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	exec.Command("pkill", "-x", "xterm").Run()
	time.Sleep(300 * time.Millisecond)
	os.Remove("/tmp/kenosm-e2e.txt")

	if _, err := d.Launch("xterm", []string{"-T", "kenosm-e2e", "-e", "sh"}, ""); err != nil {
		t.Fatalf("Launch: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)

	if err := d.Focus("kenosm-e2e"); err != nil {
		t.Fatalf("Focus: %v", err)
	}
	if err := d.Type("echo HELLO-KENOSM > /tmp/kenosm-e2e.txt"); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := d.Key("Return"); err != nil {
		t.Fatalf("Key: %v", err)
	}
	time.Sleep(800 * time.Millisecond)

	data, err := os.ReadFile("/tmp/kenosm-e2e.txt")
	if err != nil {
		t.Fatalf("read proof file: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "HELLO-KENOSM" {
		t.Fatalf("typed text mismatch: got %q (hex %x)", got, data)
	}

	if _, err := d.ScreenshotWindow("kenosm-e2e"); err != nil {
		t.Fatalf("ScreenshotWindow: %v", err)
	}

	exec.Command("pkill", "-x", "xterm").Run()
}
