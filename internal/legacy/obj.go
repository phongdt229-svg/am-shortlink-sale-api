package legacy

import (
	"bytes"
	"encoding/json"
)

// obj: object JSON GIỮ THỨ TỰ KHOÁ như mảng kết hợp PHP (map của Go bị sắp theo alphabet).
type obj []field

type field struct {
	k string
	v any
}

// o("a", 1, "b", 2) → {"a":1,"b":2}.
func o(kv ...any) obj {
	out := make(obj, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, field{kv[i].(string), kv[i+1]})
	}
	return out
}

// set ghi đè khoá đã có hoặc thêm vào cuối.
func (x *obj) set(k string, v any) {
	for i := range *x {
		if (*x)[i].k == k {
			(*x)[i].v = v
			return
		}
	}
	*x = append(*x, field{k, v})
}

func (x *obj) get(k string) (any, bool) {
	for _, f := range *x {
		if f.k == k {
			return f.v, true
		}
	}
	return nil, false
}

func (x obj) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range x {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.k)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		v, err := marshalNoEscape(f.v)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}
