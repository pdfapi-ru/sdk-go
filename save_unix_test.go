//go:build unix

package pdfapi

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestSavePermissionsLikeANormalFile(t *testing.T) {
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	path := filepath.Join(t.TempDir(), "out.pdf")
	if err := (&Result{Content: []byte("%PDF")}).Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o666&^umask); got != want {
		t.Fatalf("права %v, ждали %v", got, want)
	}
}
