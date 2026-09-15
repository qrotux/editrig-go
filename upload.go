package editrig

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// uploadMaxBytes caps the WHOLE body of a multipart submit. A multi-media field
// sends one part per file, and a gallery of photos in one submit is the normal
// case, not abuse. The precise per-file limit is the entity's; this is the
// coarse bound of a cheap refusal.
const uploadMaxBytes = 128 << 20

// multipartMemory is the memory-versus-tempfile threshold for non-file parts,
// not a size limit.
const multipartMemory = 4 << 20

// filePartPrefix prefixes the name of a multipart part carrying a file for the
// field <name without prefix>. A part without it is either "payload" or junk
// that decodeBody silently ignores.
const filePartPrefix = "file:"

// errBodyTooLarge / errMalformedMultipart are the constant texts of two
// different multipart failures: oversize is 413, a broken boundary or truncated
// part is 400, a DIFFERENT cause. Constant rather than the parser's
// err.Error(): the raw mime/multipart message is a Go implementation detail,
// not something to show an admin (it occasionally leaks a temp file path).
var (
	errBodyTooLarge       = errors.New("body too large")
	errMalformedMultipart = errors.New("malformed multipart body")
)

// decodeBody reads the request body: plain JSON, or multipart with a "payload"
// part and "file:<field>" parts.
//
// ONLY MaxBytesReader BOUNDS THE BODY. ParseMultipartForm's maxMemory is purely
// the memory-versus-disk threshold: file parts beyond it go to temp files with
// NO limit (golang/go#58529, the same class as CVE-2022-41725), so an
// authenticated admin could fill the container disk without hitting a single
// error. RemoveAll is never called without the returned cleanup
// (golang/go#20253).
func decodeBody(w http.ResponseWriter, r *http.Request, limits BodyLimits) (in map[string]any, files map[string][]Upload, cleanup func(), status int, err error) {
	noop := func() {}
	limits = limits.defaults()

	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.Body = http.MaxBytesReader(w, r.Body, limits.JSONBytes)
		body, err := decodeJSONBody(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return nil, nil, noop, http.StatusRequestEntityTooLarge, errBodyTooLarge
			}
			return nil, nil, noop, http.StatusBadRequest, err
		}
		return body.Data, nil, noop, http.StatusOK, nil
	}

	r.Body = http.MaxBytesReader(w, r.Body, limits.MultipartBytes)
	if err := r.ParseMultipartForm(limits.MultipartMemory); err != nil {
		// http.MaxBytesReader signals overflow with a typed *http.MaxBytesError
		// (since Go 1.19); ONLY that case means "body larger than
		// uploadMaxBytes" and earns a 413. Any other parse error (broken
		// boundary, truncated part) is 400: it is not about size, and mixing
		// them up would answer "entity too large" to a broken but small
		// request. Neither is 500: the body came from the admin form, not from
		// a trusted client.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, nil, noop, http.StatusRequestEntityTooLarge, errBodyTooLarge
		}
		return nil, nil, noop, http.StatusBadRequest, errMalformedMultipart
	}
	cleanup = func() { _ = r.MultipartForm.RemoveAll() }
	// MIME parsing stops at the closing boundary; the epilogue is still part
	// of the bounded HTTP body, including when Content-Length is unknown.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		cleanup()
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, nil, noop, http.StatusRequestEntityTooLarge, errBodyTooLarge
		}
		return nil, nil, noop, http.StatusBadRequest, errMalformedMultipart
	}

	in = map[string]any{}
	if values := r.MultipartForm.Value["payload"]; len(values) > 0 && values[0] != "" {
		raw := values[0]
		var body requestBody
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			cleanup()
			return nil, nil, noop, http.StatusBadRequest, err
		}
		if body.Data != nil {
			in = body.Data
		}
	}

	files = map[string][]Upload{}
	for name, headers := range r.MultipartForm.File {
		field, ok := strings.CutPrefix(name, filePartPrefix)
		if !ok {
			continue
		}
		// ALL parts of the name, not the first: a multi-media field has as many
		// as the admin picked files, and part order is the order of addition.
		for _, fh := range headers {
			f, err := fh.Open()
			if err != nil {
				cleanup()
				return nil, nil, noop, http.StatusBadRequest, err
			}
			data, err := io.ReadAll(f)
			_ = f.Close()
			if err != nil {
				cleanup()
				return nil, nil, noop, http.StatusBadRequest, err
			}
			files[field] = append(files[field], Upload{Filename: fh.Filename, Mime: fh.Header.Get("Content-Type"), Data: data})
		}
	}

	return in, files, cleanup, http.StatusOK, nil
}

func decodeJSONBody(r io.Reader) (requestBody, error) {
	var body requestBody
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&body); err != nil {
		return body, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("expected a single JSON object")
		}
		return body, err
	}
	return body, nil
}
