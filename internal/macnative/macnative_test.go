//go:build darwin

package macnative

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsNewerVersion(t *testing.T) {
	cases := []struct {
		cand, cur string
		want      bool
	}{
		{"0.1.9", "0.1.8", true},
		{"0.2.0", "0.1.99", true},
		{"0.1.8", "0.1.8", false},
		{"0.1.8-rc.1", "0.1.8", false},
		{"0.1.8", "0.1.8-rc.1", true},
		{"0.1.8-rc.10", "0.1.8-rc.2", true},
		{"0.1.8-rc.beta", "0.1.8-rc.2", true},
		{"0.1.8-rc.02", "0.1.8-rc.1", false},
		{"invalid", "0.1.8", false},
	}
	for _, tc := range cases {
		if got := IsNewerVersion(tc.cand, tc.cur); got != tc.want {
			t.Errorf("IsNewerVersion(%q, %q) = %v, want %v", tc.cand, tc.cur, got, tc.want)
		}
	}
}

func TestKeyIdentityAndVirtualKey(t *testing.T) {
	k, c := KeyIdentity(0, "a")
	if k != "a" || c != "KeyA" {
		t.Fatalf("KeyIdentity(0, a) = (%q, %q)", k, c)
	}
	k, c = KeyIdentity(55, "")
	if k != "Meta" || c != "MetaLeft" {
		t.Fatalf("KeyIdentity(55, empty) = (%q, %q)", k, c)
	}
	if kc, ok := VirtualKey("KeyC", "c", true); !ok || kc != 8 {
		t.Fatalf("VirtualKey(KeyC, c, true) = (%d, %v)", kc, ok)
	}
	if kc, ok := VirtualKey("", "?", true); !ok || kc != 44 {
		t.Fatalf("VirtualKey('', ?, true) = (%d, %v)", kc, ok)
	}
}

func TestFileSelectionStore(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(f1, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(f1, link); err != nil {
		t.Fatal(err)
	}

	store := NewFileSelectionStore()
	if _, _, err := store.RememberFiles(nil); err == nil {
		t.Fatal("expected empty selection error")
	}
	if _, _, err := store.RememberFiles([]string{dir}); err == nil {
		t.Fatal("expected directory rejection")
	}
	if _, _, err := store.RememberFiles([]string{link}); err == nil {
		t.Fatal("expected symlink rejection")
	}

	id, count, err := store.RememberFiles([]string{f1})
	if err != nil || count != 1 || id == "" {
		t.Fatalf("RememberFiles failed: id=%q count=%d err=%v", id, count, err)
	}
	paths, ok := store.Take(id)
	if !ok || len(paths) != 1 || paths[0] != f1 {
		t.Fatalf("Take failed: ok=%v paths=%v", ok, paths)
	}
	if _, ok := store.Take(id); ok {
		t.Fatal("expected selection to be consumed after Take")
	}
}

func TestParsePlist(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>io.github.lee-xeo.claude-cage</string>
<key>CageClientOnly</key><true/>
</dict></plist>`)
	m, err := ParsePlist(plist)
	if err != nil {
		t.Fatal(err)
	}
	if m["CFBundleIdentifier"] != BundleID || m["CageClientOnly"] != true {
		t.Fatalf("unexpected plist parse: %#v", m)
	}
}
