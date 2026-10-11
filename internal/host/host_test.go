package host

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"unicode/utf16"
)

func ubuntu() *WSL {
	return &WSL{Distro: "Ubuntu", Exe: "wsl.exe", Root: `\\wsl.localhost\Ubuntu`, Home: "/home/sam", Mount: "/mnt/"}
}

func TestWSLPath(t *testing.T) {
	w := ubuntu()
	for in, want := range map[string]string{
		`\\wsl.localhost\Ubuntu\home\sam\x`: "/home/sam/x",
		`\\WSL.LOCALHOST\ubuntu\home`:       "/home",
		`\\wsl$\Ubuntu\tmp\run`:             "/tmp/run",
		`\\wsl.localhost\Ubuntu`:            "/",
		`//wsl.localhost/Ubuntu/opt/app`:    "/opt/app",
		`\\wsl.localhost\Ubuntu-22.04\x`:    `\\wsl.localhost\Ubuntu-22.04\x`, // another distro
		`\\server\share\x`:                  `\\server\share\x`,
		`C:\Users\Sam\code`:                 "/mnt/c/Users/Sam/code",
		`d:/data`:                           "/mnt/d/data",
		`C:`:                                "/mnt/c",
		"/already/linux":                    "/already/linux",
		"relative/path":                     "relative/path",
	} {
		if got := w.Path(in); got != want {
			t.Errorf("Path(%q) = %q, want %q", in, got, want)
		}
	}
	w.Mount = "/"
	if got := w.Path(`C:\x`); got != "/c/x" {
		t.Errorf("with drives mounted at /: %q", got)
	}
}

func TestWSLFromToolPath(t *testing.T) {
	w := ubuntu()
	for in, want := range map[string]string{
		"/home/sam/code": `\\wsl.localhost\Ubuntu\home\sam\code`,
		"/mnt/c/Users":   `C:\Users`,
		"/mnt/d":         `D:\`,
		"/mnt/cdrom/x":   `\\wsl.localhost\Ubuntu\mnt\cdrom\x`,
		"code":           "code",
		`C:\x`:           `C:\x`,
	} {
		if got := w.FromToolPath(in); got != want {
			t.Errorf("FromToolPath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, p := range []string{"/home/sam/code", "/mnt/c/Users"} {
		if got := w.Path(w.FromToolPath(p)); got != p {
			t.Errorf("round trip of %q gave %q", p, got)
		}
	}
	if got := w.DataDir(); got != `\\wsl.localhost\Ubuntu\home\sam\.local\share\braid` {
		t.Errorf("DataDir = %q", got)
	}
	w.Root = ""
	if got := w.FromToolPath("/home/sam"); got != "/home/sam" {
		t.Errorf("without a root, tool paths are Braid paths: %q", got)
	}
}

func TestWSLCommandArgs(t *testing.T) {
	w := ubuntu()
	cmd := w.Command(context.Background(), "/home/sam/.local/bin/claude", []string{"-p", "two words"}, `\\wsl.localhost\Ubuntu\tmp\run`, "A=1")
	want := []string{"wsl.exe", "-d", "Ubuntu", "--cd", "/tmp/run", "--exec", "env", "A=1", "sh", "-c", execScript, "/home/sam/.local/bin/claude", "-p", "two words"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args = %q\nwant  %q", cmd.Args, want)
	}
	if !slices.Contains(cmd.Env, "WSL_UTF8=1") {
		t.Error("wsl.exe should be asked for UTF-8 output")
	}
	if cmd := w.Command(context.Background(), "git", nil, ""); slices.Contains(cmd.Args, "--cd") {
		t.Errorf("no dir means no --cd: %q", cmd.Args)
	}
	if w.ID() != "wsl:Ubuntu" || w.Label() != "WSL (Ubuntu)" {
		t.Errorf("id %q label %q", w.ID(), w.Label())
	}
}

func utf16le(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func TestParseDistros(t *testing.T) {
	want := []string{"Ubuntu", "Ubuntu-22.04"}
	for name, out := range map[string][]byte{
		"utf-16": utf16le("Ubuntu\r\nUbuntu-22.04\r\ndocker-desktop\r\ndocker-desktop-data\r\n"),
		"utf-8":  []byte("\ufeffUbuntu\nUbuntu-22.04\n\n"),
	} {
		if got := parseDistros(out); !slices.Equal(got, want) {
			t.Errorf("%s: %q", name, got)
		}
	}
	if got := decode([]byte("plain")); got != "plain" {
		t.Errorf("decode = %q", got)
	}
}

func TestResolve(t *testing.T) {
	for _, id := range []string{"", LocalID} {
		h, err := Resolve(context.Background(), id, "/data")
		if err != nil || h.ID() != LocalID || h.DataDir() != "/data" {
			t.Errorf("Resolve(%q) = %v, %v", id, h, err)
		}
	}
	if _, err := Resolve(context.Background(), "mars", ""); err == nil {
		t.Error("unknown host resolved")
	}
	if runtime.GOOS != "windows" && len(Options(context.Background())) != 1 {
		t.Error("only Windows offers WSL")
	}
}

func TestLocal(t *testing.T) {
	l := Local{Data: "/d"}
	if l.Path("/x") != "/x" || l.FromToolPath("/x") != "/x" || l.Label() == "" {
		t.Error("the local host maps paths to themselves")
	}
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("BRAID_KEEP_ME", "1")
	cmd := l.Command(context.Background(), "git", []string{"status"}, "/repo", "GIT_TERMINAL_PROMPT=0")
	if cmd.Dir != "/repo" || slices.Contains(cmd.Env, "CLAUDECODE=1") || !slices.Contains(cmd.Env, "BRAID_KEEP_ME=1") || !slices.Contains(cmd.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("dir %q env %q", cmd.Dir, cmd.Env)
	}
}

func TestLocalLookPathFallbacks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", t.TempDir())
	exe := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exe(filepath.Join(home, ".nvm", "versions", "node", "v18.0.0", "bin", "codex"))
	exe(filepath.Join(home, ".nvm", "versions", "node", "v20.1.0", "bin", "codex"))
	exe(filepath.Join(home, ".bun", "bin", "claude"))
	l := Local{}
	if p, err := l.LookPath(context.Background(), "codex"); err != nil || p != filepath.Join(home, ".nvm", "versions", "node", "v20.1.0", "bin", "codex") {
		t.Errorf("codex = %q, %v", p, err)
	}
	if p, err := l.LookPath(context.Background(), "claude"); err != nil || p != filepath.Join(home, ".bun", "bin", "claude") {
		t.Errorf("claude = %q, %v", p, err)
	}
	if _, err := l.LookPath(context.Background(), "nope"); err == nil {
		t.Error("found a CLI that isn't installed")
	}
}
