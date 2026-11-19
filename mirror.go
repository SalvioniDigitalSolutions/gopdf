package gopdf

import (
	"image"
	"image/draw"
)

// A report's figure was stored upside down as in a mirror and drawn
// the right way round by its placement — a flip in the matrix that maps
// the picture onto the page. Read as stored, its captions ("Railroad
// Valley Playa, Nevada …") were mirror writing no engine reads: the
// values in them were neither found nor barred, and every viewer showed
// them. A picture is read the way its page shows it as far as a mirror
// goes; turning it is the engine's business.

// Mirrored reports a placement that shows the picture mirrored: its
// matrix has a negative determinant. A rotation keeps the sign, a flip
// changes it, and a mirror and a rotation together are a flip of the
// rows and a rotation. A picture the page never paints has the zero
// matrix and is not mirrored.
func (im ImageRef) Mirrored() bool {
	m := im.Matrix
	return m[0]*m[3]-m[1]*m[2] < 0
}

// FlipRows returns img with its rows in the opposite order: a picture a
// mirrored placement shows, as it shows it up to a rotation. A grey
// picture stays grey; any other comes back as RGBA.
func FlipRows(img image.Image) image.Image {
	b := img.Bounds()
	if g, ok := img.(*image.Gray); ok {
		out := image.NewGray(image.Rect(0, 0, b.Dx(), b.Dy()))
		for y := 0; y < b.Dy(); y++ {
			copy(out.Pix[y*out.Stride:y*out.Stride+b.Dx()], g.Pix[(b.Dy()-1-y)*g.Stride:(b.Dy()-1-y)*g.Stride+b.Dx()])
		}
		return out
	}
	src := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	out := image.NewRGBA(src.Bounds())
	for y := 0; y < b.Dy(); y++ {
		copy(out.Pix[y*out.Stride:(y+1)*out.Stride], src.Pix[(b.Dy()-1-y)*src.Stride:(b.Dy()-y)*src.Stride])
	}
	return out
}

// FlippedRowsBack puts a box read on FlipRows(img) back on img as
// stored, for a picture h pixels tall.
func FlippedRowsBack(y, boxH, h int) int {
	return h - y - boxH
}
