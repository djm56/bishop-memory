package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// TestGracefulShutdown_OnSIGTERM is the Step 4 brief's required
// observation for CRITICAL 2 (Review C): exercise the signal path
// end-to-end, not just read the source. It builds the real memoryd
// binary, runs it as a subprocess against a scratch database, waits
// for it to report healthy, sends SIGTERM (the exact signal
// launchctl/systemctl send on a normal stop — the scenario the bug
// report is about), and asserts the process exits on its own within
// the graceful-shutdown window WITH a clean (zero) exit code, rather
// than needing to be force-killed. A pre-fix build (bare `defer
// db.Close()`, no signal.NotifyContext) would have failed this test:
// SIGTERM's default disposition for a Go program with no signal
// handler installed is immediate termination, which this test would
// observe as "process did not exit cleanly / needed SIGKILL" — so this
// is a check that could have failed, not one guaranteed to pass.
//
// The subprocess is guaranteed not to outlive the test: t.Cleanup
// force-kills it unconditionally, so even a test failure partway
// through (a t.Fatal before the process exits on its own) cannot leave
// it running.
func TestGracefulShutdown_OnSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping subprocess integration test in -short mode")
	}

	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed to resolve this test file's path")
	}
	// thisFile is .../bishop-memory/cmd/memoryd/main_test.go; the repo
	// root (where db/schema.sql and go.mod live) is two directories up.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")

	if _, err := os.Stat(filepath.Join(repoRoot, "db", "schema.sql")); err != nil {
		t.Fatalf("resolved repo root %q does not contain db/schema.sql (%v) — path derivation is wrong", repoRoot, err)
	}

	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "memoryd-test-bin")
	dbPath := filepath.Join(tmpDir, "test.db")

	buildCtx, buildCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer buildCancel()
	buildCmd := exec.CommandContext(buildCtx, "go", "build", "-o", binPath, ".")
	buildCmd.Dir = filepath.Join(repoRoot, "cmd", "memoryd")
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build memoryd failed: %v\n%s", err, out)
	}

	port, err := freeTCPPort()
	if err != nil {
		t.Fatalf("find a free TCP port: %v", err)
	}

	cmd := exec.Command(binPath)
	cmd.Dir = repoRoot // relative "db/schema.sql" in main.go resolves from here
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("PORT=%d", port),
		"DB_PATH="+dbPath,
		"APP_ENV=development",
	)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start memoryd subprocess: %v", err)
	}

	// Guarantee cleanup even if the test fails before the process exits
	// on its own: force-kill unconditionally. Killing an already-exited
	// process is a harmless no-op error we ignore.
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	healthURL := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	if err := waitForHealthy(healthURL, 10*time.Second); err != nil {
		t.Fatalf("memoryd did not become healthy: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("memoryd did not exit cleanly after SIGTERM: %v", err)
		}
		// A clean (nil) Wait() error means exit code 0 — the process
		// ran its graceful-shutdown path (srv.Shutdown + db.Close) and
		// returned from main() normally, rather than being torn down
		// by the OS's default SIGTERM disposition.
	case <-time.After(shutdownTimeout + 5*time.Second):
		t.Fatal("memoryd did not exit within the graceful-shutdown window after SIGTERM — signal handling is not wired up")
	}
}

// freeTCPPort asks the OS for an ephemeral port by binding to :0 and
// immediately releasing it. There is a small, unavoidable race between
// releasing the port here and memoryd binding it, but it is the
// standard technique for this kind of test and failures from it are
// rare enough not to warrant retry logic here.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForHealthy polls url until it returns HTTP 200 or the deadline
// elapses.
func waitForHealthy(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for %s (last error: %v)", timeout, url, lastErr)
}
