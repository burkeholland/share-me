package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

type receipt struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Kind string `json:"kind"`
}

func received(item Item) receipt {
	return receipt{ID: item.ID, Name: item.Name, Size: item.Size, Kind: item.Kind}
}

func (s *Service) acquireUpload(w http.ResponseWriter) bool {
	select {
	case s.uploads <- struct{}{}:
		if !s.limiter.allow() {
			<-s.uploads
			s.respondError(w, problem(http.StatusTooManyRequests, "too many transfer requests; try again shortly"), "")
			return false
		}
		return true
	default:
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "four transfers are already active; try again shortly"})
		return false
	}
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	if !s.beginTransfer(w, r) {
		return
	}
	defer func() { <-s.uploads }()
	item, err := s.receiveFile(w, r)
	if err != nil {
		s.respondError(w, err, "could not save received file")
		return
	}
	writeJSON(w, http.StatusCreated, received(item))
	s.notify()
}

func multipartBody(w http.ResponseWriter, r *http.Request, limit int64) (*multipart.Reader, error) {
	contentType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "multipart/form-data" || parameters["boundary"] == "" {
		return nil, problem(http.StatusUnsupportedMediaType, "Content-Type must be multipart/form-data with a boundary")
	}
	if len(parameters["boundary"]) > 70 {
		return nil, problem(http.StatusBadRequest, "multipart boundary is too long")
	}
	if r.ContentLength > limit {
		return nil, problem(http.StatusRequestEntityTooLarge, "request body exceeds the allowed size")
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	r.Body = &multipartEnvelope{ReadCloser: r.Body, marker: []byte("\r\n--" + parameters["boundary"] + "--")}
	return multipart.NewReader(r.Body, parameters["boundary"]), nil
}

// multipart.Reader buffers past the final delimiter and otherwise ignores its
// epilogue. Observe the RFC CRLF-delimited raw stream so arbitrary trailing data
// cannot hide in that buffer. Keep only a delimiter-sized overlap, never the
// uploaded file in memory.
type multipartEnvelope struct {
	io.ReadCloser
	marker  []byte
	window  []byte
	found   bool
	invalid bool
	ending  int
}

func (body *multipartEnvelope) Read(destination []byte) (int, error) {
	n, err := body.ReadCloser.Read(destination)
	data := destination[:n]
	if !body.found {
		body.window = append(body.window, data...)
		if index := bytes.Index(body.window, body.marker); index >= 0 {
			body.found = true
			data = body.window[index+len(body.marker):]
		} else {
			overlap := min(len(body.window), len(body.marker)-1)
			copy(body.window, body.window[len(body.window)-overlap:])
			body.window = body.window[:overlap]
			return n, err
		}
	}
	for _, value := range data {
		switch body.ending {
		case 0:
			switch value {
			case ' ', '\t':
			case '\r':
				body.ending = 1
			case '\n':
				body.ending = 2
			default:
				body.invalid = true
			}
		case 1:
			if value != '\n' {
				body.invalid = true
			}
			body.ending = 2
		default:
			body.invalid = true
		}
	}
	return n, err
}

func partFields(part *multipart.Part, field string, file bool) (string, error) {
	disposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	name, hasFilename := params["filename"]
	if err != nil || disposition != "form-data" || params["name"] != field ||
		hasFilename != file || (file && (strings.TrimSpace(name) == "" || !utf8.ValidString(name))) ||
		part.Header.Get("Content-Transfer-Encoding") != "" {
		return "", problem(http.StatusBadRequest, "multipart request must contain exactly one "+field+" field")
	}
	return name, nil
}

// Check the closing boundary and consume the bounded transport body before
// publication. The phone contract deliberately does not accept MIME epilogues.
// This catches extra parts, truncated bodies, and read errors in trailing chunks.
func finishMultipart(reader *multipart.Reader, r *http.Request) error {
	_, err := reader.NextRawPart()
	if err == nil {
		return problem(http.StatusBadRequest, "multipart request must contain exactly one field")
	}
	if !errors.Is(err, io.EOF) {
		return bodyError(err, "malformed multipart closing boundary")
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return bodyError(err, "malformed trailing request body")
	}
	envelope, ok := r.Body.(*multipartEnvelope)
	if !ok || !envelope.found || envelope.invalid || envelope.ending == 1 {
		return problem(http.StatusBadRequest, "unexpected data after multipart closing boundary")
	}
	if err := r.Context().Err(); err != nil {
		return problem(http.StatusBadRequest, "transfer was cancelled")
	}
	return nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
	err    error
}

func (reader *contextReader) Read(bytes []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		reader.err = err
		return 0, err
	}
	n, err := reader.reader.Read(bytes)
	reader.err = err
	return n, err
}

func (s *Service) receiveFile(w http.ResponseWriter, r *http.Request) (item Item, result error) {
	reader, err := multipartBody(w, r, s.config.MaxFileBytes+fileBodyOverhead)
	if err != nil {
		return Item{}, err
	}
	part, err := reader.NextRawPart()
	if err != nil {
		return Item{}, bodyError(err, "multipart file field is missing or malformed")
	}
	name, err := partFields(part, "file", true)
	if err != nil {
		return Item{}, err
	}
	name, err = validatedFilename(name)
	if err != nil {
		return Item{}, err
	}
	declared, err := declaredFileSize(r, s.config.MaxFileBytes)
	if err != nil {
		return Item{}, err
	}
	if err := s.requireScanner(); err != nil {
		return Item{}, err
	}
	pending, err := s.requestApproval(r, "file", name, declared, "")
	if err != nil {
		return Item{}, err
	}
	defer s.removePending(pending)
	if pending.preflight != nil && pending.transfer.Size >= 0 {
		declared = pending.transfer.Size
	}
	defer func() { result = approvedError(r.Context(), result) }()
	// A subdirectory keeps unscanned files out of the final inbox view while
	// guaranteeing same-volume publication, including Windows NTFS ADS.
	quarantine := filepath.Join(s.config.InboxDir, ".shareme-quarantine")
	if err := os.MkdirAll(quarantine, 0700); err != nil {
		return Item{}, fmt.Errorf("create quarantine directory: %w", err)
	}
	file, err := os.CreateTemp(quarantine, ".shareme-upload-*"+filepath.Ext(name))
	if err != nil {
		return Item{}, fmt.Errorf("create incoming file: %w", err)
	}
	temporary, closed := file.Name(), false
	defer func() {
		var cleanupErr error
		if !closed {
			cleanupErr = file.Close()
		}
		if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove partial upload: %w", err))
		}
		if cleanupErr != nil {
			result = fmt.Errorf("clean up incoming file after %v: %w", result, cleanupErr)
		}
	}()
	source := &contextReader{ctx: r.Context(), reader: part}
	copyLimit := s.config.MaxFileBytes
	if declared >= 0 {
		copyLimit = min(copyLimit, declared)
	}
	size, err := io.Copy(file, io.LimitReader(source, copyLimit+1))
	if err != nil {
		if source.err != nil && errors.Is(err, source.err) {
			return Item{}, bodyError(err, "file body is incomplete or malformed")
		}
		return Item{}, fmt.Errorf("write incoming file: %w", err)
	}
	if size > s.config.MaxFileBytes {
		return Item{}, problem(http.StatusRequestEntityTooLarge, "file exceeds the configured maximum size")
	}
	if declared >= 0 && size != declared {
		if pending.preflight != nil {
			return Item{}, problem(http.StatusForbidden, "file size does not match the approved request")
		}
		return Item{}, problem(http.StatusBadRequest, "file size does not match X-Share-Me-Size")
	}
	if err := finishMultipart(reader, r); err != nil {
		return Item{}, err
	}
	var header [512]byte
	n, err := file.ReadAt(header[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return Item{}, fmt.Errorf("inspect received file: %w", err)
	}
	if err := file.Sync(); err != nil {
		return Item{}, fmt.Errorf("flush received file: %w", err)
	}
	err = file.Close()
	closed = true
	if err != nil {
		return Item{}, fmt.Errorf("close received file: %w", err)
	}
	if err := s.scanning(pending); err != nil {
		return Item{}, err
	}
	if err := s.config.ScanFile(r.Context(), temporary); err != nil {
		return Item{}, &apiError{
			status: http.StatusUnprocessableEntity, message: "File scanning failed; the file was not saved",
			cause: err,
		}
	}
	id := pending.transfer.ID
	item = Item{
		ID: id, Kind: "file", Name: name, Size: size, MIME: http.DetectContentType(header[:n]),
		CreatedAt: time.Now().UTC(), Path: filepath.Join(s.config.InboxDir, id+"-"+name),
	}
	if err := s.publishFile(r.Context(), temporary, item); err != nil {
		return Item{}, err
	}
	return item, nil
}

func (s *Service) text(w http.ResponseWriter, r *http.Request) {
	if !s.beginTransfer(w, r) {
		return
	}
	defer func() { <-s.uploads }()
	text, err := readText(w, r)
	if err != nil {
		s.respondError(w, err, "could not read text")
		return
	}
	if err := validateText(text); err != nil {
		s.respondError(w, err, "")
		return
	}
	pending, err := s.requestApproval(r, "text", "Text", int64(len(text)), text)
	if err != nil {
		s.respondError(w, err, "could not request text approval")
		return
	}
	defer s.removePending(pending)
	item, err := s.storeText(r.Context(), pending.transfer.ID, pending.transfer.Name, text)
	if err != nil {
		s.respondError(w, approvedError(r.Context(), err), "could not save received text")
		return
	}
	writeJSON(w, http.StatusCreated, received(item))
	s.notify()
}

func validateText(text string) error {
	if len(text) > maxTextBytes {
		return problem(http.StatusRequestEntityTooLarge, "text exceeds 64 KiB")
	}
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return problem(http.StatusBadRequest, "text must be nonblank UTF-8")
	}
	return nil
}

func (s *Service) requireScanner() error {
	if s.config.ScanFile == nil {
		return &apiError{
			status: http.StatusServiceUnavailable, message: "File scanning is unavailable on this PC",
			cause: errors.New("no file scanner is configured"),
		}
	}
	return nil
}

func (s *Service) beginTransfer(w http.ResponseWriter, r *http.Request) bool {
	if err := s.checkPreflight(r); err != nil {
		s.respondError(w, err, "")
		return false
	}
	return s.acquireUpload(w)
}

func readText(w http.ResponseWriter, r *http.Request) (string, error) {
	// JSON may escape each byte as \uXXXX; forms may percent-encode each byte.
	const bodyLimit = int64(maxTextBytes*6 + 64<<10)
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return "", problem(http.StatusUnsupportedMediaType, "unsupported text Content-Type")
	}
	switch contentType {
	case "application/json":
		fields, err := readJSONObject(w, r, bodyLimit, "text")
		return fields["text"], err
	case "application/x-www-form-urlencoded":
		r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			return "", bodyError(err, "malformed form body")
		}
		fields, err := url.ParseQuery(string(body))
		if err != nil || len(fields) != 1 || len(fields["text"]) != 1 {
			return "", problem(http.StatusBadRequest, "form must contain exactly one text field")
		}
		return fields["text"][0], nil
	case "multipart/form-data":
		reader, err := multipartBody(w, r, bodyLimit)
		if err != nil {
			return "", err
		}
		part, err := reader.NextRawPart()
		if err != nil {
			return "", bodyError(err, "multipart text field is missing or malformed")
		}
		if _, err := partFields(part, "text", false); err != nil {
			return "", err
		}
		body, err := io.ReadAll(io.LimitReader(part, maxTextBytes+1))
		if len(body) > maxTextBytes {
			return "", problem(http.StatusRequestEntityTooLarge, "text exceeds 64 KiB")
		}
		if err != nil {
			return "", bodyError(err, "malformed multipart text")
		}
		if err := finishMultipart(reader, r); err != nil {
			return "", err
		}
		return string(body), nil
	default:
		return "", problem(http.StatusUnsupportedMediaType, "text requires JSON, URL-encoded form, or multipart/form-data")
	}
}
