package server

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A file under files/ that cannot be read used to end the walk: the archive
// held salt.db and whatever came before it, and `salt backup` still printed
// success and exited 0 (#109). Now everything readable goes in and the
// unreadable file is named in the error.
func TestBackupNamesAnUnreadableFileAndKeepsTheRest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a file of mode 000")
	}
	s := testServer(t)
	dir := filepath.Join(s.dataDir, "files", "t")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "a.txt"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "a.txt"), 0o644) })

	dest := filepath.Join(t.TempDir(), "b.tar.gz")
	err := Backup(s.dataDir, dest)
	var gap *BackupIncomplete
	if !errors.As(err, &gap) {
		t.Fatalf("Backup = %v, want a BackupIncomplete", err)
	}
	if len(gap.Skipped) != 1 || gap.Skipped[0] != "files/t/a.txt" || !strings.Contains(err.Error(), "files/t/a.txt") {
		t.Errorf("skipped %v, message %q: want files/t/a.txt named", gap.Skipped, err)
	}
	got := archiveNames(t, dest)
	want := []string{"files/t/b.txt", "files/t/c.txt", DBFile}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("archive holds %v, want %v", got, want)
	}
}

// Without any uploads there is nothing to miss.
func TestBackupWithoutUploadsIsComplete(t *testing.T) {
	s := testServer(t)
	os.RemoveAll(filepath.Join(s.dataDir, "files"))
	dest := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := Backup(s.dataDir, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if got := archiveNames(t, dest); len(got) != 1 || got[0] != DBFile {
		t.Errorf("archive holds %v, want only %s", got, DBFile)
	}
}

// A backup that cannot be written leaves nothing behind that looks like one.
func TestBackupThatCannotBeWrittenFails(t *testing.T) {
	s := testServer(t)
	dest := filepath.Join(t.TempDir(), "missing", "b.tar.gz")
	if err := Backup(s.dataDir, dest); err == nil {
		t.Fatal("Backup into a directory that does not exist reported success")
	}
	if _, err := os.Stat(dest); err == nil {
		t.Error("a failed backup left an archive behind")
	}
}

func archiveNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading the archive: %v", err)
		}
		names = append(names, hdr.Name)
	}
	sort.Strings(names)
	return names
}
