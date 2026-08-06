// Command demo generates demo.pdf, a one-stop showcase of gopdf features:
// text in the standard fonts, vector graphics, transforms, and images.
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math"

	"github.com/salvionidigital/gopdf"
)

func main() {
	doc := gopdf.New()
	doc.SetInfo(gopdf.Info{
		Title:   "gopdf demo",
		Author:  "gopdf",
		Subject: "Feature showcase",
	})

	page := doc.AddPage()
	margin := 20 * gopdf.Mm
	right := page.Width() - margin

	// Header.
	page.SetFillColor(gopdf.RGB(25, 55, 109))
	page.Rect(0, 0, page.Width(), 26*gopdf.Mm, gopdf.Fill)
	page.SetFillColor(gopdf.White)
	page.SetFont(gopdf.HelveticaBold, 26)
	page.Text(margin, 17*gopdf.Mm, "gopdf")
	page.SetFont(gopdf.Helvetica, 11)
	page.TextAligned(margin, 17*gopdf.Mm, right-margin, gopdf.AlignRight,
		"a pure-Go PDF library — no dependencies")

	y := 40 * gopdf.Mm

	// Fonts.
	page.SetFillColor(gopdf.Black)
	page.SetFont(gopdf.HelveticaBold, 13)
	page.Text(margin, y, "Standard fonts")
	y += 8 * gopdf.Mm
	sample := "The quick brown fox jumps over the lazy dog — “fjord”, café, 12.5%"
	for _, f := range []*gopdf.Font{
		gopdf.Helvetica, gopdf.HelveticaBold, gopdf.TimesRoman,
		gopdf.TimesItalic, gopdf.Courier,
	} {
		page.SetFont(f, 11)
		page.Text(margin, y, f.Name()+":  "+sample)
		y += 6.5 * gopdf.Mm
	}

	// Graphics.
	y += 6 * gopdf.Mm
	page.SetFont(gopdf.HelveticaBold, 13)
	page.Text(margin, y, "Vector graphics")
	y += 6 * gopdf.Mm

	page.SetFillColor(gopdf.RGB(240, 180, 60))
	page.SetStrokeColor(gopdf.RGB(25, 55, 109))
	page.SetLineWidth(1.5)
	page.Rect(margin, y, 28*gopdf.Mm, 20*gopdf.Mm, gopdf.FillStroke)

	page.SetFillColor(gopdf.RGB(190, 60, 70))
	page.Circle(margin+48*gopdf.Mm, y+10*gopdf.Mm, 10*gopdf.Mm, gopdf.FillStroke)

	page.SetFillColor(gopdf.RGB(70, 150, 90))
	cx := margin + 78*gopdf.Mm
	page.Polygon(gopdf.FillStroke,
		cx, y, cx+12*gopdf.Mm, y+20*gopdf.Mm, cx-12*gopdf.Mm, y+20*gopdf.Mm)

	// A sine wave built with the path API.
	page.SetStrokeColor(gopdf.RGB(120, 90, 200))
	page.SetLineWidth(1.2)
	x0 := margin + 100*gopdf.Mm
	page.MoveTo(x0, y+10*gopdf.Mm)
	for i := 1; i <= 60; i++ {
		t := float64(i) / 60
		page.LineTo(x0+t*55*gopdf.Mm, y+10*gopdf.Mm-9*gopdf.Mm*math.Sin(t*4*math.Pi))
	}
	page.DrawPath(gopdf.Stroke)

	page.SetStrokeColor(gopdf.Gray(140))
	page.SetDash(4, 3)
	page.Line(margin, y+26*gopdf.Mm, right, y+26*gopdf.Mm)
	page.SetDash()

	// Images.
	y += 36 * gopdf.Mm
	page.SetFillColor(gopdf.Black)
	page.SetFont(gopdf.HelveticaBold, 13)
	page.Text(margin, y, "Images")
	y += 6 * gopdf.Mm

	gradient, err := doc.AddImage(makeGradient(120, 120))
	if err != nil {
		log.Fatal(err)
	}
	page.DrawImage(gradient, margin, y, 30*gopdf.Mm, 30*gopdf.Mm)

	ball, err := doc.AddImage(makeBall(160))
	if err != nil {
		log.Fatal(err)
	}
	// Translucent PNG-style image over a stripe to show the soft mask.
	page.SetFillColor(gopdf.RGB(25, 55, 109))
	page.Rect(margin+40*gopdf.Mm, y+12*gopdf.Mm, 45*gopdf.Mm, 8*gopdf.Mm, gopdf.Fill)
	page.DrawImage(ball, margin+47*gopdf.Mm, y, 30*gopdf.Mm, 30*gopdf.Mm)

	jpegImg, err := doc.AddImageReader(bytes.NewReader(makeJPEG(120, 120)))
	if err != nil {
		log.Fatal(err)
	}
	page.DrawImage(jpegImg, margin+90*gopdf.Mm, y, 30*gopdf.Mm, 30*gopdf.Mm)

	page.SetFillColor(gopdf.Gray(90))
	page.SetFont(gopdf.Helvetica, 8)
	page.Text(margin, y+34*gopdf.Mm, "raw RGB")
	page.Text(margin+47*gopdf.Mm, y+34*gopdf.Mm, "alpha soft mask")
	page.Text(margin+90*gopdf.Mm, y+34*gopdf.Mm, "embedded JPEG")

	// Rotated watermark.
	page.Push()
	page.RotateAt(45, page.Width()/2, page.Height()/2)
	page.SetFont(gopdf.HelveticaBold, 60)
	page.SetFillColor(gopdf.RGB(225, 228, 235))
	w := page.TextWidth("PURE GO")
	page.Text(page.Width()/2-w/2, page.Height()/2+140, "PURE GO")
	page.Pop()

	// Second page: landscape with a simple table.
	p2 := doc.AddPageSize(gopdf.A4.Landscape())
	p2.SetFont(gopdf.HelveticaBold, 16)
	p2.Text(margin, margin+6*gopdf.Mm, "Second page — A4 landscape")
	drawTable(p2, margin, margin+16*gopdf.Mm)

	// Third page: embedded fonts, transparency, links, wrapped text.
	p3 := doc.AddPage()
	y = margin + 6*gopdf.Mm
	p3.SetFont(gopdf.HelveticaBold, 16)
	p3.Text(margin, y, "Embedded fonts & more")
	y += 12 * gopdf.Mm

	if unicodeFont := loadSystemFont(); unicodeFont != nil {
		p3.SetFont(gopdf.HelveticaBold, 13)
		p3.Text(margin, y, "TrueType embedding ("+unicodeFont.Name()+", subset)")
		y += 7 * gopdf.Mm
		p3.SetFont(unicodeFont, 12)
		for _, line := range []string{
			"Ελληνικά: Καλημέρα κόσμε",
			"Русский: Привет, мир",
			"Čeština: Příliš žluťoučký kůň úpěl ďábelské ódy",
			"Türkçe: Pijamalı hasta yağız şoföre çabucak güvendi",
		} {
			p3.Text(margin, y, line)
			y += 6.5 * gopdf.Mm
		}
	} else {
		p3.SetFont(gopdf.Helvetica, 11)
		p3.Text(margin, y, "(no system TrueType font found for the Unicode sample)")
		y += 7 * gopdf.Mm
	}

	// Transparency.
	y += 6 * gopdf.Mm
	p3.SetFillColor(gopdf.Black)
	p3.SetFont(gopdf.HelveticaBold, 13)
	p3.Text(margin, y, "Transparency")
	y += 21 * gopdf.Mm
	p3.Push()
	p3.SetAlpha(0.55, 1)
	p3.SetFillColor(gopdf.RGB(190, 60, 70))
	p3.Circle(margin+12*gopdf.Mm, y, 11*gopdf.Mm, gopdf.Fill)
	p3.SetFillColor(gopdf.RGB(60, 120, 190))
	p3.Circle(margin+24*gopdf.Mm, y, 11*gopdf.Mm, gopdf.Fill)
	p3.SetFillColor(gopdf.RGB(240, 180, 60))
	p3.Circle(margin+18*gopdf.Mm, y-9*gopdf.Mm, 11*gopdf.Mm, gopdf.Fill)
	p3.Pop()

	// Wrapped text in a rounded box, with a link.
	boxX := margin + 50*gopdf.Mm
	boxW := right - boxX
	p3.SetStrokeColor(gopdf.Gray(150))
	p3.RoundedRect(boxX, y-16*gopdf.Mm, boxW, 30*gopdf.Mm, 4*gopdf.Mm, gopdf.Stroke)
	p3.SetFont(gopdf.TimesRoman, 11)
	p3.SetFillColor(gopdf.Black)
	p3.TextWrapped(boxX+4*gopdf.Mm, y-10*gopdf.Mm, boxW-8*gopdf.Mm, 5*gopdf.Mm,
		"This paragraph is word-wrapped automatically by TextWrapped. "+
			"The box around it is a RoundedRect, and the colored circles on the "+
			"left are painted with 55% fill opacity via SetAlpha.")
	p3.SetFillColor(gopdf.RGB(25, 55, 109))
	p3.SetFont(gopdf.Helvetica, 11)
	linkText := "gopdf on GitHub"
	p3.Text(boxX+4*gopdf.Mm, y+9*gopdf.Mm, linkText)
	p3.LinkURL(boxX+4*gopdf.Mm, y+5*gopdf.Mm, p3.TextWidth(linkText), 5*gopdf.Mm,
		"https://github.com/salvionidigital/gopdf")

	// Bookmarks for the viewer sidebar.
	root := doc.AddOutline(nil, "gopdf demo", page, 0)
	doc.AddOutline(root, "Fonts & graphics", page, 30*gopdf.Mm)
	doc.AddOutline(root, "Feature table", p2, 0)
	doc.AddOutline(root, "Embedded fonts & more", p3, 0)

	if err := doc.Save("demo.pdf"); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote demo.pdf")
}

// loadSystemFont tries a few well-known TrueType font locations and returns
// nil if none exists.
func loadSystemFont() *gopdf.Font {
	for _, path := range []string{
		"/System/Library/Fonts/Supplemental/Arial.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"C:\\Windows\\Fonts\\arial.ttf",
	} {
		if f, err := gopdf.LoadFont(path); err == nil {
			return f
		}
	}
	return nil
}

func drawTable(p *gopdf.Page, x, y float64) {
	headers := []string{"Feature", "Status"}
	rows := [][]string{
		{"Document & page model", "done"},
		{"Standard 14 fonts with metrics", "done"},
		{"Vector graphics & paths", "done"},
		{"JPEG / PNG / GIF images", "done"},
		{"Flate compression", "done"},
		{"TrueType embedding", "planned"},
	}
	colW := []float64{90 * gopdf.Mm, 40 * gopdf.Mm}
	rowH := 9 * gopdf.Mm

	p.SetFillColor(gopdf.RGB(25, 55, 109))
	p.Rect(x, y, colW[0]+colW[1], rowH, gopdf.Fill)
	p.SetFillColor(gopdf.White)
	p.SetFont(gopdf.HelveticaBold, 11)
	p.Text(x+3, y+rowH-3*gopdf.Mm, headers[0])
	p.Text(x+colW[0]+3, y+rowH-3*gopdf.Mm, headers[1])

	p.SetFont(gopdf.Helvetica, 11)
	for i, row := range rows {
		ry := y + rowH*float64(i+1)
		if i%2 == 1 {
			p.SetFillColor(gopdf.Gray(240))
			p.Rect(x, ry, colW[0]+colW[1], rowH, gopdf.Fill)
		}
		p.SetFillColor(gopdf.Black)
		p.Text(x+3, ry+rowH-3*gopdf.Mm, row[0])
		p.Text(x+colW[0]+3, ry+rowH-3*gopdf.Mm, row[1])
	}
	p.SetStrokeColor(gopdf.Gray(150))
	p.SetLineWidth(0.5)
	total := rowH * float64(len(rows)+1)
	p.Rect(x, y, colW[0]+colW[1], total, gopdf.Stroke)
	p.Line(x+colW[0], y, x+colW[0], y+total)
}

// makeGradient renders an opaque RGB gradient test image.
func makeGradient(w, h int) image.Image {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.Set(x, y, color.RGBA{
				R: uint8(255 * x / w),
				G: uint8(255 * y / h),
				B: 160,
				A: 255,
			})
		}
	}
	return m
}

// makeBall renders a soft-edged translucent sphere to exercise soft masks.
func makeBall(size int) image.Image {
	m := image.NewNRGBA(image.Rect(0, 0, size, size))
	c := float64(size) / 2
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			d := math.Hypot(float64(x)-c, float64(y)-c) / c
			if d > 1 {
				continue
			}
			fade := 1 - d*d
			m.SetNRGBA(x, y, color.NRGBA{
				R: uint8(200 + 55*fade),
				G: uint8(120 * fade),
				B: uint8(60 * fade),
				A: uint8(230 * fade),
			})
		}
	}
	return m
}

// makeJPEG returns an encoded JPEG to exercise the DCT passthrough path.
func makeJPEG(w, h int) []byte {
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8((x ^ y) & 0xFF)
			m.Set(x, y, color.RGBA{R: v, G: uint8(255 - v), B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: 90}); err != nil {
		panic(err)
	}
	return buf.Bytes()
}
