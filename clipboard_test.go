package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"testing"
)

func TestEncodeDIBAsPNG(t *testing.T) {
	dib := make([]byte, 40+16)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], 2)
	binary.LittleEndian.PutUint32(dib[8:12], 2)
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 32)
	binary.LittleEndian.PutUint32(dib[20:24], 16)
	copy(dib[40:], []byte{
		255, 0, 0, 0, 255, 255, 255, 0,
		0, 0, 255, 0, 0, 255, 0, 0,
	})
	encoded, err := encodeDIBAsPNG(dib)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		t.Fatal("clipboard bitmap was not encoded as PNG")
	}
	image, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	want := [][4]uint32{
		{65535, 0, 0, 65535},
		{0, 65535, 0, 65535},
		{0, 0, 65535, 65535},
		{65535, 65535, 65535, 65535},
	}
	for index, point := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {1, 1}} {
		r, g, b, a := image.At(point[0], point[1]).RGBA()
		if [4]uint32{r, g, b, a} != want[index] {
			t.Fatalf("pixel %v = %v, want %v", point, [4]uint32{r, g, b, a}, want[index])
		}
	}
}

func TestEncodeDIBRejectsUnsupportedFormat(t *testing.T) {
	dib := make([]byte, 40)
	binary.LittleEndian.PutUint32(dib[0:4], 40)
	binary.LittleEndian.PutUint32(dib[4:8], 1)
	binary.LittleEndian.PutUint32(dib[8:12], 1)
	binary.LittleEndian.PutUint16(dib[12:14], 1)
	binary.LittleEndian.PutUint16(dib[14:16], 8)
	if _, err := encodeDIBAsPNG(dib); err == nil {
		t.Fatal("unsupported indexed clipboard bitmap was accepted")
	}
}
