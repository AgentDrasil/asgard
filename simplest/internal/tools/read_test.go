package tools

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func TestReadToolTruncationHint(t *testing.T) {
	dir := t.TempDir()
	var sb strings.Builder
	for i := 1; i <= 2500; i++ {
		sb.WriteString("line")
		sb.WriteString(strings.Repeat("x", 0))
		sb.WriteString("\n")
	}
	writeFile(t, dir, "big.txt", sb.String())

	res := mustExec(t, NewReadTool(dir), `{"path":"big.txt"}`)
	out := textOf(t, res)
	wantHint := "[Showing lines 1-2000 of 2501. Use offset=2001 to continue.]"
	if !strings.Contains(out, wantHint) {
		t.Errorf("output missing hint %q; tail: %q", wantHint, out[maxInt(0, len(out)-120):])
	}
	details, ok := res.Details.(*ReadToolDetails)
	if !ok || details.Truncation == nil {
		t.Fatalf("expected truncation details, got %+v", res.Details)
	}
	if !details.Truncation.Truncated || details.Truncation.TruncatedBy != "lines" {
		t.Errorf("unexpected truncation %+v", details.Truncation)
	}

	res = mustExec(t, NewReadTool(dir), `{"path":"big.txt","offset":2001}`)
	out = textOf(t, res)
	if !strings.HasPrefix(out, "line\n") && !strings.Contains(out, "line") {
		t.Errorf("offset read should return remaining lines, got %q", out[:minInt(80, len(out))])
	}
	if strings.Contains(out, "Showing lines") {
		t.Error("continuation read within limits should not carry a truncation hint")
	}
}

func TestReadToolOffsetBeyondEOF(t *testing.T) {
	tests := []struct {
		name string
		args string
	}{
		{"offset past end", `{"path":"small.txt","offset":10}`},
		{"offset exactly at line count+1 (split tail)", `{"path":"small.txt","offset":5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "small.txt", "a\nb\nc\n")
			_, err := execTool(t, NewReadTool(dir), tt.args)
			if err == nil {
				t.Fatal("expected error for offset beyond EOF")
			}
			if !strings.Contains(err.Error(), "beyond end of file") {
				t.Errorf("error = %q, want 'beyond end of file'", err.Error())
			}
		})
	}
}

func TestReadToolOffsetAndLimitWindow(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "w.txt", "one\ntwo\nthree\nfour\n")
	res := mustExec(t, NewReadTool(dir), `{"path":"w.txt","offset":2,"limit":2}`)
	out := textOf(t, res)
	want := "two\nthree\n\n[2 more lines in file. Use offset=4 to continue.]"
	if out != want {
		t.Errorf("windowed read = %q, want %q", out, want)
	}
}

func TestReadToolImageContent(t *testing.T) {
	dir := t.TempDir()
	png := string([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n', 0, 0, 0, 13})
	writeFile(t, dir, "img.png", png)
	wantData := []byte(png)

	res := mustExec(t, NewReadTool(dir), `{"path":"img.png"}`)
	if len(res.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(res.Content))
	}
	note, ok := res.Content[0].(types.TextContent)
	if !ok || !strings.Contains(note.Text, "image/png") {
		t.Errorf("note block = %+v, want text mentioning image/png", res.Content[0])
	}
	img, ok := res.Content[1].(types.ImageContent)
	if !ok {
		t.Fatalf("second block is %T, want ImageContent", res.Content[1])
	}
	if img.MimeType != "image/png" {
		t.Errorf("MimeType = %q, want image/png", img.MimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if string(decoded) != string(wantData) {
		t.Errorf("decoded image data mismatch: %d vs %d bytes", len(decoded), len(wantData))
	}
}

func TestReadToolMissingFileError(t *testing.T) {
	dir := t.TempDir()
	_, err := execTool(t, NewReadTool(dir), `{"path":"ghost.txt"}`)
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("err = %v, want not-exist error", err)
	}
}
