package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A saved Books set from the old preset becomes Ebooks, folders and id kept;
// one the operator renamed or re-keyed is theirs and stays as it is.
func TestBooksSetBecomesEbooks(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"folderSets":[
		{"id":"books","name":"Books","match":"BOOK","folders":["/mars/sea/eBooks"]},
		{"id":"comics","name":"Comics","match":"COMIC","folders":["/c"]}]}`), 0o644)
	sets := loadSettings(root).FolderSets
	if sets[0].Name != "Ebooks" || sets[0].Match != "EBOOK|BOOK" || sets[0].ID != "books" || sets[0].Folders[0] != "/mars/sea/eBooks" {
		t.Errorf("Books set: %+v", sets[0])
	}
	if sets[1].Name != "Comics" {
		t.Errorf("an unrelated set changed: %+v", sets[1])
	}

	os.WriteFile(filepath.Join(root, "settings.json"), []byte(`{"folderSets":[
		{"id":"books","name":"Books","match":"BOOK|NOVEL","folders":["/b"]}]}`), 0o644)
	if s := loadSettings(root).FolderSets[0]; s.Name != "Books" || s.Match != "BOOK|NOVEL" {
		t.Errorf("a set the operator changed was rewritten: %+v", s)
	}
}
