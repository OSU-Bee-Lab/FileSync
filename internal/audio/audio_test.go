package audio

import (
	"io"
	"testing"
)

type stubDriver struct {
	name string
	exts []string
}

func (d stubDriver) Name() string                    { return d.name }
func (d stubDriver) Extensions() []string            { return d.exts }
func (d stubDriver) Open(io.Reader) (Decoder, error) { return nil, nil }

func TestRegistryMatchesExtensionsCaseInsensitively(t *testing.T) {
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
	Register(stubDriver{name: "first", exts: []string{".dupe"}})
	defer func() {
		if recover() == nil {
			t.Fatal("registering a second driver for the same extension did not panic")
		}
	}()
	Register(stubDriver{name: "second", exts: []string{".dupe"}})
}
