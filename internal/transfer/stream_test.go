package transfer

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

type chunkReader struct {
	reader io.Reader
	chunk  int
}

func (reader chunkReader) Read(data []byte) (int, error) {
	return reader.reader.Read(data[:min(len(data), reader.chunk)])
}

func TestMultipartEnvelopeAcrossChunkBoundaries(t *testing.T) {
	const marker = "\r\n--boundary--"
	for _, chunk := range []int{1, 2, 7, 71, 512, 32768} {
		for _, suffix := range []string{"", "\r\n", " \t\r\n", "garbage", "\r\nextra", "\r", "\r\n" + marker} {
			prefix := strings.Repeat("binary\x00\x01", 512) + "\n--boundary--\nnot a CRLF delimiter" + strings.Repeat("x", 99)
			input := prefix + marker + suffix
			envelope := &multipartEnvelope{
				ReadCloser: io.NopCloser(chunkReader{reader: strings.NewReader(input), chunk: chunk}),
				marker:     []byte(marker),
			}
			actual, err := io.ReadAll(envelope)
			if err != nil || !bytes.Equal(actual, []byte(input)) {
				t.Fatal("envelope changed streamed bytes", err)
			}
			valid := envelope.found && !envelope.invalid && envelope.ending != 1
			wantValid := suffix == "" || suffix == "\r\n" || suffix == " \t\r\n"
			if valid != wantValid {
				t.Fatalf("chunk=%d, suffix=%q: valid=%t", chunk, suffix, valid)
			}
		}
	}
}

func TestBoundaryLikeBinaryDataIsPreserved(t *testing.T) {
	f := newFixture(t, 1024)
	data := "\x00\x01\n--binary--\n\xffnear delimiter\r\n--binary-not-a-boundary\r\nend"
	body := "--binary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"binary.dat\"\r\n\r\n" +
		data + "\r\n--binary--\r\n"
	item := receiptItem(t, f, f.approved(t, "/api/upload", "multipart/form-data; boundary=binary", []byte(body)))
	if item.Size != int64(len(data)) {
		t.Fatal("binary content was confused with a closing delimiter")
	}
	stored, err := os.ReadFile(item.Path)
	if err != nil || !bytes.Equal(stored, []byte(data)) {
		t.Fatal("binary file bytes changed", err)
	}
}
