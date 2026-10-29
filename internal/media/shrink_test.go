package media

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"testing"
)

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withOrientation inserts an EXIF segment holding only an orientation tag.
func withOrientation(jpegData []byte, o uint16) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1, 0x01, 0x12, 0, 3, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(tiff[18:], o)
	segment := append([]byte("Exif\x00\x00"), tiff...)
	header := []byte{0xff, 0xe1, 0, 0}
	binary.BigEndian.PutUint16(header[2:], uint16(len(segment)+2))
	return append(append(append([]byte{}, jpegData[:2]...), append(header, segment...)...), jpegData[2:]...)
}

func decodeSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" {
		t.Fatalf("result is not a JPEG: %s %v", format, err)
	}
	return cfg.Width, cfg.Height
}

func TestShrinkScalesDownAndKeepsSmallImages(t *testing.T) {
	noise := image.NewRGBA(image.Rect(0, 0, 3000, 1500))
	rand.New(rand.NewSource(1)).Read(noise.Pix)
	var big bytes.Buffer
	if err := png.Encode(&big, noise); err != nil {
		t.Fatal(err)
	}
	out, err := Shrink(big.Bytes(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, out); w != 1500 || h != 750 {
		t.Fatalf("scaled to %dx%d", w, h)
	}
	if len(out) >= big.Len()/2 {
		t.Fatalf("not smaller: %d from %d", len(out), big.Len())
	}
	small, err := Shrink(encodeJPEG(t, image.NewRGBA(image.Rect(0, 0, 100, 60))), 2048)
	if err != nil {
		t.Fatal(err)
	}
	if w, h := decodeSize(t, small); w != 100 || h != 60 {
		t.Fatalf("small image resized to %dx%d", w, h)
	}
}

func TestShrinkAppliesRotationTags(t *testing.T) {
	// Left half red, right half blue.
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 20 {
				c = color.RGBA{B: 255, A: 255}
			}
			src.Set(x, y, c)
		}
	}
	for _, tc := range []struct {
		tag           uint16
		width, height int
		// Which color is at the top-left once displayed upright.
		topLeftRed bool
	}{
		{1, 40, 20, true}, {2, 40, 20, false}, {3, 40, 20, false},
		{6, 20, 40, true}, {8, 20, 40, false},
	} {
		out, err := Shrink(withOrientation(encodeJPEG(t, src), tc.tag), 2048)
		if err != nil {
			t.Fatal(err)
		}
		if w, h := decodeSize(t, out); w != tc.width || h != tc.height {
			t.Fatalf("tag %d: %dx%d", tc.tag, w, h)
		}
		img, _ := jpeg.Decode(bytes.NewReader(out))
		r, _, b, _ := img.At(2, 2).RGBA()
		if (r > b) != tc.topLeftRed {
			t.Fatalf("tag %d: wrong corner (r=%d b=%d)", tc.tag, r, b)
		}
	}
}

func TestShrinkCompositesTransparencyOnWhiteAndRefusesNonImages(t *testing.T) {
	clear := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	var b bytes.Buffer
	if err := png.Encode(&b, clear); err != nil {
		t.Fatal(err)
	}
	out, err := Shrink(b.Bytes(), 2048)
	if err != nil {
		t.Fatal(err)
	}
	img, _ := jpeg.Decode(bytes.NewReader(out))
	if r, g, bl, _ := img.At(4, 4).RGBA(); r>>8 < 250 || g>>8 < 250 || bl>>8 < 250 {
		t.Fatal("transparent area is not white")
	}
	for _, data := range [][]byte{nil, []byte("not an image"), []byte("RIFF\x00\x00\x00\x00WEBPVP8 ")} {
		if _, err := Shrink(data, 2048); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
}

func TestAHEICPhotoIsReadAndReturnedAsAJPEG(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.heic")
	if err != nil {
		t.Fatal(err)
	}
	if !IsHEIC(data) || IsHEIC(encodeJPEG(t, image.NewRGBA(image.Rect(0, 0, 4, 4)))) || IsHEIC([]byte("short")) {
		t.Fatal("HEIC not told apart from other files")
	}
	out, err := Shrink(data, 2048)
	if err != nil {
		t.Fatal(err)
	}
	w, h := decodeSize(t, out)
	if w < 1 || h < 1 {
		t.Fatalf("empty result %dx%d", w, h)
	}
	small, err := Shrink(data, 16)
	if err != nil {
		t.Fatal(err)
	}
	if sw, sh := decodeSize(t, small); sw > 16 || sh > 16 {
		t.Fatalf("not reduced: %dx%d", sw, sh)
	}
	// Cut short or not a photo at all, it is refused, not read as garbage.
	for name, bad := range map[string][]byte{"truncated": data[:len(data)/3], "header only": data[:16], "not a photo": append(append([]byte{}, data[:12]...), make([]byte, 64)...)} {
		if _, err := Shrink(bad, 2048); err == nil || err.Error() != "image_unreadable" {
			t.Errorf("%s: %v", name, err)
		}
	}
}
