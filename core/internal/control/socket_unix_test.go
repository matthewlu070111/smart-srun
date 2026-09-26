//go:build unix

package control

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// socketPathIn keeps the path short. A Unix socket address is limited to about
// 100 bytes, and a long test name plus a temp directory gets close to it.
func socketPathIn(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "srunctl")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	return filepath.Join(base, "run", "control.sock")
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %s: %v", path, err)
	}
	return info.Mode()
}

// The directory is the security boundary, so both halves of the contract are
// asserted: reaching the socket at all has to require already being the user
// the daemon runs as.
func TestListenAppliesThePathAndModeContract(t *testing.T) {
	path := socketPathIn(t)

	listener, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	dirMode := modeOf(t, filepath.Dir(path))
	if !dirMode.IsDir() {
		t.Fatalf("runtime path is %v, not a directory", dirMode)
	}
	if dirMode.Perm() != RuntimeDirMode {
		t.Errorf("directory mode %v, want %v", dirMode.Perm(), RuntimeDirMode)
	}

	socketMode := modeOf(t, path)
	if socketMode&fs.ModeSocket == 0 {
		t.Fatalf("%s is %v, not a socket", path, socketMode)
	}
	if socketMode.Perm() != SocketFileMode {
		t.Errorf("socket mode %v, want %v", socketMode.Perm(), SocketFileMode)
	}

	if err := VerifySocketSecurity(path); err != nil {
		t.Errorf("the endpoint Listen just created fails its own check: %v", err)
	}
}

// The declared path is part of the contract: the Lua bridge and the CLI find
// the daemon by it, so it cannot drift with a refactor.
func TestTheSocketPathIsTheOneTheContractFixes(t *testing.T) {
	if SocketPath != "/var/run/smart-srun/control.sock" {
		t.Errorf("SocketPath = %q", SocketPath)
	}
	if filepath.Dir(SocketPath) != RuntimeDir {
		t.Errorf("RuntimeDir = %q does not hold SocketPath = %q", RuntimeDir, SocketPath)
	}
	if RuntimeDirMode != 0o700 || SocketFileMode != 0o600 {
		t.Errorf("modes are %v/%v, contract says 0700/0600", RuntimeDirMode, SocketFileMode)
	}
}

// An upgrade from a version that created the directory more loosely must not
// leave it that way. MkdirAll does nothing at all when the path already exists.
func TestListenTightensADirectoryThatWasLeftOpen(t *testing.T) {
	path := socketPathIn(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	listener, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	if got := modeOf(t, filepath.Dir(path)).Perm(); got != RuntimeDirMode {
		t.Fatalf("directory mode %v after Listen, want %v", got, RuntimeDirMode)
	}
}

// A daemon that was killed leaves its socket behind. Refusing to start would
// need a manual cleanup after every crash.
func TestListenReplacesAStaleSocket(t *testing.T) {
	path := socketPathIn(t)

	first, err := Listen(path)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	// Closing a Unix listener normally unlinks the socket, so leave the file in
	// place to reproduce what a killed process leaves behind.
	first.(*net.UnixListener).SetUnlinkOnClose(false)
	first.Close()

	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the stale socket was not left in place: %v", err)
	}

	second, err := Listen(path)
	if err != nil {
		t.Fatalf("a stale socket blocked startup: %v", err)
	}
	second.Close()
}

// Removing whatever is at the path would make this program delete a file
// somebody else chose. Only a socket is ours to clear.
func TestListenRefusesToUnlinkSomethingThatIsNotASocket(t *testing.T) {
	path := socketPathIn(t)
	if err := os.MkdirAll(filepath.Dir(path), RuntimeDirMode); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if _, err := Listen(path); err == nil {
		t.Fatal("a regular file at the socket path was removed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("the file was deleted anyway: %v", err)
	}
}

// A symlinked runtime directory redirects every mode change and every file this
// program writes to a location it did not choose.
func TestListenRefusesASymlinkedRuntimeDirectory(t *testing.T) {
	path := socketPathIn(t)
	elsewhere := filepath.Join(filepath.Dir(filepath.Dir(path)), "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o700); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.Symlink(elsewhere, filepath.Dir(path)); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}

	if _, err := Listen(path); err == nil {
		t.Fatal("a symlinked runtime directory was accepted")
	}
}

// The check exists so a daemon refuses to serve through an endpoint somebody
// loosened, rather than finding out from the first unexpected caller.
func TestVerifySocketSecurityRejectsLooseModes(t *testing.T) {
	path := socketPathIn(t)
	listener, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("chmod socket: %v", err)
	}
	if err := VerifySocketSecurity(path); err == nil {
		t.Error("a world-writable socket passed the check")
	}
	if err := os.Chmod(path, SocketFileMode); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if err := os.Chmod(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	if err := VerifySocketSecurity(path); err == nil {
		t.Error("a world-traversable runtime directory passed the check")
	}
}

func TestVerifySocketSecurityRejectsAMissingOrWrongEndpoint(t *testing.T) {
	path := socketPathIn(t)
	if err := VerifySocketSecurity(path); err == nil {
		t.Error("a socket that does not exist passed the check")
	}

	if err := os.MkdirAll(filepath.Dir(path), RuntimeDirMode); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := VerifySocketSecurity(path); err == nil {
		t.Error("a regular file was accepted as the control socket")
	}
}

// End to end over a real socket: the framing, the deadlines and the modes have
// to hold together on the transport this protocol actually runs on, not only on
// a buffer in a test.
func TestARealSocketServesOneRequestPerConnection(t *testing.T) {
	path := socketPathIn(t)
	listener, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	registry := registryWith(t, "version.get",
		func(context.Context, json.RawMessage) (any, error) {
			return map[string]string{"version": "2.0.0rc1"}, nil
		})

	served := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			served <- acceptErr
			return
		}
		defer conn.Close()
		served <- ServeConn(context.Background(), registry, conn)
	}()

	client, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	request := `{"rpc_version":1,"request_id":"req-real","method":"version.get"}` + "\n"
	if _, err := client.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}

	line, err := bufio.NewReader(client).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if err := <-served; err != nil {
		t.Fatalf("ServeConn: %v", err)
	}

	var response Response
	if err := json.Unmarshal([]byte(strings.TrimRight(line, "\n")), &response); err != nil {
		t.Fatalf("response does not parse: %v (%q)", err, line)
	}
	if !response.OK || response.RequestID != "req-real" {
		t.Fatalf("response = %+v", response)
	}
}

// When the endpoint cannot be created the daemon has to say so and stop, not
// carry on with no way for anything to reach it.
func TestListenFailsClosedWhenTheEndpointCannotBeCreated(t *testing.T) {
	t.Run("the runtime directory cannot be made", func(t *testing.T) {
		base, err := os.MkdirTemp("", "srunctl")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })

		blocked := filepath.Join(base, "blocked")
		if err := os.WriteFile(blocked, nil, 0o600); err != nil {
			t.Fatalf("prepare: %v", err)
		}

		if _, err := Listen(filepath.Join(blocked, "run", "control.sock")); err == nil {
			t.Fatal("Listen reported success with no directory to listen in")
		}
	})

	t.Run("the address is longer than a Unix socket path", func(t *testing.T) {
		base, err := os.MkdirTemp("", "srunctl")
		if err != nil {
			t.Fatalf("temp dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(base) })

		// sun_path is about 100 bytes. A path over it must come back as a
		// refusal, not as a panic out of the net package.
		path := filepath.Join(base, strings.Repeat("d", 120), "control.sock")
		if _, err := Listen(path); err == nil {
			t.Fatal("an address too long for a Unix socket was accepted")
		}
	})
}

// Not TCP. A control protocol that could be reached from the network would be a
// management port on a campus router.
func TestTheProtocolIsNotServedOverTCP(t *testing.T) {
	source, err := os.ReadFile("socket_unix.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, forbidden := range []string{`"tcp"`, `"tcp4"`, `"tcp6"`, "ListenTCP"} {
		if strings.Contains(string(source), forbidden) {
			t.Errorf("socket_unix.go mentions %s; this endpoint is Unix-only", forbidden)
		}
	}
}
