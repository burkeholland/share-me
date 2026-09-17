package transfer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"
)

func TestFileNamesUnicodeAndDuplicates(t *testing.T) {
	for _, name := range []string{"こんにちは 🌷.jpg", "../../escape.txt", `C:\folder\file.jpg`,
		"CON.txt", "aux", "LPT1.log", "COM¹.txt", "file.txt:stream", "name. ", "..", strings.Repeat("界", 200) + ".txt"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, 128)
			paths := make(map[string]bool)
			for _, content := range []string{"first", "second"} {
				body, contentType := multipartBytes(t, partSpec{field: "file", name: name, data: content, file: true})
				item := receiptItem(t, f, f.approved(t, "/api/upload", contentType, body))
				if item.Name != sanitizeFilename(name) || !utf8.ValidString(item.Name) ||
					!filepath.IsAbs(item.Path) || !containedPath(f.config.InboxDir, item.Path) ||
					paths[item.Path] || strings.ContainsAny(item.Name, `<>:"/\|?*`) {
					t.Fatalf("unsafe or duplicated item: %#v", item)
				}
				paths[item.Path] = true
				data, err := os.ReadFile(item.Path)
				if err != nil || string(data) != content {
					t.Fatal("received content changed", err)
				}
			}
			items, err := f.service.List()
			if err != nil || len(items) != 2 {
				t.Fatal("duplicate display names overwrote metadata", err)
			}
			first, err := os.ReadFile(items[1].Path)
			if err != nil || string(first) != "first" {
				t.Fatal("later upload overwrote original file", err)
			}
		})
	}
}

func TestDeclaredFileSizeValidationAndExactMatch(t *testing.T) {
	for _, test := range []struct {
		size   string
		status int
		prompt bool
	}{
		{"-1", 400, false}, {"+5", 400, false}, {"1.5", 400, false}, {"", 400, false},
		{"999999999999999999999999", 413, false}, {"129", 413, false},
		{"4", 400, true}, {"6", 400, true}, {"5", 201, true}, {"0", 400, true},
	} {
		t.Run(test.size, func(t *testing.T) {
			var prompts, scans atomic.Int32
			f := newFixture(t, 128, func(config *Config) {
				config.ScanFile = func(context.Context, string) error { scans.Add(1); return nil }
				config.OnChange = func() { prompts.Add(1) }
			})
			before := prompts.Load()
			body, contentType := multipartBytes(t, partSpec{field: "file", name: "sized.jpg", data: "12345", file: true})
			result := f.approved(t, "/api/upload", contentType, body, func(r *http.Request) { r.Header.Set("X-Share-Me-Size", test.size) })
			assertStatus(t, result, test.status)
			if !test.prompt && prompts.Load() != before {
				t.Fatal("invalid declared size prompted the desktop")
			}
			if test.status == 201 {
				receiptItem(t, f, result)
				if scans.Load() != 1 {
					t.Fatal("valid file was not scanned")
				}
			} else {
				if scans.Load() != 0 {
					t.Fatal("size mismatch was scanned")
				}
				assertUnpublished(t, f)
			}
		})
	}
	f := newFixture(t, 128)
	body, contentType := multipartBytes(t, partSpec{field: "file", name: "empty.txt", file: true})
	assertStatus(t, f.call(t, "POST", "/api/upload", contentType, body, func(r *http.Request) {
		r.Header.Add("X-Share-Me-Size", "0")
		r.Header.Add("X-Share-Me-Size", "0")
	}), http.StatusBadRequest)
	item := receiptItem(t, f, f.approved(t, "/api/upload", contentType, body,
		func(r *http.Request) { r.Header.Set("X-Share-Me-Size", "0") }))
	if item.Size != 0 {
		t.Fatal("zero-byte approved file not accepted")
	}
}

func TestMalformedAndOversizeUploadsNeverPublishOrScan(t *testing.T) {
	good, contentType := multipartBytes(t, partSpec{field: "file", name: "a.txt", data: "hello", file: true})
	large, largeType := multipartBytes(t, partSpec{field: "file", name: "a.txt", data: "hello!", file: true})
	extra, extraType := multipartBytes(t,
		partSpec{field: "file", name: "a.txt", data: "first", file: true},
		partSpec{field: "file", name: "b.txt", data: "later", file: true})
	field, fieldType := multipartBytes(t,
		partSpec{field: "file", name: "a.txt", data: "first", file: true}, partSpec{field: "extra", data: "unexpected"})
	wrong, wrongType := multipartBytes(t, partSpec{field: "text", data: "hello"})
	emptyName, emptyType := multipartBytes(t, partSpec{field: "file", name: "", data: "hello", file: true})
	for _, test := range []struct {
		name, contentType string
		body              []byte
		status            int
	}{
		{"oversize-file", largeType, large, http.StatusRequestEntityTooLarge},
		{"second-file", extraType, extra, http.StatusBadRequest},
		{"extra-field", fieldType, field, http.StatusBadRequest},
		{"wrong-field", wrongType, wrong, http.StatusBadRequest},
		{"no-filename", emptyType, emptyName, http.StatusBadRequest},
		{"truncated-end", contentType, good[:len(good)-8], http.StatusBadRequest},
		{"no-body", contentType, nil, http.StatusBadRequest},
		{"no-boundary", "multipart/form-data", good, http.StatusUnsupportedMediaType},
		{"not-multipart", "application/json", []byte(`{}`), http.StatusUnsupportedMediaType},
		{"trailing-data", contentType, append(append([]byte(nil), good...), []byte("epilogue")...), http.StatusBadRequest},
		{"second-envelope", contentType, append(append([]byte(nil), good...), good...), http.StatusBadRequest},
		{"oversize-trailing", contentType, append(append([]byte(nil), good...), []byte(strings.Repeat("x", int(fileBodyOverhead)))...), http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			var scans atomic.Int32
			f := newFixture(t, 5, func(config *Config) {
				config.ScanFile = func(context.Context, string) error { scans.Add(1); return nil }
			})
			result := f.approved(t, "/api/upload", test.contentType, test.body, func(r *http.Request) { r.ContentLength = -1 })
			assertStatus(t, result, test.status)
			object(t, result, "error")
			if scans.Load() != 0 {
				t.Fatal("malformed request was scanned")
			}
			waitPending(t, f, 0)
			assertUnpublished(t, f)
		})
	}
}

func TestTextFormatsLimitsAndWhitespace(t *testing.T) {
	for _, format := range []string{"json", "form", "multipart"} {
		t.Run(format, func(t *testing.T) {
			f := newFixture(t, 128)
			for _, test := range []struct {
				text   string
				status int
			}{
				{" \n café 日本語 \r\n", http.StatusCreated},
				{strings.Repeat("a", maxTextBytes), http.StatusCreated},
				{strings.Repeat("a", maxTextBytes+1), http.StatusRequestEntityTooLarge},
				{" \r\n\t", http.StatusBadRequest}, {"", http.StatusBadRequest},
			} {
				var body []byte
				var contentType string
				switch format {
				case "json":
					body, _ = json.Marshal(map[string]string{"text": test.text})
					contentType = "application/json"
				case "form":
					body = []byte(url.Values{"text": {test.text}}.Encode())
					contentType = "application/x-www-form-urlencoded"
				case "multipart":
					body, contentType = multipartBytes(t, partSpec{field: "text", data: test.text})
				}
				result := f.approved(t, "/api/text", contentType, body)
				assertStatus(t, result, test.status)
				if test.status == http.StatusCreated {
					item := receiptItem(t, f, result)
					if item.Text != test.text || item.Size != int64(len(test.text)) || item.Path != "" || item.Kind != "text" {
						t.Fatal("saved text or native metadata changed")
					}
				} else {
					object(t, result, "error")
				}
			}
			items, err := f.service.List()
			if err != nil || len(items) != 2 {
				t.Fatal("invalid text was committed", err)
			}
		})
	}
}

func TestStrictTextBodiesRejectBeforeApproval(t *testing.T) {
	var cases []struct{ contentType, body string }
	for _, body := range []string{`null`, `[]`, `{}`, `{"text":null}`, `{"Text":"alias"}`, `{"text":1}`,
		`{"text":"a","text":"b"}`, `{"text":"a","other":"b"}`, `{"text":"a"} false`, "{\"text\":\"\xff\"}"} {
		cases = append(cases, struct{ contentType, body string }{"application/json", body})
	}
	for _, body := range []string{"text=a&text=b", "text=a&other=b", "text=%zz", "wrong=a", "text=%ff"} {
		cases = append(cases, struct{ contentType, body string }{"application/x-www-form-urlencoded", body})
	}
	for _, test := range cases {
		t.Run(test.body, func(t *testing.T) {
			f := newFixture(t, 128)
			assertStatus(t, f.call(t, "POST", "/api/text", test.contentType, []byte(test.body)), http.StatusBadRequest)
			if len(f.service.Pending()) != 0 {
				t.Fatal("invalid text prompted approval")
			}
			assertUnpublished(t, f)
		})
	}
	f := newFixture(t, 128)
	for _, parts := range [][]partSpec{
		{{field: "text", name: "file.txt", data: "a", file: true}},
		{{field: "text", data: "a"}, {field: "text", data: "b"}},
		{{field: "other", data: "a"}},
	} {
		body, contentType := multipartBytes(t, parts...)
		assertStatus(t, f.call(t, "POST", "/api/text", contentType, body), http.StatusBadRequest)
	}
	body, contentType := multipartBytes(t, partSpec{field: "text", data: "a"})
	assertStatus(t, f.call(t, "POST", "/api/text", contentType, body[:len(body)-8]), http.StatusBadRequest)
	assertStatus(t, f.call(t, "POST", "/api/text", "text/plain", []byte("no")), http.StatusUnsupportedMediaType)
	assertStatus(t, f.call(t, "POST", "/api/text", "application/json", []byte(strings.Repeat(" ", maxTextBytes*8))), http.StatusRequestEntityTooLarge)
	assertUnpublished(t, f)
	escaped := []byte(`{"text":"` + strings.Repeat(`\u0061`, maxTextBytes) + `"}`)
	item := receiptItem(t, f, f.approved(t, "/api/text", "application/json", escaped))
	if item.Size != maxTextBytes {
		t.Fatal("escaped JSON at the text limit was rejected")
	}
}
