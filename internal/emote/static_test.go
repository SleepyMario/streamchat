package emote

import (
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestStaticAssetConvertsAnimatedGIFOnce(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "twitch", "emotesv2_4c3b4ed516de493bbcd2df2f5d450f49.img")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	palette := color.Palette{color.Transparent, color.RGBA{G: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	first := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	second := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	for index := range first.Pix {
		first.Pix[index] = 1
		second.Pix[index] = 2
	}
	if err = gif.EncodeAll(file, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{5, 5}}); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	staticPath, err := staticAsset(path)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(staticPath) != ".png" || staticPath == path {
		t.Fatalf("static path=%q", staticPath)
	}
	staticFile, err := os.Open(staticPath)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(staticFile)
	_ = staticFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := color.RGBAModel.Convert(decoded.At(0, 0)).(color.RGBA); got.G != 255 || got.B != 0 {
		t.Fatalf("first frame color=%+v", got)
	}
	before, err := os.Stat(staticPath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := staticAsset(path)
	if err != nil || again != staticPath {
		t.Fatalf("again=%q err=%v", again, err)
	}
	after, err := os.Stat(staticPath)
	if err != nil || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("static preview was rewritten: before=%v after=%v err=%v", before.ModTime(), after.ModTime(), err)
	}
}

func TestStaticAssetLeavesNonGIFFilesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "7.img")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\nstreamchat"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := staticAsset(path)
	if err != nil || got != path {
		t.Fatalf("path=%q err=%v", got, err)
	}
}
