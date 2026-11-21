package agentcli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

func TestOpenRCManagedProcess(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		edit func(t *testing.T, root string)
		want bool
	}{
		{name: "supervised Agent", want: true},
		{
			name: "direct Agent process record",
			edit: func(t *testing.T, root string) {
				writeOpenRCIdentityFixture(t, root, "run/yz-agent.pid", strconv.Itoa(os.Getpid()))
			},
			want: true,
		},
		{
			name: "unrelated supervisor",
			edit: func(t *testing.T, root string) {
				writeOpenRCIdentityFixture(t, root, "run/yz-agent.pid", "2147483647")
			},
		},
		{
			name: "another service child",
			edit: func(t *testing.T, root string) {
				writeOpenRCIdentityFixture(t, root, "run/openrc/options/yz-agent/child_pid", "2147483647")
			},
		},
		{
			name: "missing service child record",
			edit: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "run/openrc/options/yz-agent/child_pid")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "parent is not supervise-daemon",
			edit: func(t *testing.T, root string) {
				name := filepath.Join(root, "proc", strconv.Itoa(os.Getppid()), "exe")
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				writeOpenRCIdentityFixture(t, root, "other-parent", "unrelated executable")
				if err := os.Symlink(filepath.Join(root, "other-parent"), name); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing parent executable",
			edit: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "proc", strconv.Itoa(os.Getppid()), "exe")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing supervise-daemon installation",
			edit: func(t *testing.T, root string) {
				if err := os.Remove(filepath.Join(root, "supervise-daemon")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "writable supervise-daemon executable",
			edit: func(t *testing.T, root string) {
				if err := os.Chmod(filepath.Join(root, "supervise-daemon"), 0o777); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Agent pidfile symlink",
			edit: func(t *testing.T, root string) {
				name := filepath.Join(root, "run/yz-agent.pid")
				if err := os.Rename(name, name+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+".target", name); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "writable supervisor record",
			edit: func(t *testing.T, root string) {
				if err := os.Chmod(filepath.Join(root, "run/yz-agent.pid"), 0o666); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "writable child record",
			edit: func(t *testing.T, root string) {
				if err := os.Chmod(filepath.Join(root, "run/openrc/options/yz-agent/child_pid"), 0o666); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "malformed child record",
			edit: func(t *testing.T, root string) {
				writeOpenRCIdentityFixture(t, root, "run/openrc/options/yz-agent/child_pid", "invalid")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeOpenRCIdentityFixture(t, root, "run/yz-agent.pid", strconv.Itoa(os.Getppid())+"\n")
			writeOpenRCIdentityFixture(t, root, "run/openrc/options/yz-agent/child_pid", strconv.Itoa(os.Getpid())+"\n")
			writeOpenRCIdentityFixture(t, root, "supervise-daemon", "supervisor fixture")
			parent := filepath.Join(root, "proc", strconv.Itoa(os.Getppid()))
			if err := os.MkdirAll(parent, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "supervise-daemon"), filepath.Join(parent, "exe")); err != nil {
				t.Fatal(err)
			}
			if tc.edit != nil {
				tc.edit(t, root)
			}
			got := openRCManagedProcessAt(
				"yz-agent",
				filepath.Join(root, "run"),
				filepath.Join(root, "proc"),
				filepath.Join(root, "supervise-daemon"),
			)
			if got != tc.want {
				t.Fatalf("remote management available = %t, want %t", got, tc.want)
			}
		})
	}
}

func writeOpenRCIdentityFixture(t *testing.T, root, name, data string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadOpenRCPIDRejectsUnsafeRecords(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		data string
	}{
		{name: "empty"},
		{name: "zero", data: "0\n"},
		{name: "negative", data: "-1\n"},
		{name: "malformed", data: "123\n456\n"},
		{name: "oversized", data: strings.Repeat("1", 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			name := filepath.Join(t.TempDir(), "record")
			if err := os.WriteFile(name, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readOpenRCPID(name); err == nil {
				t.Fatal("unsafe process record accepted")
			}
		})
	}

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeOpenRCIdentityFixture(t, root, "target", strconv.Itoa(os.Getpid()))
		link := filepath.Join(root, "record")
		if err := os.Symlink(filepath.Join(root, "target"), link); err != nil {
			t.Fatal(err)
		}
		if _, err := readOpenRCPID(link); err == nil {
			t.Fatal("symlink process record accepted")
		}
	})

	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		if _, err := readOpenRCPID(t.TempDir()); err == nil {
			t.Fatal("directory accepted as process record")
		}
	})

	t.Run("named pipe", func(t *testing.T) {
		t.Parallel()
		name := filepath.Join(t.TempDir(), "record")
		if err := syscall.Mkfifo(name, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readOpenRCPID(name); err == nil {
			t.Fatal("named pipe accepted as process record")
		}
	})

	t.Run("another owner", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() != 0 {
			t.Skip("changing record ownership requires root")
		}
		name := filepath.Join(t.TempDir(), "record")
		if err := os.WriteFile(name, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(name, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := readOpenRCPID(name); err == nil {
			t.Fatal("another user's process record accepted")
		}
	})
}
