package applog

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// crashEnv, when set, turns a re-execution of this test binary into the
// subject of TestStartCapturesPanicInLog: a process that starts logging and
// then dies the way the app would in the field.
const crashEnv = "FILESYNC_APPLOG_CRASH_CHILD"

// TestStartCapturesPanicInLog is the whole point of the package: a panic on
// a goroutine nobody is watching has to end up in the file. It can only be
// tested from a separate process - the panic kills whoever runs it - so the
// test re-executes itself with crashEnv set.
func TestStartCapturesPanicInLog(t *testing.T) {
	if os.Getenv(crashEnv) != "" {
		path, err := Start()
		// Stdout is still the pipe back to the parent; only stderr was
		// redirected, which is exactly the split being asserted.
		fmt.Println("LOGPATH:" + path)
		if err != nil {
			fmt.Println("STARTERR:" + err.Error())
			return
		}
		fmt.Fprintln(os.Stderr, "a line written to stderr before the crash")
		done := make(chan struct{})
		go func() {
			defer close(done)
			panic("boom from a background goroutine")
		}()
		<-done
		return
	}

	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=TestStartCapturesPanicInLog", "-test.v")
	cmd.Env = append(os.Environ(),
		crashEnv+"=1",
		// One of these is what os.UserConfigDir reads, depending on the
		// platform; setting all three keeps the child out of the real
		// config directory everywhere.
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"AppData="+home,
	)
	out, err := cmd.Output()
	if err == nil {
		t.Fatal("child process exited cleanly; expected it to die from the panic")
	}

	var logPath string
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "LOGPATH:"); ok {
			logPath = rest
		}
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "STARTERR:"); ok {
			t.Fatalf("child could not start logging: %s", rest)
		}
	}
	if logPath == "" {
		t.Fatalf("child never reported a log path; stdout was:\n%s", out)
	}
	if !strings.HasPrefix(logPath, home) {
		t.Fatalf("child wrote outside its temporary home: %s", logPath)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading the child's log: %v", err)
	}
	logged := string(data)
	for _, want := range []string{
		"=== FileSync",                     // the per-launch header
		"a line written to stderr before",  // ordinary stderr writes
		"boom from a background goroutine", // the panic message
		"goroutine",                        // ...and its stack trace
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log is missing %q; it contains:\n%s", want, logged)
		}
	}
}

func TestOpenRotatedKeepsOnePreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, Name)

	// Under the cap: the existing file is appended to, not rotated.
	if err := os.WriteFile(path, []byte("first session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := openRotated(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "second session")
	f.Close()
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "first session") {
		t.Errorf("an under-cap log was not appended to: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, prevName)); !os.IsNotExist(err) {
		t.Error("an under-cap log should not have been rotated")
	}

	// At the cap: the old file becomes the previous one and the new one
	// starts empty, so the two together stay bounded.
	if err := os.WriteFile(path, make([]byte, maxLogBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err = openRotated(path)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(f, "third session")
	f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxLogBytes {
		t.Errorf("log was not rotated: still %d bytes", info.Size())
	}
	prev, err := os.Stat(filepath.Join(dir, prevName))
	if err != nil {
		t.Fatalf("previous log was not kept: %v", err)
	}
	if prev.Size() != maxLogBytes {
		t.Errorf("previous log = %d bytes, want the %d it was rotated at", prev.Size(), maxLogBytes)
	}
}
