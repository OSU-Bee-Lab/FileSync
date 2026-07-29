package audio

import (
	"io"
	"maps"
	"slices"
	"testing"
)

// isolateRegistry restores the driver registry after the test. The registry is
// package-global and panics on a duplicate extension, so a test that registers
// into it has to put it back or it can only ever run once per process.
func isolateRegistry(t *testing.T) {
	t.Helper()
	mu.Lock()
	oldByExt, oldDrivers := maps.Clone(byExt), slices.Clone(drivers)
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		byExt, drivers = oldByExt, oldDrivers
		mu.Unlock()
	})
}

type stubDriver struct {
	name string
	exts []string
}

func (d stubDriver) Name() string                    { return d.name }
func (d stubDriver) Extensions() []string            { return d.exts }
func (d stubDriver) Open(io.Reader) (Decoder, error) { return nil, nil }

func TestRegistryMatchesExtensionsCaseInsensitively(t *testing.T) {
	isolateRegistry(t)
	Register(stubDriver{name: "stub", exts: []string{".stub"}})

	for _, name := range []string{"rec.stub", "REC.STUB", "a/b/260623_0900.Stub"} {
		if !CanPlay(name) {
			t.Errorf("CanPlay(%q) = false, want true", name)
		}
	}
	if CanPlay("rec.stubby") {
		t.Error("CanPlay matched a longer extension")
	}
	if CanPlay("noextension") {
		t.Error("CanPlay matched a file with no extension")
	}
	if d := DriverFor("rec.stub"); d == nil || d.Name() != "stub" {
		t.Errorf("DriverFor returned %v, want the stub driver", d)
	}
}

func TestRegisterPanicsOnDuplicateExtension(t *testing.T) {
	isolateRegistry(t)
	Register(stubDriver{name: "first", exts: []string{".dupe"}})
	defer func() {
		if recover() == nil {
			t.Fatal("registering a second driver for the same extension did not panic")
		}
	}()
	Register(stubDriver{name: "second", exts: []string{".dupe"}})
}
