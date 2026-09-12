//go:build linux || darwin

package ccpeer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewLocalPathFitsTheAddressLimit states the invariant that the type of the
// address imposes. A Unix socket address is not a path of any length, and a
// path that does not fit fails at bind time with EINVAL, which surfaces as
// "invalid argument" and names nothing useful.
//
// The regression this locks down was real: on macOS the per user temporary
// directory is 46 bytes before anything of ours, which left the generated path
// one byte over the limit and broke every test that binds an endpoint.
func TestNewLocalPathFitsTheAddressLimit(t *testing.T) {
	for _, kind := range []string{"inbox", "claudio-ctl"} {
		t.Run(kind, func(t *testing.T) {
			path, err := unixEndpoint{}.NewLocalPath(kind)
			if err != nil {
				t.Fatalf("NewLocalPath(%q): %v", kind, err)
			}
			if len(path) > maxSocketPath {
				t.Errorf("NewLocalPath(%q) returned %d bytes, over the %d byte limit: %s",
					kind, len(path), maxSocketPath, path)
			}
		})
	}
}

// TestNewLocalPathFallsBackWhenTheTemporaryDirectoryIsLong reproduces the macOS
// failure on any Unix. A temporary directory long enough to exhaust the address
// must not produce an unbindable path: the short fallback exists for exactly
// this, and the result has to be one that actually binds.
func TestNewLocalPathFallsBackWhenTheTemporaryDirectoryIsLong(t *testing.T) {
	// Long enough that no name of ours could fit under it, on either platform.
	long := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Both are consulted before the fallback, so both have to be out of the way.
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", long)

	path, err := unixEndpoint{}.NewLocalPath("inbox")
	if err != nil {
		t.Fatalf("NewLocalPath: %v", err)
	}
	if len(path) > maxSocketPath {
		t.Fatalf("the fallback returned %d bytes, over the %d byte limit: %s",
			len(path), maxSocketPath, path)
	}
	if strings.HasPrefix(path, long) {
		t.Errorf("the long temporary directory was used anyway: %s", path)
	}

	// The length check is a prediction about what the kernel will accept, so it
	// is worth confirming against the kernel rather than against itself.
	l, err := unixEndpoint{}.Listen(path)
	if err != nil {
		t.Fatalf("the path that passed the length check did not bind: %v", err)
	}
	t.Cleanup(func() {
		_ = l.Close()
		_ = os.Remove(path)
	})
}

// TestNewLocalPathRefusesADirectoryThatIsASymbolicLink covers the reason the
// directory is inspected at all. One of the candidates sits under a world
// writable directory, where somebody else can get there first and leave a link
// pointing at something of theirs. Following it would put our sockets where
// they can be replaced, and would point the tightening chmod at a target
// somebody else chose.
func TestNewLocalPathRefusesADirectoryThatIsASymbolicLink(t *testing.T) {
	base := t.TempDir()
	elsewhere := t.TempDir()

	const rel = "claudio-planted"
	if err := os.Symlink(elsewhere, filepath.Join(base, rel)); err != nil {
		t.Skipf("this system does not allow the test to create a symbolic link: %v", err)
	}

	err := ensureOwnedDir(base, rel)
	if err == nil {
		t.Fatal("a directory that is a symbolic link was accepted")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("the error does not say why it was refused: %v", err)
	}
}

// TestEnsureOwnedDirTightensLoosePermissions covers the other half. A directory
// that is already ours but readable by everybody is our own doing rather than
// an attack, so it is repaired instead of refused.
func TestEnsureOwnedDirTightensLoosePermissions(t *testing.T) {
	base := t.TempDir()

	const rel = "claudio-loose"
	dir := filepath.Join(base, rel)
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	// Mkdir applies the umask, so set the mode the test actually needs.
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := ensureOwnedDir(base, rel); err != nil {
		t.Fatalf("ensureOwnedDir: %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the directory was left at %#o, want 0700", perm)
	}
}
