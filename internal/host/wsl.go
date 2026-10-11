package host

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf16"
)

// wslExe is the WSL launcher. Tests substitute a fake.
var wslExe = defaultWSLExe()

func defaultWSLExe() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		return filepath.Join(root, "System32", "wsl.exe")
	}
	return "wsl.exe"
}

// WSL runs tools inside a WSL distribution through wsl.exe. Every argument
// goes through argv (wsl.exe --exec), never a shell command line.
type WSL struct {
	Distro string
	Exe    string // wsl.exe
	// Root is the Braid path of the distro's "/", e.g. \\wsl.localhost\Ubuntu.
	// Empty when Braid and the tools share a filesystem (tests).
	Root  string
	Home  string // $HOME inside the distro
	Mount string // where Windows drives are mounted, e.g. /mnt/
}

// execScript puts the tool's own directory first on PATH, so a CLI that nvm
// installed finds its node, then replaces the shell with the tool.
const execScript = `PATH="${0%/*}:$PATH"; export PATH; exec "$0" "$@"`

// NewWSL starts the distribution if needed and learns its home directory and
// where it mounts Windows drives.
func NewWSL(ctx context.Context, exe, distro string) (*WSL, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	w := &WSL{Distro: distro, Exe: exe, Mount: "/mnt/"}
	out, err := w.output(ctx, "sh", "-c", `printf '%s\n' "$HOME"; wslpath -u 'C:\' 2>/dev/null || true`)
	if err != nil {
		return nil, fmt.Errorf("couldn't start %s: %w", w.Label(), err)
	}
	lines := nonEmptyLines(out)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return nil, fmt.Errorf("%s didn't report a home directory", w.Label())
	}
	w.Home = lines[0]
	if len(lines) > 1 {
		if m, ok := strings.CutSuffix(strings.TrimRight(lines[1], "/"), "/c"); ok {
			w.Mount = m + "/"
		}
	}
	w.Root = uncRoot(distro)
	return w, nil
}

// uncRoot is how Windows reaches the distro's files. Older Windows only
// knows the \\wsl$ form.
func uncRoot(distro string) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	root := `\\wsl.localhost\` + distro
	if _, err := os.Stat(root + `\`); err != nil {
		root = `\\wsl$\` + distro
	}
	return root
}

func (w *WSL) ID() string    { return "wsl:" + w.Distro }
func (w *WSL) Label() string { return "WSL (" + w.Distro + ")" }

func (w *WSL) DataDir() string {
	return w.FromToolPath(path.Join(w.Home, ".local", "share", "braid"))
}

// Path maps \\wsl.localhost\<distro>\x (or \\wsl$\…) to /x and C:\x to
// /mnt/c/x. Other paths are returned unchanged.
func (w *WSL) Path(p string) string {
	if rest, ok := w.inDistro(p); ok {
		return rest
	}
	if isDrivePath(p) {
		return strings.TrimRight(w.Mount, "/") + "/" + strings.ToLower(p[:1]) + strings.ReplaceAll(p[2:], `\`, "/")
	}
	return p
}

func (w *WSL) inDistro(p string) (string, bool) {
	if !strings.HasPrefix(p, `\\`) && !strings.HasPrefix(p, "//") {
		return "", false
	}
	q := strings.ReplaceAll(p, "/", `\`)
	for _, root := range []string{w.Root, `\\wsl.localhost\` + w.Distro, `\\wsl$\` + w.Distro} {
		if root == "" || len(q) < len(root) || !strings.EqualFold(q[:len(root)], root) {
			continue
		}
		if len(q) > len(root) && q[len(root)] != '\\' {
			continue
		}
		if rest := strings.ReplaceAll(q[len(root):], `\`, "/"); rest != "" {
			return rest, true
		}
		return "/", true
	}
	return "", false
}

// FromToolPath maps /mnt/c/x to C:\x and any other absolute path to the
// distro's files as Windows sees them.
func (w *WSL) FromToolPath(p string) string {
	if !strings.HasPrefix(p, "/") || w.Root == "" {
		return p
	}
	if rest, ok := strings.CutPrefix(p, w.Mount); ok && len(rest) > 0 && isLetter(rest[0]) && (len(rest) == 1 || rest[1] == '/') {
		tail := rest[1:]
		if tail == "" {
			tail = "/"
		}
		return strings.ToUpper(rest[:1]) + ":" + strings.ReplaceAll(tail, "/", `\`)
	}
	return w.Root + strings.ReplaceAll(p, "/", `\`)
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func isDrivePath(p string) bool {
	return len(p) >= 2 && isLetter(p[0]) && p[1] == ':' && (len(p) == 2 || p[2] == '\\' || p[2] == '/')
}

func (w *WSL) Command(ctx context.Context, bin string, args []string, dir string, env ...string) *exec.Cmd {
	argv := []string{"-d", w.Distro}
	if dir != "" {
		argv = append(argv, "--cd", w.Path(dir))
	}
	argv = append(argv, "--exec", "env")
	argv = append(argv, env...)
	argv = append(argv, "sh", "-c", execScript, bin)
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, w.Exe, argv...)
	cmd.Env = append(cleanEnv(), "WSL_UTF8=1")
	hideWindow(cmd)
	return cmd
}

// lookScript prints the first match for a CLI: on the distro's PATH (but not
// a Windows program reached through the drive mounts), then each candidate.
// A candidate with a * is a glob whose last match wins.
const lookScript = `IFS=
n=$1 mount=$2
shift 2
if p=$(command -v "$n" 2>/dev/null); then
  case $p in "$mount"*) ;; /*) echo "$p"; exit 0 ;; esac
fi
for c in "$@"; do
  case $c in *'*'*)
    last=
    for m in $c; do [ -f "$m" ] && [ -x "$m" ] && last=$m; done
    c=$last ;;
  esac
  if [ -n "$c" ] && [ -f "$c" ] && [ -x "$c" ]; then echo "$c"; exit 0; fi
done
exit 1`

// LookPath searches the distro the way Local searches this computer: a CLI
// started through wsl.exe gets the distro's bare default PATH, without what
// shell startup files add.
func (w *WSL) LookPath(ctx context.Context, name string) (string, error) {
	args := append([]string{"-c", lookScript, "sh", name, w.Mount}, candidates(w.Home, name, path.Join)...)
	out, err := w.output(ctx, "sh", args...)
	if lines := nonEmptyLines(out); err == nil && len(lines) > 0 {
		return lines[len(lines)-1], nil
	}
	return "", fmt.Errorf("%s not found in %s", name, w.Label())
}

func (w *WSL) output(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := w.Command(ctx, bin, args, "")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(decode(stderr.Bytes()) + "\n" + decode(out)); msg != "" {
			lines := strings.Split(msg, "\n")
			return "", fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
		}
		return "", err
	}
	return string(out), nil
}

// DefaultDistro is the WSL distribution wsl.exe uses without -d, or "".
func DefaultDistro(ctx context.Context) string {
	if runtime.GOOS != "windows" {
		return ""
	}
	return defaultDistro(ctx, wslExe)
}

func defaultDistro(ctx context.Context, exe string) string {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--exec", "printenv", "WSL_DISTRO_NAME")
	cmd.Env = append(cleanEnv(), "WSL_UTF8=1")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(decode(out))
}

func wslOptions(ctx context.Context, exe string) []Option {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-l", "-q")
	cmd.Env = append(cleanEnv(), "WSL_UTF8=1")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		// No WSL, or no distributions installed.
		return nil
	}
	var opts []Option
	for _, d := range parseDistros(out) {
		w := WSL{Distro: d}
		opts = append(opts, Option{ID: w.ID(), Label: w.Label()})
	}
	return opts
}

// parseDistros reads `wsl.exe -l -q`, skipping Docker Desktop's internal
// distributions, which have no user tools.
func parseDistros(out []byte) []string {
	var ds []string
	for _, line := range strings.Split(decode(out), "\n") {
		d := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "\ufeff"))
		if d == "" || strings.HasPrefix(strings.ToLower(d), "docker-desktop") {
			continue
		}
		ds = append(ds, d)
	}
	return ds
}

// decode returns wsl.exe's own output as text. Unless WSL_UTF8=1 is honoured
// (newer WSL), it writes UTF-16LE.
func decode(b []byte) string {
	if len(b) < 2 || bytes.IndexByte(b, 0) < 0 {
		return string(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = uint16(b[2*i]) | uint16(b[2*i+1])<<8
	}
	return string(utf16.Decode(u))
}
