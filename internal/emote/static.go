package emote

import (
	"bytes"
	"errors"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
)

const maxStaticImagePixels = 16 * 1024 * 1024

// staticAsset converts animated GIF input to a persistent first-frame PNG.
// Kitty's graphics protocol accepts PNG data directly, and terminal emotes use
// a stable one-row preview rather than starting an independent animation.
func staticAsset(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("read cached emote")
	}
	if len(data) < 6 || (!bytes.Equal(data[:6], []byte("GIF87a")) && !bytes.Equal(data[:6], []byte("GIF89a"))) {
		return path, nil
	}
	target := path[:len(path)-len(filepath.Ext(path))] + ".static.png"
	if sourceInfo, sourceErr := os.Stat(path); sourceErr == nil {
		if targetInfo, targetErr := os.Stat(target); targetErr == nil && targetInfo.Mode().IsRegular() && targetInfo.Size() > 0 && !targetInfo.ModTime().Before(sourceInfo.ModTime()) {
			return target, nil
		}
	}
	config, err := gif.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > maxStaticImagePixels/config.Height {
		return "", errors.New("decode cached GIF dimensions")
	}
	frame, err := gif.Decode(bytes.NewReader(data))
	if err != nil {
		return "", errors.New("decode cached GIF frame")
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".static-*.tmp")
	if err != nil {
		return "", errors.New("create static emote file")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		err = png.Encode(temporary, frame)
	}
	if syncErr := temporary.Sync(); err == nil {
		err = syncErr
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", errors.New("write static emote file")
	}
	if err = os.Rename(temporaryPath, target); err != nil {
		return "", errors.New("store static emote file")
	}
	return target, nil
}
