package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBridgeLockIsSingleInstance covers the guard added because the bridge can
// now be started two ways — by the service manager and by a direct spawn. Two
// of them on one store would drive the same WhatsApp session at once.
func TestBridgeLockIsSingleInstance(t *testing.T) {
	t.Setenv("WHATSAPP_ASSISTANT_HOME", t.TempDir())

	if bridgeRunning() {
		t.Fatal("nothing has started yet, but the lock reads as held")
	}

	release, err := claimBridgeLock()
	if err != nil {
		t.Fatalf("first claim failed: %v", err)
	}
	if !bridgeRunning() {
		t.Error("lock is held but bridgeRunning() says otherwise")
	}
	// A second bridge must be refused immediately, not queued behind the first.
	done := make(chan error, 1)
	go func() {
		_, err := claimBridgeLock()
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a second bridge acquired the lock while the first held it")
		}
	case <-time.After(3 * time.Second):
		t.Error("second claim blocked instead of failing fast")
	}

	release()
	if bridgeRunning() {
		t.Error("lock still reads as held after release")
	}
}

// TestDescribeServiceNamesTheMissingStage is the regression guard for the bug
// this all came from: a registered task was reported as "installed and running"
// even though no process had ever started and no store existed.
func TestDescribeServiceNamesTheMissingStage(t *testing.T) {
	registeredButDead := serviceState{TaskRegistered: true}
	got := describeService(registeredButDead)

	if !strings.Contains(got, "NOT running") {
		t.Errorf("a dead sync process must be reported as not running:\n%s", got)
	}
	if !strings.Contains(got, "NOT created") {
		t.Errorf("a missing store must be reported as missing:\n%s", got)
	}

	// And the healthy case must not shout about problems it does not have.
	healthy := serviceState{
		TaskRegistered: true, ProcessRunning: true, StoreExists: true,
		HeartbeatAt: time.Now(), Connected: true, Linked: true,
	}
	got = describeService(healthy)
	if strings.Contains(got, "NOT") {
		t.Errorf("healthy state reported a problem:\n%s", got)
	}
}

// TestFailureDetailReportsAMissingLog: "no log" is itself the finding, and the
// report has to say so rather than staying silent about it.
func TestFailureDetailReportsAMissingLog(t *testing.T) {
	t.Setenv("WHATSAPP_ASSISTANT_HOME", t.TempDir())
	got := failureDetail()
	if !strings.Contains(got, "No bridge log") {
		t.Errorf("a missing bridge log must be called out:\n%s", got)
	}
	if !strings.Contains(got, "MISSING") {
		t.Errorf("a missing bridge binary must be called out:\n%s", got)
	}
}

func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	var sb strings.Builder
	for i := 0; i < 500; i++ {
		sb.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}
	got := tailFile(path, 10)
	if n := len(strings.Split(got, "\n")); n != 10 {
		t.Errorf("asked for 10 lines, got %d", n)
	}
	if tailFile(filepath.Join(dir, "nope"), 10) != "" {
		t.Error("a missing file should tail to empty, not to something")
	}
}
