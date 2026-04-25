package proxy

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"
)

func peekMultipartFields(body []byte, boundary string) (model string, stream bool, err error) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			return model, stream, nil
		}
		if err != nil {
			return "", false, err
		}
		name := part.FormName()
		if name != "model" && name != "stream" {
			continue
		}
		val, err := io.ReadAll(io.LimitReader(part, 1024))
		if err != nil {
			return "", false, err
		}
		switch name {
		case "model":
			model = strings.TrimSpace(string(val))
		case "stream":
			stream = parseMultipartBool(string(val))
		}
	}
}

func parseMultipartBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func rewriteMultipartField(body []byte, boundary, name, value string) ([]byte, error) {
	mr := multipart.NewReader(bytes.NewReader(body), boundary)
	var out bytes.Buffer
	mw := multipart.NewWriter(&out)
	if err := mw.SetBoundary(boundary); err != nil {
		return nil, err
	}

	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = mw.Close()
			return nil, err
		}

		header := cloneMIMEHeader(part.Header)
		dst, err := mw.CreatePart(header)
		if err != nil {
			_ = mw.Close()
			return nil, err
		}
		if part.FormName() == name {
			if _, err := io.WriteString(dst, value); err != nil {
				_ = mw.Close()
				return nil, err
			}
			continue
		}
		if _, err := io.Copy(dst, part); err != nil {
			_ = mw.Close()
			return nil, err
		}
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func cloneMIMEHeader(in textproto.MIMEHeader) textproto.MIMEHeader {
	out := make(textproto.MIMEHeader, len(in))
	for k, vals := range in {
		out[k] = append([]string(nil), vals...)
	}
	return out
}
