package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
)

// maxPixels bounds the memory a decode may take: the header is read first, so a
// small file claiming enormous dimensions is refused before it is decoded.
const maxPixels = 64 << 20

// Shrink decodes a JPEG, PNG or GIF photo and returns it as a JPEG whose sides
// are at most maxSide pixels (it only ever reduces by whole-number factors, so
// the result may be smaller). Transparency becomes white and a JPEG's rotation
// tag is applied, since the re-encoded file no longer carries it. A GIF keeps
// only its first frame.
func Shrink(data []byte, maxSide int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxPixels || maxSide < 1 {
		return nil, errors.New("image_unreadable")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("image_unreadable")
	}
	b := src.Bounds()
	factor := max(1, (b.Dx()+maxSide-1)/maxSide, (b.Dy()+maxSide-1)/maxSide)
	w, h := max(1, b.Dx()/factor), max(1, b.Dy()/factor)
	scaled := image.NewRGBA(image.Rect(0, 0, w, h))
	n := uint64(factor * factor)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var r, g, bl, a uint64
			for dy := 0; dy < factor; dy++ {
				for dx := 0; dx < factor; dx++ {
					cr, cg, cb, ca := src.At(b.Min.X+x*factor+dx, b.Min.Y+y*factor+dy).RGBA()
					r, g, bl, a = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca)
				}
			}
			// Colors are alpha-premultiplied, so adding the missing alpha
			// composites the pixel onto white.
			white := 0xffff - a/n
			o := scaled.PixOffset(x, y)
			scaled.Pix[o], scaled.Pix[o+1], scaled.Pix[o+2], scaled.Pix[o+3] = byte((r/n+white)>>8), byte((g/n+white)>>8), byte((bl/n+white)>>8), 0xff
		}
	}
	var out bytes.Buffer
	if err = jpeg.Encode(&out, orient(scaled, exifOrientation(data)), &jpeg.Options{Quality: 85}); err != nil {
		return nil, errors.New("image_unreadable")
	}
	return out.Bytes(), nil
}

// orient applies an EXIF orientation (1 to 8) so the image displays upright.
func orient(src *image.RGBA, o int) draw.Image {
	if o < 2 || o > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var sx, sy int
			switch o {
			case 2:
				sx, sy = w-1-x, y
			case 3:
				sx, sy = w-1-x, h-1-y
			case 4:
				sx, sy = x, h-1-y
			case 5:
				sx, sy = y, x
			case 6:
				sx, sy = y, h-1-x
			case 7:
				sx, sy = w-1-y, h-1-x
			default:
				sx, sy = w-1-y, x
			}
			copy(dst.Pix[dst.PixOffset(x, y):dst.PixOffset(x, y)+4], src.Pix[src.PixOffset(sx, sy):src.PixOffset(sx, sy)+4])
		}
	}
	return dst
}

// exifOrientation reads the orientation tag of a JPEG, or 1 (upright) when the
// file has none or isn't a JPEG.
func exifOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return 1
	}
	for i := 2; i+4 <= len(data) && data[i] == 0xff; {
		marker := data[i+1]
		if marker == 0xda || marker == 0xd9 {
			break
		}
		n := int(binary.BigEndian.Uint16(data[i+2:]))
		if n < 2 || i+2+n > len(data) {
			break
		}
		if segment := data[i+4 : i+2+n]; marker == 0xe1 && len(segment) >= 6 && string(segment[:6]) == "Exif\x00\x00" {
			return tiffOrientation(segment[6:])
		}
		i += 2 + n
	}
	return 1
}

func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	offset := int64(order.Uint32(t[4:]))
	if offset < 8 || offset+2 > int64(len(t)) {
		return 1
	}
	entries := int(order.Uint16(t[offset:]))
	for k := 0; k < entries; k++ {
		e := int(offset) + 2 + 12*k
		if e+12 > len(t) {
			break
		}
		if order.Uint16(t[e:]) == 0x0112 {
			if v := int(order.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
