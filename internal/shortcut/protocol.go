package shortcut

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultPort     = 49322
	maxPreflight    = 512 * 1024
	maxText         = 64 * 1024
	maxResponse     = 64 * 1024
	maxFileBytes    = int64(2 * 1024 * 1024 * 1024)
	maxCommandBytes = 1024
)

type Request struct {
	Operation  string
	ID         string
	Token      string
	Name       string
	DeviceID   string
	RemoteAddr string
	Body       io.Reader
}

type Response struct {
	StatusCode int
	Body       []byte
}

func validID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}

func validToken(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == value
}

func validName(name string, limit int) bool {
	if name == "" || len(name) > limit || !utf8.ValidString(name) || strings.TrimSpace(name) != name {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) || unicode.In(c, unicode.Cf) {
			return false
		}
	}
	return true
}

// Fields are protocol arguments, not a shell command. Quoting, escapes,
// substitutions, redirection and executable names have no special meaning.
func parseCommand(command string, setup bool) (Request, error) {
	bad := errors.New("invalid shareme-v1 command")
	if len(command) > maxCommandBytes {
		return Request{}, bad
	}
	for _, c := range command {
		if c > 127 || c < 32 && c != '\t' && c != '\n' && c != '\r' || c == 127 {
			return Request{}, bad
		}
	}
	if setup {
		if command != "shareme-v1 setup" {
			return Request{}, errors.New("setup authentication only permits shareme-v1 setup")
		}
		return Request{Operation: "setup"}, nil
	}
	fields := strings.Fields(command)
	if len(fields) < 2 || fields[0] != "shareme-v1" {
		return Request{}, bad
	}
	req := Request{Operation: fields[1]}
	switch req.Operation {
	case "request":
		if len(fields) != 2 {
			return Request{}, bad
		}
		return req, nil
	case "status", "cancel", "text":
		if len(fields) != 4 {
			return Request{}, bad
		}
	case "upload":
		if len(fields) != 5 {
			return Request{}, bad
		}
		name, err := base64.StdEncoding.Strict().DecodeString(fields[4])
		if err != nil || base64.StdEncoding.EncodeToString(name) != fields[4] ||
			!validName(string(name), 255) || bytes.ContainsAny(name, `/\:`) || string(name) == "." || string(name) == ".." {
			return Request{}, errors.New("invalid encoded filename")
		}
		req.Name = string(name)
	default:
		return Request{}, bad
	}
	req.ID, req.Token = fields[2], fields[3]
	if !validID(req.ID) || !validToken(req.Token) {
		return Request{}, bad
	}
	return req, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("stdin exceeds operation limit")
	}
	return data, nil
}

type streamBody struct {
	ctx       context.Context
	reader    io.Reader
	remaining int64
	eof       bool
	err       error
}

func (b *streamBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		b.err = err
		return 0, err
	}
	if b.err != nil {
		return 0, b.err
	}
	if b.eof {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.reader.Read(extra[:])
		if n != 0 {
			b.err = errors.New("file exceeds upload limit")
			return 0, b.err
		}
		if err == io.EOF {
			b.eof = true
		} else if err != nil {
			b.err = err
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.reader.Read(p)
	b.remaining -= int64(n)
	if err == io.EOF {
		b.eof = true
	} else if err != nil {
		b.err = err
	}
	return n, err
}

func (b *streamBody) finish() error {
	if b.err != nil {
		return b.err
	}
	if b.eof {
		return b.ctx.Err()
	}
	var extra [1]byte
	n, err := b.Read(extra[:])
	if n != 0 || err != io.EOF {
		return errors.New("upload handler must consume stdin through EOF")
	}
	return nil
}

func validResponse(response Response) bool {
	return response.StatusCode >= 200 && response.StatusCode <= 599 &&
		len(response.Body) <= maxResponse && utf8.Valid(response.Body) && json.Valid(response.Body)
}
