// Package legacy: API tương thích v1/v2/v3 của hệ PHP (giữ path, tham số, envelope, thông báo lỗi tiếng Việt).
//
// Envelope: {"error": 0|1, "error_description": "...", "data": ...}. Lỗi nghiệp vụ trả HTTP 200 + error = 1
// (đúng như hệ cũ: ApiException được render thành JSON HTTP 200).
package legacy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"unicode/utf16"
	"unicode/utf8"
)

const msgOK = "Xử lý thành công!"

// msgOKNoBang: responseSuccess() của hệ cũ (không có dấu "!").
const msgOKNoBang = "Xử lý thành công"

type envelope struct {
	Error            int `json:"error"`
	ErrorDescription any `json:"error_description"`
	Data             any `json:"data"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := phpJSON(v)
	if err != nil {
		b = []byte(`{"error":1,"error_description":"Có lỗi xảy ra, Vui lòng thử lại","data":null}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// phpJSON mã hoá giống json_encode($v) mặc định của PHP: "/" → "\/", ký tự ngoài ASCII → \uXXXX
// (cặp surrogate cho ký tự > U+FFFF), không có xuống dòng cuối.
func phpJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	out := make([]byte, 0, len(raw)+len(raw)/8)
	for _, r := range string(raw) {
		switch {
		case r == '/':
			out = append(out, '\\', '/')
		case r < utf8.RuneSelf:
			out = append(out, byte(r))
		case r > 0xFFFF:
			r1, r2 := utf16.EncodeRune(r)
			out = fmt.Appendf(out, `\u%04x\u%04x`, r1, r2)
		default:
			out = fmt.Appendf(out, `\u%04x`, r)
		}
	}
	return out, nil
}

// ok: responseJson(true, msg, data) — msg rỗng → "Xử lý thành công!".
func ok(w http.ResponseWriter, msg string, data any) {
	if msg == "" {
		msg = msgOK
	}
	writeJSON(w, http.StatusOK, envelope{Error: 0, ErrorDescription: msg, Data: data})
}

// success: responseSuccess(data) — "Xử lý thành công".
func success(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, envelope{Error: 0, ErrorDescription: msgOKNoBang, Data: data})
}

// fail: ApiException / responseJson(false, ...) — HTTP 200, error = 1.
func fail(w http.ResponseWriter, msg any, data any) {
	writeJSON(w, http.StatusOK, envelope{Error: 1, ErrorDescription: msg, Data: data})
}

// apiError: lỗi nghiệp vụ trả từ service (giữ nguyên thông báo + data của hệ cũ).
type apiError struct {
	Msg  string
	Data any
}

func (e *apiError) Error() string { return e.Msg }

func errData(msg string, data any) *apiError { return &apiError{Msg: msg, Data: data} }

// nullEnding: data lỗi chuẩn của nhóm shorten.
func nullEnding() obj { return o("short_url", nil, "ending", nil) }
