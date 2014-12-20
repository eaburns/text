package buffer

import (
	"encoding/binary"
	"io"
	"unicode/utf8"
)

const (
	// RuneBytes is the number of bytes in Go's rune type.
	runeBytes = 4
)

// A Runes is an unbounded rune buffer backed by a file.
type Runes struct {
	bytes *Bytes
}

// NewRunes returns a new Runes buffer.
// The buffer caches no more than blockSize runes in memory.
func NewRunes(blockSize int) *Runes {
	return &Runes{bytes: NewBytes(blockSize * runeBytes)}
}

// Close closes the buffer, freeing its resources.
func (b *Runes) Close() error {
	return b.bytes.Close()
}

// Size returns the number of runes in the buffer.
func (b *Runes) Size() int64 {
	return b.bytes.Size() / runeBytes
}

// Read returns the runes in the range of an Address in the buffer.
func (b *Runes) Read(at Address) ([]rune, error) {
	if at.From < 0 || at.From > at.To || at.To > b.Size() {
		return nil, AddressError(at)
	}
	bs, err := b.bytes.Read(at.asBytes())
	if err != nil {
		return nil, err
	}
	rs := make([]rune, 0, at.Size())
	for len(bs) > 0 {
		r := rune(binary.LittleEndian.Uint32(bs))
		rs = append(rs, r)
		bs = bs[runeBytes:]
	}
	return rs, nil
}

// Write writes runes to the range of an Address in the buffer.
func (b *Runes) Write(rs []rune, at Address) error {
	if at.From < 0 || at.From > at.To || at.To > b.Size() {
		return AddressError(at)
	}
	bs := make([]byte, len(rs)*runeBytes)
	for i, r := range rs {
		binary.LittleEndian.PutUint32(bs[i*runeBytes:], uint32(r))
	}
	return b.bytes.Write(bs, at.asBytes())
}

// Get overwrites the buffer with the contents of the io.RuneReader.
// The return value is the number of bytes read.
func (b *Runes) Get(r io.RuneReader) (int, error) {
	at := Address{From: 0, To: b.Size()}
	var tot int
	for {
		r, w, err := r.ReadRune()
		tot += w
		switch {
		case err == io.EOF:
			return tot, nil
		case err != nil:
			return tot, err
		}
		if err := b.Write([]rune{r}, at); err != nil {
			return tot, err
		}
		at = Address{From: b.Size(), To: b.Size()}
	}
}

// Put writes the UTF8 encoding of the buffer to the io.Writer.
// The return value is the number of bytes written.
func (b *Runes) Put(w io.Writer) (int, error) {
	const n = 512
	var tot int
	var at Address
	for at.From < b.Size() {
		at.To = at.From + int64(b.bytes.blockSize*runeBytes)
		if at.To > b.Size() {
			at.To = b.Size()
		}
		rs, err := b.Read(at)
		if err != nil {
			return tot, err
		}

		var o int
		var bs [utf8.UTFMax * n]byte
		for _, r := range rs {
			o += utf8.EncodeRune(bs[o:], r)
		}
		m, err := w.Write(bs[:o])
		tot += m
		if err != nil {
			return tot, err
		}
		at.From = at.To
	}
	return tot, nil
}