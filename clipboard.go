package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

type clipboardPayload struct {
	Files []string
	Name  string
	MIME  string
	Data  []byte
	Text  string
}

func encodeDIBAsPNG(data []byte) ([]byte, error) {
	if len(data) < 40 {
		return nil, errors.New("the clipboard image header is incomplete")
	}
	headerSize := int(binary.LittleEndian.Uint32(data[0:4]))
	width := int(int32(binary.LittleEndian.Uint32(data[4:8])))
	signedHeight := int32(binary.LittleEndian.Uint32(data[8:12]))
	planes := binary.LittleEndian.Uint16(data[12:14])
	bits := int(binary.LittleEndian.Uint16(data[14:16]))
	compression := binary.LittleEndian.Uint32(data[16:20])
	if headerSize < 40 || headerSize > len(data) || width <= 0 || signedHeight == 0 || planes != 1 ||
		(bits != 24 && bits != 32) || (compression != 0 && compression != 3) || (compression == 3 && bits != 32) {
		return nil, errors.New("the clipboard image uses an unsupported Windows bitmap format")
	}
	height := int(signedHeight)
	topDown := height < 0
	if topDown {
		height = -height
	}
	if width > 32768 || height > 32768 || int64(width)*int64(height) > 268435456 {
		return nil, errors.New("the clipboard image is too large")
	}
	offset := headerSize
	var masks [4]uint32
	if compression == 3 {
		if headerSize >= 52 {
			masks[0] = binary.LittleEndian.Uint32(data[40:44])
			masks[1] = binary.LittleEndian.Uint32(data[44:48])
			masks[2] = binary.LittleEndian.Uint32(data[48:52])
			if headerSize >= 56 {
				masks[3] = binary.LittleEndian.Uint32(data[52:56])
			}
		} else {
			if len(data) < offset+12 {
				return nil, errors.New("the clipboard image color masks are incomplete")
			}
			masks[0] = binary.LittleEndian.Uint32(data[offset : offset+4])
			masks[1] = binary.LittleEndian.Uint32(data[offset+4 : offset+8])
			masks[2] = binary.LittleEndian.Uint32(data[offset+8 : offset+12])
			offset += 12
		}
	}
	stride := ((width*bits + 31) / 32) * 4
	if stride < 0 || height > (len(data)-offset)/stride {
		return nil, errors.New("the clipboard image pixels are incomplete")
	}
	output := image.NewNRGBA(image.Rect(0, 0, width, height))
	hasAlpha := false
	if bits == 32 && compression == 0 {
		for row := 0; row < height && !hasAlpha; row++ {
			start := offset + row*stride
			for x := 0; x < width; x++ {
				if data[start+x*4+3] != 0 {
					hasAlpha = true
					break
				}
			}
		}
	}
	for y := 0; y < height; y++ {
		sourceY := y
		if !topDown {
			sourceY = height - 1 - y
		}
		row := data[offset+sourceY*stride:]
		for x := 0; x < width; x++ {
			var pixel color.NRGBA
			if bits == 24 {
				pixel = color.NRGBA{R: row[x*3+2], G: row[x*3+1], B: row[x*3], A: 255}
			} else if compression == 0 {
				alpha := byte(255)
				if hasAlpha {
					alpha = row[x*4+3]
				}
				pixel = color.NRGBA{R: row[x*4+2], G: row[x*4+1], B: row[x*4], A: alpha}
			} else {
				value := binary.LittleEndian.Uint32(row[x*4 : x*4+4])
				pixel = color.NRGBA{
					R: maskedChannel(value, masks[0]),
					G: maskedChannel(value, masks[1]),
					B: maskedChannel(value, masks[2]),
					A: maskedAlpha(value, masks[3]),
				}
			}
			output.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, output); err != nil {
		return nil, fmt.Errorf("encode clipboard image: %w", err)
	}
	return encoded.Bytes(), nil
}

func maskedChannel(value, mask uint32) byte {
	if mask == 0 {
		return 0
	}
	shift := 0
	for mask&(1<<shift) == 0 {
		shift++
	}
	component := (value & mask) >> shift
	maximum := mask >> shift
	return byte((uint64(component)*255 + uint64(maximum)/2) / uint64(maximum))
}

func maskedAlpha(value, mask uint32) byte {
	if mask == 0 {
		return 255
	}
	return maskedChannel(value, mask)
}
