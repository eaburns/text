// Copyright © 2016, The T Authors.

// Package text handles rich-formatted text layout and drawing.
//
// A Setter lays out styalized text into a bounding box
// by repeatedly calling Add or AddStyle, and then calling Set.
// Set returns a Text which contains the text
// from all previous calls to Add or AddStyle.
//
// A Text can be queried for the byte-index of points
// and it can be drawn to a window.
// Rasterization of the lines of text is done lazily.
// Once finished, Text.Release releases the rasterized lines
// back to its setter to be reused by the next call to Set.
//
// A typical use
//
// First create a setter, add bytes to the setter,
// set it into a Text, and draw the Text.
//
// When the text changes,
// release the old Text, re-add the bytes to the setter,
// re-set it into a new Text, and draw the new Text.
//
// The new Text re-uses pre-rendered lines from the old text.
// In the common case, where little changed,
// drawing the new Text is very efficient.
package text

import (
	"image"
	"image/color"
	"image/draw"
	"unicode"
	"unicode/utf8"

	"golang.org/x/exp/shiny/screen"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// A Style describes a font face and colors.
type Style struct {
	Face   font.Face
	FG, BG color.Color
}

// Options control text layout by a setter.
type Options struct {
	// Size is the size of the Text returned by Set.
	Size image.Point

	// DefaultStyle dictates
	// the default background color of text,
	// the minimum line height of lines of text,
	// and the units of tab width.
	DefaultStyle Style

	// TabWidth is the number of DefaultStyle space-widths
	// between tab stops.
	TabWidth int

	// Padding is the number of pixels
	// between the borders of Bounds
	// and the Text.
	Padding int
}

// A Setter lays out text to fit in a rectangle.
type Setter struct {
	opts              Options
	lines, reuseLines []*line
	freeSpans         []*span
	// Styles is an intern table of styles.
	// This lets the Texts returned by Setter
	// use internal copies of the given Styles,
	// while avoiding extra allocation.
	styles []*Style
}

type line struct {
	spans   []*span
	w, h, a fixed.Int26_6
	buf     screen.Buffer
}

type span struct {
	*Style
	text   string
	x0, x1 fixed.Int26_6
}

// NewSetter returns a new Setter.
func NewSetter(opts Options) *Setter { return &Setter{opts: opts} }

// Release releases the resources of the Setter.
//
// The Setter may continue to be used after calling Release.
func (s *Setter) Release() {
	for _, l := range append(s.lines, s.reuseLines...) {
		if l.buf != nil {
			l.buf.Release()
		}
		s.freeSpans = append(s.freeSpans, l.spans...)
	}
}

// Reset clears any added lines, and resets the setter with new Options.
func (s *Setter) Reset(opts Options) {
	for _, l := range s.lines {
		s.freeSpans = append(s.freeSpans, l.spans...)
	}
	s.lines = s.lines[:0]
	s.opts = opts
}

// Tab returns the next tab stop.
func (s *Setter) tab(x fixed.Int26_6) fixed.Int26_6 {
	sp := advance(&s.opts.DefaultStyle, ' ')
	w := sp * fixed.Int26_6(s.opts.TabWidth)
	t := w - (x % w) + x
	if t-x < sp {
		return t + w
	}
	return t
}

// Add adds text to the Setter using the default style.
func (s *Setter) Add(text []byte) { s.AddStyle(&s.opts.DefaultStyle, text) }

// AddStyle adds text to the Setter using the given style.
func (s *Setter) AddStyle(sty *Style, text []byte) {
	if len(text) == 0 {
		return
	}

	ymax := fixed.I(s.opts.Size.Y)
	var h fixed.Int26_6
	for _, l := range s.lines {
		h += l.h
	}

	styCopy := s.internStyle(sty)

	m := s.opts.DefaultStyle.Face.Metrics()
	if len(s.lines) == 0 {
		s.lines = append(s.lines, &line{h: m.Height, a: m.Ascent})
	}
	for len(text) > 0 {
		if h > ymax {
			// Tall enough.
			return
		}
		text = add1(s, styCopy, text)
		if len(text) > 0 {
			h += s.lines[len(s.lines)-1].h
			s.lines = append(s.lines, &line{h: m.Height, a: m.Ascent})
		}
	}
}

// InternStyle returns a copy of the style from the intern table.
// If the style is not in the table, it is added.
func (s *Setter) internStyle(sty *Style) *Style {
	var styCopy *Style
	for _, sc := range s.styles {
		if *sc == *sty {
			styCopy = sc
			break
		}
	}
	if styCopy == nil {
		styCopy = new(Style)
		*styCopy = *sty
		s.styles = append(s.styles, styCopy)
	}
	return styCopy
}

func add1(s *Setter, sty *Style, text []byte) []byte {
	l := s.lines[len(s.lines)-1]
	var x0 fixed.Int26_6
	width := fixed.I(s.opts.Size.X - 2*s.opts.Padding)
	if len(l.spans) > 0 && len(l.spans[len(l.spans)-1].text) > 0 {
		lastSpan := l.spans[len(l.spans)-1]
		lastText := lastSpan.text
		if r, _ := utf8.DecodeLastRuneInString(lastText); r == '\n' {
			return text
		}
		x0 = lastSpan.x1
		if len(text) > 0 && lastSpan.Face == sty.Face {
			r, _ := utf8.DecodeRune(text)
			if len(lastText) > 0 {
				p, _ := utf8.DecodeLastRuneInString(lastText)
				x0 += sty.Face.Kern(p, r)
			}
		}
	}
	sp := s.newSpan(sty, "", x0, x0)
	var start, i int
	for i < len(text) {
		r, w := utf8.DecodeRune(text[i:])
		adv := advance(sty, r)
		if i > 0 {
			p, _ := utf8.DecodeLastRune(text[:i])
			adv += sty.Face.Kern(p, r)
		}
		if r == '\t' {
			adv = s.tab(sp.x1) - sp.x1
		}
		if r == '\n' || sp.x1+adv > width {
			// Always add newline or non-fitting tabs to the end of the line.
			// If the line is empty and the first rune doesn't fit, add it anyway.
			if r == '\n' || r == '\t' || len(l.spans) == 0 && i == 0 {
				i += w
				sp.x1 += adv
			}
			break
		}
		i += w
		sp.x1 += adv
	}

	m := sp.Face.Metrics()
	if m.Height > l.h {
		l.h = m.Height
	}
	if m.Ascent > l.a {
		l.a = m.Ascent
	}
	l.w = sp.x1
	sp.text = string(text[start:i])
	l.spans = append(l.spans, sp)
	return text[i:]
}

func (s *Setter) newSpan(sty *Style, text string, x0, x1 fixed.Int26_6) *span {
	var sp *span
	if n := len(s.freeSpans); n == 0 {
		sp = new(span)
	} else {
		sp, s.freeSpans = s.freeSpans[n-1], s.freeSpans[:n-1]
	}
	sp.Style = sty
	sp.text = text
	sp.x0 = x0
	sp.x1 = x1
	return sp
}

func advance(sty *Style, r rune) fixed.Int26_6 {
	adv, ok := sty.Face.GlyphAdvance(r)
	if !ok {
		adv, _ = sty.Face.GlyphAdvance(unicode.ReplacementChar)
	}
	return adv
}

// Set returns the Text containing the text from all calls to Add or AddStyle
// since the previous call to Set.
//
// Where possible, the returned Text uses pre-rasterized lines
// that were released to the Setter
// by the previous call to Text.Release.
func (s *Setter) Set() *Text {
	var h1 int
	for _, line := range s.lines {
		// Find a reuse line with the exact same spans; reuse its buffer.
		for _, r := range s.reuseLines {
			if r.buf == nil || len(r.spans) != len(line.spans) {
				continue
			}
			match := true
			for i, sp := range r.spans {
				span := line.spans[i]
				if sp.Style != span.Style || sp.text != span.text {
					match = false
					break
				}
			}
			if match {
				line.buf = r.buf
				r.buf = nil
				break
			}
		}
		h1 += int(line.h >> 6)
	}
	t := &Text{setter: s, lines: s.lines, size: s.opts.Size}
	for _, l := range s.reuseLines {
		s.freeSpans = append(s.freeSpans, l.spans...)
		l.spans = nil
		if l.buf != nil {
			l.buf.Release()
			l.buf = nil
		}
	}
	s.lines = s.reuseLines[:0]
	s.reuseLines = nil
	return t
}

// A Text is a type-set text.
type Text struct {
	setter *Setter
	lines  []*line
	size   image.Point
}

// Size returns the size of the Text.
func (t *Text) Size() image.Point { return t.size }

// Release releases the rasterized lines of the Text
// back to the Setter that created it
// for reuse by the next call to Set.
//
// The Text should no longer be used after it is released.
//
// To release the resources back to the operating system,
// first release them to the Setter using this method,
// then call Setter.Release.
func (t *Text) Release() {
	t.setter.reuseLines = append(t.setter.reuseLines, t.lines...)
	t.lines = nil
}

// Index returns the byte index into the text
// corresponding to the glyph at the given point.
// 0,0 is the top left of the text.
func (t *Text) Index(p image.Point) int {
	y := fixed.I(t.setter.opts.Padding)
	if len(t.lines) == 0 || fixed.I(p.Y) < y {
		return 0
	}

	var i, l int
	for l = 0; l < len(t.lines); l++ {
		line := t.lines[l]
		y += line.h
		if y > fixed.I(p.Y) {
			break
		}
		i += line.len()
	}
	if l >= len(t.lines) {
		return i
	}

	var w int
	line := t.lines[l]
	for _, sp := range line.spans {
		x := sp.x0 + fixed.I(t.setter.opts.Padding)
		var j int
		for j < len(sp.text) {
			var r rune
			r, w = utf8.DecodeRuneInString(sp.text[j:])
			if r == '\t' {
				x = t.setter.tab(x)
			} else {
				x += advance(sp.Style, r)
				if j > 0 {
					p, _ := utf8.DecodeLastRuneInString(sp.text[:j])
					x += sp.Face.Kern(p, r)
				}
			}
			if x > fixed.I(p.X) {
				return i
			}
			j += w
			i += w
		}
	}
	return i - w
}

// Len returns the length of the line in bytes.
func (l *line) len() int {
	var n int
	for i := range l.spans {
		n += len(l.spans[i].text)
	}
	return n
}

// Draw draws the text to the Window.
func (t *Text) Draw(at image.Point, scr screen.Screen, win screen.Window) {
	b := image.Rectangle{Min: at, Max: at.Add(t.size)}
	win.Fill(b, t.setter.opts.DefaultStyle.BG, draw.Over)

	pad := t.setter.opts.Padding
	textWidth := b.Dx() - 2*pad
	var y int
	x0, y1 := b.Min.X+pad, b.Min.Y+pad
	for _, l := range t.lines {
		y = y1
		y1 = y + int(l.h>>6)
		if y1 > b.Max.Y-pad {
			break
		}
		if l.buf == nil && int(l.w>>6) > 0 {
			var err error
			size := image.Pt(int(l.w>>6), int(l.h>>6))
			l.buf, err = scr.NewBuffer(size)
			if err != nil {
				panic(err)
			}
			drawLine(t, l, l.buf.RGBA())
		}
		if l.buf == nil {
			continue
		}

		if l.buf.Bounds().Dx() > textWidth {
			// The line doesn't fit the width, don't draw it.
			continue
		}

		win.Upload(image.Pt(x0, y), l.buf, l.buf.Bounds())
		if dx := l.buf.Bounds().Dx(); dx < textWidth {
			bg := t.setter.opts.DefaultStyle.BG
			if len(l.spans) > 0 {
				bg = l.spans[len(l.spans)-1].BG
			}
			win.Fill(image.Rect(x0+dx, y, b.Max.X-pad, y1), bg, draw.Over)
		}
	}
}

func drawLine(t *Text, l *line, img draw.Image) {
	for _, sp := range l.spans {
		fg := image.NewUniform(sp.FG)
		bg := image.NewUniform(sp.BG)
		box := image.Rect(int(sp.x0>>6), 0, int(sp.x1>>6), int(l.h>>6))
		draw.Draw(img, box, bg, image.ZP, draw.Over)
		x := sp.x0
		for i, r := range sp.text {
			if r == '\t' {
				x = t.setter.tab(x)
				continue
			}
			if r == '\n' {
				continue
			}
			if i > 0 {
				p, _ := utf8.DecodeLastRuneInString(sp.text[:i])
				x += sp.Face.Kern(p, r)
			}
			pt := fixed.Point26_6{X: x, Y: l.a}
			dr, mask, maskp, _, ok := sp.Face.Glyph(pt, r)
			if !ok {
				dr, mask, maskp, _, _ = sp.Face.Glyph(pt, unicode.ReplacementChar)
			}
			draw.DrawMask(img, dr, fg, image.ZP, mask, maskp, draw.Over)
			x += advance(sp.Style, r)
		}
	}
}