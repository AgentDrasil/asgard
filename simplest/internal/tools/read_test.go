package tools

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AgentDrasil/asgard/simplest/internal/types"
)

func createTestPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 255, G: 0, B: 0, A: 255}}, image.Point{}, draw.Src)
	var buf bytes.Buffer
	err := png.Encode(&buf, img)
	require.NoError(t, err)
	return buf.Bytes()
}

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
	wantData := createTestPNG(t, 20, 20)
	writeFile(t, dir, "img.png", string(wantData))

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

func TestReadTool_ModelInjection_Registry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	reg := DefaultRegistry(dir)

	tool, ok := reg.Get("read")
	require.True(t, ok)
	readTool, ok := tool.(*ReadTool)
	require.True(t, ok)

	assert.Nil(t, readTool.model)

	m := &types.Model{
		ID:    "gpt-4o",
		Input: []string{"text", "image"},
	}
	reg.SetModel(m)

	assert.Equal(t, m, readTool.model)
}

func TestReadTool_NonVisionModel_Placeholder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pngData := createTestPNG(t, 20, 20)
	writeFile(t, dir, "photo.png", string(pngData))

	tool := NewReadTool(dir)
	nonVisionModel := &types.Model{
		ID:    "text-only-model",
		Input: []string{"text"},
	}
	tool.SetModel(nonVisionModel)

	res := mustExec(t, tool, `{"path":"photo.png"}`)
	require.Len(t, res.Content, 1)

	tc, ok := res.Content[0].(types.TextContent)
	require.True(t, ok)
	assert.Equal(t, "[image omitted: model text-only-model does not support images]", tc.Text)
}

func TestReadTool_VisionModel_LargeImageResized(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	largePNG := createTestPNG(t, 200, 100)
	writeFile(t, dir, "large.png", string(largePNG))

	tool := NewReadTool(dir)
	visionModel := &types.Model{
		ID:    "vision-model",
		Input: []string{"text", "image"},
		InputLimits: &types.ModelInputLimits{
			Images: &types.ModelImageInputLimits{
				Resize: &types.ModelImageResizeOptions{
					MaxWidth:  50,
					MaxHeight: 50,
				},
			},
		},
	}
	tool.SetModel(visionModel)

	res := mustExec(t, tool, `{"path":"large.png"}`)
	require.Len(t, res.Content, 2)

	note, ok := res.Content[0].(types.TextContent)
	require.True(t, ok)
	assert.Contains(t, note.Text, "Read image file")

	img, ok := res.Content[1].(types.ImageContent)
	require.True(t, ok)

	decoded, err := base64.StdEncoding.DecodeString(img.Data)
	require.NoError(t, err)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
	require.NoError(t, err)

	assert.LessOrEqual(t, cfg.Width, 50)
	assert.LessOrEqual(t, cfg.Height, 50)
}
