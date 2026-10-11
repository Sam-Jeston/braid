// Package hosttest provides a stand-in for wsl.exe so tests on Unix can run
// Braid's WSL host end to end.
package hosttest

import (
	"os"
	"path/filepath"
	"testing"
)

// FakeWSL writes a wsl.exe that runs commands on this machine: it honours
// -d (recording each distro in the file "distros" next to it), --cd and
// --exec, and answers -l with the contents of the file "list". Braid and the
// "distro" share one filesystem, so a host.WSL using it has an empty Root.
func FakeWSL(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = -l ]; then cat "$(dirname "$0")/list"; exit 0; fi
while [ $# -gt 0 ]; do
  case $1 in
    -d) printf '%s\n' "$2" >> "$(dirname "$0")/distros"; shift 2 ;;
    --cd) cd "$2" || exit 1; shift 2 ;;
    --exec) shift; exec "$@" ;;
    *) echo "fake wsl.exe: unexpected argument $1" >&2; exit 2 ;;
  esac
done
`
	exe := filepath.Join(dir, "wsl.exe")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}
