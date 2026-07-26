package bundlev2

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSealAndOpenRoundTrip(t *testing.T) {
	source := filepath.Join(t.TempDir(), "incident")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "inbound.log"), []byte("{\"type\":\"InboundRequest\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "incident.json"), []byte("{\"captured_at\":\"2026-01-01T00:00:00Z\"}"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "incident.inferno")
	passphrase := []byte("correct horse battery staple")
	if err := SealDirectory(source, bundle, passphrase); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == `{"type":"InboundRequest"}` {
		t.Fatal("bundle contains plaintext")
	}
	destination := filepath.Join(t.TempDir(), "opened")
	if err := OpenToDirectory(bundle, destination, passphrase); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "inbound.log"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "{\"type\":\"InboundRequest\"}\n" {
		t.Fatalf("roundtrip content=%q", got)
	}
	info, _ := os.Stat(filepath.Join(destination, "inbound.log"))
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("extracted mode=%o", info.Mode().Perm())
	}
}

func TestOpenRejectsWrongPassphrase(t *testing.T) {
	source := filepath.Join(t.TempDir(), "incident")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "inbound.log"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(t.TempDir(), "incident.inferno")
	if err := SealDirectory(source, bundle, []byte("correct passphrase")); err != nil {
		t.Fatal(err)
	}
	if err := OpenToDirectory(bundle, filepath.Join(t.TempDir(), "opened"), []byte("incorrect passphrase")); err == nil {
		t.Fatal("expected authenticated decryption failure")
	}
}
