//go:build !windows

package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/host/hosttest"
)

func writeExe(t *testing.T, p, script string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestNewWSLLearnsTheDistro(t *testing.T) {
	exe := hosttest.FakeWSL(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	w, err := NewWSL(context.Background(), exe, "Ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if w.Home != home || w.Mount != "/mnt/" || w.Root != "" || w.DataDir() != filepath.Join(home, ".local", "share", "braid") {
		t.Errorf("%+v data %q", w, w.DataDir())
	}
	if b, _ := os.ReadFile(filepath.Join(filepath.Dir(exe), "distros")); strings.TrimSpace(string(b)) != "Ubuntu" {
		t.Errorf("ran in distros %q", b)
	}

	broken := filepath.Join(t.TempDir(), "wsl.exe")
	writeExe(t, broken, `echo "There is no distribution with the supplied name." >&2; exit 1`)
	if _, err := NewWSL(context.Background(), broken, "Gone"); err == nil || !strings.Contains(err.Error(), "no distribution") {
		t.Errorf("a missing distro should say so: %v", err)
	}
}

func TestWSLCommandRuns(t *testing.T) {
	w := &WSL{Distro: "Ubuntu", Exe: hosttest.FakeWSL(t), Mount: "/mnt/"}
	tool := filepath.Join(t.TempDir(), "bin", "tool")
	// The tool's own dir comes first on PATH, as nvm's node needs.
	writeExe(t, tool, `printf '%s|%s|%s|' "$PWD" "$FOO" "${PATH%%:*}"; printf '[%s]' "$@"`)
	work := t.TempDir()
	out, err := w.Command(context.Background(), tool, []string{"a b", `"q"`, "$HOME"}, work, "FOO=bar").Output()
	if err != nil {
		t.Fatal(err)
	}
	if want := work + "|bar|" + filepath.Dir(tool) + `|[a b]["q"][$HOME]`; string(out) != want {
		t.Errorf("got  %s\nwant %s", out, want)
	}
}

func TestWSLLookPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	onPath := t.TempDir()
	t.Setenv("PATH", onPath+":/usr/bin:/bin")
	w := &WSL{Distro: "Ubuntu", Exe: hosttest.FakeWSL(t), Home: home, Mount: "/mnt/"}
	ctx := context.Background()

	writeExe(t, filepath.Join(onPath, "braid-tool"), "")
	if p, err := w.LookPath(ctx, "braid-tool"); err != nil || p != filepath.Join(onPath, "braid-tool") {
		t.Errorf("on PATH: %q, %v", p, err)
	}
	// Windows programs reached through the drive mounts don't count.
	w.Mount = onPath + "/"
	if p, err := w.LookPath(ctx, "braid-tool"); err == nil {
		t.Errorf("found a Windows program: %q", p)
	} else if !strings.Contains(err.Error(), "WSL (Ubuntu)") {
		t.Errorf("error should name the distro: %v", err)
	}
	w.Mount = "/mnt/"

	writeExe(t, filepath.Join(home, ".local", "bin", "claude"), "")
	writeExe(t, filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin", "codex"), "")
	writeExe(t, filepath.Join(home, ".nvm", "versions", "node", "v20.1.0", "bin", "codex"), "")
	if p, err := w.LookPath(ctx, "claude"); err != nil || p != filepath.Join(home, ".local", "bin", "claude") {
		t.Errorf("claude = %q, %v", p, err)
	}
	if p, err := w.LookPath(ctx, "codex"); err != nil || p != filepath.Join(home, ".nvm", "versions", "node", "v20.1.0", "bin", "codex") {
		t.Errorf("codex = %q, %v", p, err)
	}
}

func TestWSLDiscovery(t *testing.T) {
	exe := hosttest.FakeWSL(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(exe), "list"), utf16le("Ubuntu\r\ndocker-desktop\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := wslOptions(context.Background(), exe)
	if len(opts) != 1 || opts[0].ID != "wsl:Ubuntu" || opts[0].Label != "WSL (Ubuntu)" {
		t.Errorf("options = %+v", opts)
	}
	if opts := wslOptions(context.Background(), filepath.Join(t.TempDir(), "missing.exe")); opts != nil {
		t.Errorf("no WSL should mean no options: %+v", opts)
	}
	t.Setenv("WSL_DISTRO_NAME", "Debian")
	if d := defaultDistro(context.Background(), exe); d != "Debian" {
		t.Errorf("default distro = %q", d)
	}
}
