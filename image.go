package gopdf

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"

	_ "image/gif"  // register formats for AddImageReader
	_ "image/jpeg" //
	_ "image/png"  //
)

// Image is an image registered with a Document, ready to be placed on any
// of its pages with Page.DrawImage.
type Image struct {
	index  int
	width  int
	height int
}

// Width returns the intrinsic width of the image in pixels.
func (img *Image) Width() int { return img.width }

// Height returns the intrinsic height of the image in pixels.
func (img *Image) Height() int { return img.height }

// imageData is the document-internal representation of an embedded image.
type imageData struct {
	width      int
	height     int
	colorSpace string // DeviceRGB, DeviceGray or DeviceCMYK
	dct        bool   // data is raw JPEG (DCTDecode)
	invert     bool   // Adobe CMYK JPEG: samples are stored inverted
	data       []byte // JPEG file bytes, or raw samples row by row
	smask      []byte // optional 8-bit alpha samples
}

// AddImageFile registers an image file (JPEG, PNG or GIF) with the
// document.
func (d *Document) AddImageFile(path string) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := d.AddImageReader(f)
	if err != nil {
		return nil, fmt.Errorf("gopdf: %s: %w", path, err)
	}
	return img, nil
}

// AddImageReader registers an image read from r. JPEG data is embedded
// directly without re-encoding; PNG and GIF images are decoded and stored
// as raw samples, preserving any alpha channel as a PDF soft mask.
func (d *Document) AddImageReader(r io.Reader) (*Image, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gopdf: decoding image: %w", err)
	}
	if format == "jpeg" {
		cs := "DeviceRGB"
		switch cfg.ColorModel {
		case color.GrayModel, color.Gray16Model:
			cs = "DeviceGray"
		case color.CMYKModel:
			cs = "DeviceCMYK"
		}
		return d.addImageData(&imageData{
			width:      cfg.Width,
			height:     cfg.Height,
			colorSpace: cs,
			dct:        true,
			// CMYK JPEGs written by Adobe tools store inverted samples.
			invert: cs == "DeviceCMYK" && jpegIsAdobe(raw),
			data:   raw,
		}), nil
	}
	m, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("gopdf: decoding image: %w", err)
	}
	return d.AddImage(m)
}

// AddImage registers a decoded image with the document. Grayscale images
// are stored as 8-bit gray samples, everything else as 8-bit RGB; if the
// image has any transparency, the alpha channel is preserved as a PDF soft
// mask. *image.Gray, *image.NRGBA and *image.RGBA use fast paths that read
// pixel data directly.
func (d *Document) AddImage(m image.Image) (*Image, error) {
	b := m.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("gopdf: image has empty bounds")
	}

	if gray, ok := m.(*image.Gray); ok {
		data := make([]byte, 0, w*h)
		for y := 0; y < h; y++ {
			row := gray.Pix[y*gray.Stride:]
			data = append(data, row[:w]...)
		}
		return d.addImageData(&imageData{
			width: w, height: h, colorSpace: "DeviceGray", data: data,
		}), nil
	}

	rgb := make([]byte, 0, w*h*3)
	alpha := make([]byte, 0, w*h)
	opaque := true
	switch im := m.(type) {
	case *image.NRGBA:
		for y := 0; y < h; y++ {
			row := im.Pix[y*im.Stride : y*im.Stride+w*4]
			for x := 0; x < len(row); x += 4 {
				rgb = append(rgb, row[x], row[x+1], row[x+2])
				a := row[x+3]
				alpha = append(alpha, a)
				if a != 255 {
					opaque = false
				}
			}
		}
	case *image.RGBA:
		for y := 0; y < h; y++ {
			row := im.Pix[y*im.Stride : y*im.Stride+w*4]
			for x := 0; x < len(row); x += 4 {
				r, g, bl, a := row[x], row[x+1], row[x+2], row[x+3]
				if a != 255 {
					opaque = false
					if a == 0 {
						r, g, bl = 0, 0, 0
					} else {
						// Un-premultiply so translucent pixels
						// keep their true color.
						r = uint8((uint32(r)*255 + uint32(a)/2) / uint32(a))
						g = uint8((uint32(g)*255 + uint32(a)/2) / uint32(a))
						bl = uint8((uint32(bl)*255 + uint32(a)/2) / uint32(a))
					}
				}
				rgb = append(rgb, r, g, bl)
				alpha = append(alpha, a)
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(m.At(x, y)).(color.NRGBA)
				rgb = append(rgb, c.R, c.G, c.B)
				alpha = append(alpha, c.A)
				if c.A != 255 {
					opaque = false
				}
			}
		}
	}
	data := &imageData{
		width:      w,
		height:     h,
		colorSpace: "DeviceRGB",
		data:       rgb,
	}
	if !opaque {
		data.smask = alpha
	}
	return d.addImageData(data), nil
}

// jpegIsAdobe reports whether the JPEG stream carries an Adobe APP14
// marker, which indicates inverted CMYK sample values.
func jpegIsAdobe(raw []byte) bool {
	i := 2
	for i+4 <= len(raw) && raw[i] == 0xFF {
		marker := raw[i+1]
		if marker == 0xD8 || (marker >= 0xD0 && marker <= 0xD7) || marker == 0x01 {
			i += 2 // standalone marker, no length field
			continue
		}
		segLen := int(raw[i+2])<<8 | int(raw[i+3])
		if marker == 0xEE && segLen >= 7 && i+4+5 <= len(raw) &&
			string(raw[i+4:i+9]) == "Adobe" {
			return true
		}
		if marker == 0xDA { // start of scan: no more metadata segments
			return false
		}
		i += 2 + segLen
	}
	return false
}

func (d *Document) addImageData(data *imageData) *Image {
	d.images = append(d.images, data)
	return &Image{
		index:  len(d.images) - 1,
		width:  data.width,
		height: data.height,
	}
}
