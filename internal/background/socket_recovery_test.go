package background

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestControlSocketCrashChild(t *testing.T) {
	path := os.Getenv("PM_SOCKET_CRASH_FIXTURE")
	if path == "" {
		return
	}
	l, err := listenControl(path)
	if err != nil {
		os.Exit(7)
	}
	_ = l
	fmt.Println("ready")
	time.Sleep(time.Minute)
	os.Exit(9)
}
func TestControlSocketRecoversDeadProcess(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "pm-socket-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "private", "control.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestControlSocketCrashChild$")
	c.Env = append(os.Environ(), "PM_SOCKET_CRASH_FIXTURE="+path)
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Start(); err != nil {
		t.Fatal(err)
	}
	if ready, e := bufio.NewReader(out).ReadString('\n'); e != nil || ready != "ready\n" {
		_ = c.Process.Kill()
		_ = c.Wait()
		t.Fatalf("child not ready: %q %v", ready, e)
	}
	if err = c.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err = c.Wait(); err == nil {
		t.Fatal("child did not terminate abnormally")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal(err)
	}
	l, err := listenControl(path)
	if err != nil {
		t.Fatalf("dead listener recovery: %v", err)
	}
	defer l.Close()
	if _, err := listenControl(path); err == nil {
		t.Fatal("live listener stolen")
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestControlSocketCloseDoesNotRemoveReplacement(t *testing.T) {
	root, e := os.MkdirTemp("/tmp", "pm-replace-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(root, "private", "control.sock")
	l, e := listenControl(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(path); e != nil {
		t.Fatal(e)
	}
	replacement, e := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	defer replacement.Close()
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Lstat(path); e != nil {
		t.Fatal("replacement removed", e)
	}
	if _, e = listenControl(path); e == nil {
		t.Fatal("unlocked live listener stolen")
	}
	conn, e := net.Dial("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	conn.Close()
}
