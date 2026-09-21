package agentcli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/P0me1oo/YZ-Agent/internal/panel"
	"github.com/gofrs/uuid/v5"
)

func remoteFixture(t *testing.T) (string, panel.MachineOperation) {
	t.Helper()
	old := defaultInstallRoot
	defaultInstallRoot = t.TempDir()
	t.Cleanup(func() { defaultInstallRoot = old })
	id, err := uuid.NewV4()
	if err != nil {
		t.Fatal(err)
	}
	return RemoteKey("https://panel.example.test", 7), panel.MachineOperation{ID: id.String(), Action: "restart", ExpiresAt: time.Now().Add(15 * time.Minute).Unix()}
}

func TestRemotePersistsBeforeLaunchAndDeduplicatesAfterReload(t *testing.T) {
	key, operation := remoteFixture(t)
	calls := 0
	launch := func(key string, command panel.MachineOperation) error {
		calls++
		if got := RemoteResult(key); got == nil || got.ID != command.ID || got.Status != "running" {
			t.Fatal("operation was not persisted before launch")
		}
		return nil
	}
	for range 3 {
		if err := startRemote(key, operation, func() bool { return true }, launch); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("launched %d times", calls)
	}
	if RemoteResult(RemoteKey("https://another.example.test", 7)) != nil {
		t.Fatal("result leaked to another panel")
	}
}

func TestRemoteFailureAndExpiredCommand(t *testing.T) {
	key, operation := remoteFixture(t)
	if err := startRemote(key, operation, func() bool { return true }, func(string, panel.MachineOperation) error { return errors.New("local detail") }); err != nil {
		t.Fatal(err)
	}
	if got := RemoteResult(key); got.Status != "failed" || got.Error != "launch_failed" {
		t.Fatalf("unexpected result: %+v", got)
	}
	operation.ID = "invalid"
	if err := StartRemote(key, operation); err == nil {
		t.Fatal("invalid ID accepted")
	}
	id, _ := uuid.NewV4()
	operation.ID, operation.ExpiresAt = id.String(), time.Now().Unix()-1
	if err := StartRemote(key, operation); err == nil {
		t.Fatal("expired command accepted")
	}
}

func TestRemoteLateResultDoesNotOverwriteNewOperation(t *testing.T) {
	key, first := remoteFixture(t)
	first.Status = "running"
	if err := activateRemote(&remoteRecord{Key: key, Operation: first}); err != nil {
		t.Fatal(err)
	}
	second := first
	id, _ := uuid.NewV4()
	second.ID = id.String()
	if err := activateRemote(&remoteRecord{Key: key, Operation: second}); err != nil {
		t.Fatal(err)
	}
	first.Status = "succeeded"
	if err := saveRemote(&remoteRecord{Key: key, Operation: first}); err != nil {
		t.Fatal(err)
	}
	if got := RemoteResult(key); got.ID != second.ID || got.Status != "running" {
		t.Fatal("late result overwrote the active operation")
	}
	if err := startRemote(key, first, func() bool { return true }, func(string, panel.MachineOperation) error { t.Fatal("old operation replayed"); return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteUnsupportedAndInterruptedWorker(t *testing.T) {
	key, operation := remoteFixture(t)
	if err := startRemote(key, operation, func() bool { return false }, func(string, panel.MachineOperation) error { t.Fatal("unsupported worker launched"); return nil }); err != nil {
		t.Fatal(err)
	}
	if RemoteResult(key).Error != "unsupported" {
		t.Fatal("missing unsupported result")
	}
	operation.Status, operation.ExpiresAt = "running", time.Now().Unix()-1
	if err := saveRemote(&remoteRecord{Key: key, Operation: operation}); err != nil {
		t.Fatal(err)
	}
	if RemoteResult(key).Error != "timeout" {
		t.Fatal("interrupted operation did not expire")
	}
}

func TestLatestReleaseIsResolvedOnceAndRejectsPrerelease(t *testing.T) {
	old := downloadBase
	t.Cleanup(func() { downloadBase = old })
	for _, version := range []string{"v1.16.0", "v1.16.0-beta.1"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/latest" {
					http.Redirect(w, r, "/tag/"+version, http.StatusFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			downloadBase = server.URL
			got, err := latestStableRelease(context.Background())
			if version == "v1.16.0" && (err != nil || got != version) {
				t.Fatalf("resolve: %s %v", got, err)
			}
			if version != "v1.16.0" && err == nil {
				t.Fatal("prerelease accepted")
			}
		})
	}
}

func TestRemoteWorkerExecutionAndFailure(t *testing.T) {
	for _, action := range []string{"restart", "upgrade"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%t", action, fail), func(t *testing.T) {
				key, operation := remoteFixture(t)
				operation.Action, operation.Status = action, "running"
				if err := activateRemote(&remoteRecord{Key: key, Operation: operation}); err != nil {
					t.Fatal(err)
				}
				locked, installationLocked, calls := false, false, 0
				ops := remoteExecution{
					lock:             func() (func(), error) { locked = true; return func() { locked = false }, nil },
					installationLock: func() (func(), error) { installationLocked = true; return func() { installationLocked = false }, nil },
					executable:       func() (string, error) { return "installed-agent", nil },
					latest:           func(context.Context) (string, error) { return "v1.16.0", nil },
					run: func(name string, args ...string) error {
						calls++
						if !locked || name != "installed-agent" {
							t.Fatal("worker ran without lock or wrong executable")
						}
						want := []string{"upgrade", "--version", "v1.16.0"}
						if action == "restart" {
							want = []string{"service", "restart"}
							if !installationLocked {
								t.Fatal("restart did not lock installation")
							}
						}
						if !reflect.DeepEqual(args, want) {
							t.Fatalf("args: %v", args)
						}
						if fail {
							return errors.New("private execution detail")
						}
						return nil
					},
				}
				if err := runRemoteWith([]string{key, operation.ID}, ops); err != nil {
					t.Fatal(err)
				}
				want := "succeeded"
				if fail {
					want = "failed"
				}
				if got := RemoteResult(key); got.Status != want || (fail && got.Error != "execution_failed") {
					t.Fatalf("result: %+v", got)
				}
				if locked || installationLocked || calls != 1 {
					t.Fatal("execution lifecycle did not finish")
				}
				if err := runRemoteWith([]string{key, operation.ID}, ops); err == nil {
					t.Fatal("completed command replayed")
				}
			})
		}
	}
}
