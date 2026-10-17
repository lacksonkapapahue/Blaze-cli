package refs

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif" // decode GIF
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
)

const (
	maxImageFile = 32 * 1024 * 1024 // bigger files are refused before decoding
	maxImageSide = 2048             // longest side sent; bigger images are scaled down
	maxPixels    = 50_000_000       // refuse decoding giant canvases
)

// LoadImage reads an image file and returns it as a data URL ready for an
// OpenAI image_url part, plus the bytes sent. Images with a side over 2048 px
// are scaled down; anything still over maxBytes after re-encoding is refused.
// Only PNG, JPEG and GIF are read (pure Go, no cgo).
func LoadImage(path string, maxBytes int) (string, int, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxImage
	}
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, cleanFSErr(err)
	}
	if st.Size() > maxImageFile {
		return "", 0, fmt.Errorf("too big (%s; %s max)", HumanSize(st.Size()), HumanSize(maxImageFile))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", 0, cleanFSErr(err)
	}
	return EncodeImage(b, maxBytes)
}

// EncodeImage is LoadImage for bytes already read.
func EncodeImage(b []byte, maxBytes int) (string, int, error) {
	mime := http.DetectContentType(b)
	switch mime {
	case "image/png", "image/jpeg", "image/gif":
	default:
		return "", 0, errors.New("not a PNG, JPEG or GIF image")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return "", 0, errors.New("unreadable image")
	}
	if cfg.Width*cfg.Height > maxPixels {
		return "", 0, fmt.Errorf("image too large (%d×%d)", cfg.Width, cfg.Height)
	}
	out := b
	if cfg.Width > maxImageSide || cfg.Height > maxImageSide || len(b) > maxBytes || mime == "image/gif" {
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return "", 0, errors.New("unreadable image")
		}
		img = shrink(img, maxImageSide)
		out, mime, err = reencode(img, mime, maxBytes)
		if err != nil {
			return "", 0, err
		}
	}
	if len(out) > maxBytes {
		return "", 0, fmt.Errorf("too big after scaling (%s; %s max)", HumanSize(int64(len(out))), HumanSize(int64(maxBytes)))
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(out), len(out), nil
}

// reencode writes img as PNG (screenshots stay sharp) and falls back to JPEG,
// then smaller JPEGs, when the PNG is over maxBytes.
func reencode(img image.Image, mime string, maxBytes int) ([]byte, string, error) {
	var buf bytes.Buffer
	if mime != "image/jpeg" {
		if err := png.Encode(&buf, img); err == nil && buf.Len() <= maxBytes {
			return buf.Bytes(), "image/png", nil
		}
	}
	for _, q := range []int{88, 75, 60} {
		buf.Reset()
		if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: q}); err != nil {
			return nil, "", errors.New("couldn't encode the image")
		}
		if buf.Len() <= maxBytes {
			break
		}
	}
	return buf.Bytes(), "image/jpeg", nil
}

// flatten puts img on white (JPEG has no transparency).
func flatten(img image.Image) image.Image {
	b := img.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, image.White, image.Point{}, draw.Src)
	draw.Draw(dst, b, img, b.Min, draw.Over)
	return dst
}

// shrink scales img so its longest side is at most side, averaging the source
// pixels under each destination pixel (a box filter: simple and sharp enough).
func shrink(img image.Image, side int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= side && h <= side {
		return img
	}
	nw, nh := side, h*side/w
	if h > w {
		nw, nh = w*side/h, side
	}
	nw, nh = max(nw, 1), max(nh, 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		y1 = max(y1, y0+1)
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			x1 = max(x1, x0+1)
			var r, g, bl, a, n uint64
			// sample at most a 4×4 grid of the box: plenty for a downscale
			sy := max(1, (y1-y0)/4)
			sx := max(1, (x1-x0)/4)
			for yy := y0; yy < y1; yy += sy {
				for xx := x0; xx < x1; xx += sx {
					cr, cg, cb, ca := img.At(xx, yy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i+0] = uint8(r / n >> 8)
			dst.Pix[i+1] = uint8(g / n >> 8)
			dst.Pix[i+2] = uint8(bl / n >> 8)
			dst.Pix[i+3] = uint8(a / n >> 8)
		}
	}
	return dst
}
