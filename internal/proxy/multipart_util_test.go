package proxy

import (
	"bytes"
	"io"
	"mime/multipart"
	"strings"
	"testing"
)

func TestPeekMultipartFields(t *testing.T) {
	body, boundary := makeMultipartBody(t, func(w *multipart.Writer) {
		mustWriteField(t, w, "model", "gpt-image-2")
		mustWriteField(t, w, "stream", "true")
		part, err := w.CreateFormFile("image[]", "a.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("png-bytes"))
	})

	model, stream, err := peekMultipartFields(body, boundary)
	if err != nil {
		t.Fatalf("peekMultipartFields: %v", err)
	}
	if model != "gpt-image-2" || !stream {
		t.Fatalf("model=%q stream=%v", model, stream)
	}
}

func TestRewriteMultipartField(t *testing.T) {
	body, boundary := makeMultipartBody(t, func(w *multipart.Writer) {
		mustWriteField(t, w, "model", "alias-image")
		mustWriteField(t, w, "prompt", "make it sharper")
		part, err := w.CreateFormFile("image[]", "a.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("png-bytes"))
	})

	rewritten, err := rewriteMultipartField(body, boundary, "model", "gpt-image-2")
	if err != nil {
		t.Fatalf("rewriteMultipartField: %v", err)
	}
	mr := multipart.NewReader(bytes.NewReader(rewritten), boundary)
	fields := map[string]string{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		data, _ := io.ReadAll(part)
		fields[part.FormName()] = string(data)
	}
	if fields["model"] != "gpt-image-2" {
		t.Fatalf("model = %q", fields["model"])
	}
	if fields["prompt"] != "make it sharper" {
		t.Fatalf("prompt = %q", fields["prompt"])
	}
	if !strings.Contains(fields["image[]"], "png-bytes") {
		t.Fatalf("image bytes not preserved: %q", fields["image[]"])
	}
}

func makeMultipartBody(t *testing.T, fill func(*multipart.Writer)) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fill(w)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), w.Boundary()
}

func mustWriteField(t *testing.T, w *multipart.Writer, name, value string) {
	t.Helper()
	if err := w.WriteField(name, value); err != nil {
		t.Fatal(err)
	}
}
