package editrig

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"testing"
)

// buildMultipartRequest is the multipartBody trick from handler_test.go, but
// assembles a ready *http.Request: decodeBody is tested here directly,
// bypassing write() and routing.
func buildMultipartRequest(t *testing.T, payloadJSON string, fileParts map[string]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if payloadJSON != "" {
		if err := mw.WriteField("payload", payloadJSON); err != nil {
			t.Fatalf("write payload field: %v", err)
		}
	}
	for name, content := range fileParts {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="`+name+`"; filename="f.bin"`)
		hdr.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatalf("create part %s: %v", name, err)
		}
		if _, err := part.Write([]byte(content)); err != nil {
			t.Fatalf("write part %s: %v", name, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// buildMultipartRequestMulti is the same builder with SEVERAL parts per name:
// FormData allows repeated names, and a multi-media field sends them that way.
func buildMultipartRequestMulti(t *testing.T, payloadJSON string, parts []struct{ Name, Content string }) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if payloadJSON != "" {
		if err := mw.WriteField("payload", payloadJSON); err != nil {
			t.Fatalf("write payload field: %v", err)
		}
	}
	for i, p := range parts {
		hdr := textproto.MIMEHeader{}
		hdr.Set("Content-Disposition", `form-data; name="`+p.Name+`"; filename="f`+strconv.Itoa(i)+`.bin"`)
		hdr.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(hdr)
		if err != nil {
			t.Fatalf("create part %s: %v", p.Name, err)
		}
		if _, err := part.Write([]byte(p.Content)); err != nil {
			t.Fatalf("write part %s: %v", p.Name, err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// Several parts with one name is not "first wins": a multi-media field sends
// one part per file, and part order is the order of addition.
func TestDecodeBodyKeepsEveryPartOfOneField(t *testing.T) {
	req := buildMultipartRequestMulti(t, `{"data":{"gallery":["id-1"]}}`, []struct{ Name, Content string }{
		{"file:gallery", "aaa"},
		{"file:gallery", "bbb"},
	})
	rec := httptest.NewRecorder()

	_, files, cleanup, _, err := decodeBody(rec, req, BodyLimits{})
	defer cleanup()

	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	ups := files["gallery"]
	if len(ups) != 2 {
		t.Fatalf("uploads = %d, want 2", len(ups))
	}
	if string(ups[0].Data) != "aaa" || string(ups[1].Data) != "bbb" {
		t.Errorf("uploads = %q/%q, want aaa/bbb", ups[0].Data, ups[1].Data)
	}
}

// decodeBody on a JSON body: not multipart means plain json.Decode, and
// cleanup is a no-op (no temp files to remove).
func TestDecodeBodyJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte(`{"data":{"a":"x"}}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	in, files, cleanup, status, err := decodeBody(rec, req, BodyLimits{})
	defer cleanup()

	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if in["a"] != "x" {
		t.Errorf("in = %v, want a=x", in)
	}
	if files != nil {
		t.Errorf("files = %v, want nil on the JSON path", files)
	}
}

// decodeBody on a multipart body parses "payload" and "file:<field>"
// separately; files do NOT land in in (that is write() in handler.go, after
// structural validation).
func TestDecodeBodyMultipart(t *testing.T) {
	req := buildMultipartRequest(t, `{"data":{"a":"x"}}`, map[string]string{"file:photo": "\xff\xd8\xff"})
	rec := httptest.NewRecorder()

	in, files, cleanup, status, err := decodeBody(rec, req, BodyLimits{})
	defer cleanup()

	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if in["a"] != "x" {
		t.Errorf("in = %v, want a=x", in)
	}
	ups, ok := files["photo"]
	if !ok || len(ups) != 1 {
		t.Fatalf("files = %v, want a single \"photo\" entry", files)
	}
	up := ups[0]
	if up.Filename != "f.bin" || up.Mime != "application/octet-stream" || string(up.Data) != "\xff\xd8\xff" {
		t.Errorf("upload = %+v, want the submitted part", up)
	}
}

// Without a "payload" part in must be an empty map, not nil, or write() would
// see a difference from the JSON path, where body.Data==nil is already
// normalized to map{}.
func TestDecodeBodyMultipartWithoutPayload(t *testing.T) {
	req := buildMultipartRequest(t, "", map[string]string{"file:photo": "x"})
	rec := httptest.NewRecorder()

	in, _, cleanup, status, err := decodeBody(rec, req, BodyLimits{})
	defer cleanup()

	if err != nil {
		t.Fatalf("decodeBody: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if in == nil || len(in) != 0 {
		t.Errorf("in = %v, want an empty map", in)
	}
}

// A body larger than uploadMaxBytes is 413, and MaxBytesReader SETS THE BOUND,
// not ParseMultipartForm(multipartMemory): were the bound the memory
// threshold, this submit would pass. golang/go#58529 is the class of bug this
// guards against.
func TestDecodeBodyOversized(t *testing.T) {
	req := buildMultipartRequest(t, `{"data":{}}`, map[string]string{
		"file:photo": string(make([]byte, uploadMaxBytes+1)),
	})
	rec := httptest.NewRecorder()

	_, _, cleanup, status, err := decodeBody(rec, req, BodyLimits{})
	defer cleanup()

	if err == nil {
		t.Fatal("decodeBody: err = nil, want an error on an oversized body")
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", status)
	}
}
