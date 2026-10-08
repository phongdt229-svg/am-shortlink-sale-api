package legacy

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

const maxBody = 8 << 20 // 8 MB (shorten-multi 1000 URL)

// Input: tham số request kiểu $request->input() của Lumen — gộp query + form + JSON body
// (body ưu tiên hơn query).
type Input map[string]any

func parseInput(r *http.Request) (Input, error) {
	in := Input{}
	for k, v := range r.URL.Query() {
		setForm(in, k, v)
	}
	ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch {
	case ct == "application/json" || strings.HasSuffix(ct, "+json"):
		b, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
		if err != nil {
			return nil, err
		}
		if len(strings.TrimSpace(string(b))) > 0 {
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				return in, nil // JSON hỏng: Lumen coi như không có input body
			}
			for k, v := range m {
				in[k] = v
			}
		}
	case ct == "application/x-www-form-urlencoded" || ct == "multipart/form-data":
		r.Body = http.MaxBytesReader(nil, r.Body, maxBody)
		if ct == "multipart/form-data" {
			if err := r.ParseMultipartForm(maxBody); err != nil {
				return in, nil
			}
		} else if err := r.ParseForm(); err != nil {
			return in, nil
		}
		for k, v := range r.PostForm {
			setForm(in, k, v)
		}
	}
	return in, nil
}

// setForm: hỗ trợ cú pháp PHP urls[0][path]=... và a[]=...
func setForm(in Input, key string, vals []string) {
	if len(vals) == 0 {
		return
	}
	v := vals[len(vals)-1]
	i := strings.IndexByte(key, '[')
	if i <= 0 || !strings.HasSuffix(key, "]") {
		in[key] = v
		return
	}
	root := key[:i]
	parts := strings.Split(strings.TrimSuffix(key[i+1:], "]"), "][")
	cur, ok := in[root].(map[string]any)
	if !ok {
		cur = map[string]any{}
		in[root] = cur
	}
	for j, p := range parts {
		if p == "" {
			p = strconv.Itoa(len(cur))
		}
		if j == len(parts)-1 {
			cur[p] = v
			break
		}
		next, ok := cur[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[p] = next
		}
		cur = next
	}
}

// Has: tham số có mặt và khác rỗng (validateRequired của Laravel).
func (in Input) Has(k string) bool {
	v, ok := in[k]
	if !ok || v == nil {
		return false
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t) != ""
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// Str: giá trị dạng chuỗi (số → chuỗi; mảng → "").
func (in Input) Str(k string) string { return toStr(in[k]) }

func toStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "1"
		}
		return "0"
	case json.Number:
		return t.String()
	}
	return ""
}

// Int: số nguyên hoặc def.
func (in Input) Int(k string, def int64) int64 {
	s := strings.TrimSpace(in.Str(k))
	if s == "" {
		return def
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return int64(f)
}

// List: mảng phần tử (JSON array hoặc form urls[0][...]). ok=false nếu không phải mảng.
func (in Input) List(k string) ([]any, bool) {
	switch t := in[k].(type) {
	case []any:
		return t, true
	case map[string]any:
		// form: khoá "0","1",... → giữ thứ tự số
		out := make([]any, 0, len(t))
		for i := 0; ; i++ {
			v, ok := t[strconv.Itoa(i)]
			if !ok {
				break
			}
			out = append(out, v)
		}
		if len(out) != len(t) {
			// khoá không liên tục: lấy theo thứ tự bất kỳ
			out = out[:0]
			for _, v := range t {
				out = append(out, v)
			}
		}
		return out, true
	}
	return nil, false
}

// item: phần tử object của mảng urls.
func item(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func itemStr(m map[string]any, k string) string { return toStr(m[k]) }
