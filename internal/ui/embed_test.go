package ui

import (
	"io/fs"
	"strings"
	"testing"
)

func TestFSContainsBuiltWebApplication(t *testing.T) {
	root := FS()
	if root == nil {
		t.Fatal("FS() is nil; run the web build before compiling the binary")
	}

	for _, path := range []string{
		"index.html",
		"manifest.webmanifest",
		"icon-192.png",
		"icon-512.png",
	} {
		if _, err := fs.Stat(root, path); err != nil {
			t.Errorf("embedded asset %q: %v", path, err)
		}
	}

	index, err := fs.ReadFile(root, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), "<title>LLMBeam</title>") {
		t.Fatal("embedded index does not contain LLMBeam metadata")
	}
}
