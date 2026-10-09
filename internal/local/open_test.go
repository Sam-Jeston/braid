//go:build !windows

package local

import (
	"log/slog"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunOpenerReportsFailure(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo 'No default applications for x-scheme-handler/https' >&2; exit 4")
	err := runOpener(cmd, "https://linear.app/x/issue/ENG-1", true)
	if err == nil || !strings.Contains(err.Error(), "No default applications") || !strings.Contains(err.Error(), "ENG-1") {
		t.Fatalf("want a descriptive error, got %v", err)
	}
}

func TestRunOpenerIgnoresMeaninglessExit(t *testing.T) {
	// explorer.exe exits 1 even when it opened the target.
	if err := runOpener(exec.Command("sh", "-c", "exit 1"), "https://example.com", false); err != nil {
		t.Fatal(err)
	}
}

func TestRunOpenerSucceeds(t *testing.T) {
	if err := runOpener(exec.Command("true"), "https://example.com", true); err != nil {
		t.Fatal(err)
	}
}

func TestRunOpenerDoesNotWaitForLongRunningOpener(t *testing.T) {
	old := openWait
	openWait = 100 * time.Millisecond
	defer func() { openWait = old }()
	cmd := exec.Command("sleep", "5")
	start := time.Now()
	if err := runOpener(cmd, "https://example.com", true); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("waited %v for an opener that was still running", d)
	}
	_ = cmd.Process.Kill()
}

func TestRunOpenerToleratesChildHoldingStderr(t *testing.T) {
	// Like xdg-open handing off to a browser that inherits stderr and keeps running.
	cmd := exec.Command("sh", "-c", "sleep 3 & exit 0")
	if err := runOpener(cmd, "https://example.com", true); err != nil {
		t.Fatalf("a successful opener was reported as failing: %v", err)
	}
}

func TestOpenRefusesUnexportedPaths(t *testing.T) {
	c := newCore(t, slog.New(slog.DiscardHandler))
	for _, target := range []string{"/etc/passwd", "file:///etc/passwd", "javascript:alert(1)", "ssh://host"} {
		if err := c.Open(target); err == nil {
			t.Errorf("Open(%q) should be refused", target)
		}
	}
}
