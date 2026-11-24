package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBinaryFilesAreFlagged(t *testing.T) {
	options := Options{BinaryFiles: []BinaryFile{
		{Path: "logo.png", Bytes: 10, Format: "png", Verified: true},
		{Path: "run.dat", Bytes: 20},
		{Path: "old.dat", Bytes: 30, Deleted: true},
	}}
	var human bytes.Buffer
	if err := WriteAI(&human, FormatHuman, options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "could not verify: 2 of 3") || !strings.Contains(human.String(), "run.dat (20 bytes)") || !strings.Contains(human.String(), "old.dat (30 bytes, deleted)") {
		t.Errorf("human report: %s", human.String())
	}
	if strings.Contains(human.String(), "logo.png") {
		t.Errorf("a verified file was flagged: %s", human.String())
	}
	var machine bytes.Buffer
	if err := WriteAI(&machine, FormatJSON, options); err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		BinaryFiles []BinaryFile `json:"binaryFiles"`
	}
	if err := json.Unmarshal(machine.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.BinaryFiles) != 3 || !parsed.BinaryFiles[0].Verified || parsed.BinaryFiles[1].Verified || !parsed.BinaryFiles[2].Deleted {
		t.Errorf("json report: %s", machine.String())
	}
	var sarif bytes.Buffer
	if err := WriteAI(&sarif, FormatSARIF, options); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sarif.String(), "slopcop.binaryFiles") {
		t.Errorf("sarif report: %s", sarif.String())
	}
}

func TestUnverifiedBinaryFilesListIsBounded(t *testing.T) {
	var files []BinaryFile
	for i := range listedExcluded + 3 {
		files = append(files, BinaryFile{Path: fmt.Sprintf("file-%d.bin", i), Bytes: 1})
	}
	got := omittedFilesHuman(Options{BinaryFiles: files})
	if !strings.Contains(got, "... and 3 more") || strings.Contains(got, fmt.Sprintf("file-%d.bin", listedExcluded)) {
		t.Fatalf("human report: %s", got)
	}
}
