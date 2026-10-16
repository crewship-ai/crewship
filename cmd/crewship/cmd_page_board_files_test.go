package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestPageBoardCLIImageUploadAndDownload(t *testing.T) {
	var pixels bytes.Buffer
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	picture.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&pixels, picture); err != nil {
		t.Fatal(err)
	}
	stub := pageStub(t)
	resetPageBoardCommand(t)
	stub.OnPost("/api/v1/pages/plan/board/files", func(r *http.Request, body []byte) (int, []byte, string) {
		if !bytes.Equal(body, pixels.Bytes()) || r.Header.Get("Content-Type") != "image/png" || r.URL.Query().Get("base_seq") != "3" || r.URL.Query().Get("mutation_id") != "upload-same" {
			t.Error("lost raw pixels, MIME or mutation identity")
		}
		return 201, []byte(`{"file_id":"image","seq":4}`), "application/json"
	})
	if _, err := runPageCLI(t, pixels.String(), "page", "board", "upload", "plan", "--file", "-", "--base-seq", "3", "--mutation-id", "upload-same"); err != nil {
		t.Fatal(err)
	}
	stub.OnGet("/api/v1/pages/plan/board/files/image", func(_ *http.Request, _ []byte) (int, []byte, string) { return 200, pixels.Bytes(), "image/png" })
	output := filepath.Join(t.TempDir(), "picture.png")
	if _, err := runPageCLI(t, "", "page", "board", "download", "plan", "image", "--output", output); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(output)
	if err != nil || !bytes.Equal(saved, pixels.Bytes()) {
		t.Fatal("download changed bytes", err)
	}
	resetPageBoardCommand(t)
	if _, err := runPageCLI(t, "", "page", "board", "download", "plan", "image", "--output", output); err == nil {
		t.Fatal("overwrote existing file")
	}
}
