package legacy

import (
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// isURL ~ filter_var(FILTER_VALIDATE_URL) của PHP (rule "url" của Laravel 5.1):
// cần scheme + host, không khoảng trắng / ký tự điều khiển.
func isURL(s string) bool {
	if s == "" || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			return false // FILTER_VALIDATE_URL chỉ nhận ASCII
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "mailto", "news", "file":
		return u.Opaque != "" || u.Path != ""
	}
	return u.Hostname() != ""
}

// encodeSpaces: hệ cũ thay " " → "%20" trước khi kiểm url.
func encodeSpaces(s string) string { return strings.ReplaceAll(s, " ", "%20") }

func strLen(s string) int { return utf8.RuneCountInString(s) }

func isNumeric(s string) bool {
	_, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return err == nil
}

// rule: một luật kiểm tra của một trường; trả thông báo lỗi hoặc "".
type rule struct {
	field string
	check func(in Input) string
}

// firstError: thông báo của luật đầu tiên không đạt ($validator->errors()->first()).
func firstError(in Input, rules []rule) string {
	for _, r := range rules {
		if msg := r.check(in); msg != "" {
			return msg
		}
	}
	return ""
}

// allErrors: MessageBag dạng {"field": ["msg", ...]} (responseJson(false, $validator->errors())).
func allErrors(in Input, rules []rule) obj {
	var out obj
	for _, r := range rules {
		if msg := r.check(in); msg != "" {
			out.addMsg(r.field, msg)
		}
	}
	return out
}

// addMsg thêm thông báo vào danh sách của field (giữ thứ tự field như MessageBag).
func (x *obj) addMsg(field, msg string) {
	if v, ok := x.get(field); ok {
		x.set(field, append(v.([]string), msg))
		return
	}
	x.set(field, []string{msg})
}

func required(field, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return msg
		}
		return ""
	}}
}

// urlRule: chỉ kiểm khi có giá trị (luật không ngầm định của Laravel).
func urlRule(field, msg string) rule {
	return rule{field, func(in Input) string {
		if in.Has(field) && !isURL(encodeSpaces(in.Str(field))) {
			return msg
		}
		return ""
	}}
}

func minLen(field string, n int, msg string) rule {
	return rule{field, func(in Input) string {
		if in.Has(field) && strLen(in.Str(field)) < n {
			return msg
		}
		return ""
	}}
}

func maxLen(field string, n int, msg string) rule {
	return rule{field, func(in Input) string {
		if in.Has(field) && strLen(in.Str(field)) > n {
			return msg
		}
		return ""
	}}
}

func numeric(field, msg string) rule {
	return rule{field, func(in Input) string {
		if in.Has(field) && !isNumeric(in.Str(field)) {
			return msg
		}
		return ""
	}}
}

func maxNum(field string, n float64, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return ""
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(in.Str(field)), 64)
		if err == nil && f > n {
			return msg
		}
		return ""
	}}
}

func minNum(field string, n float64, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return ""
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(in.Str(field)), 64)
		if err == nil && f < n {
			return msg
		}
		return ""
	}}
}

func isArray(field, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return ""
		}
		if _, ok := in.List(field); !ok {
			return msg
		}
		return ""
	}}
}

func oneOf(field string, vals []string, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return ""
		}
		v := in.Str(field)
		for _, x := range vals {
			if v == x {
				return ""
			}
		}
		return msg
	}}
}

func isString(field, msg string) rule {
	return rule{field, func(in Input) string {
		if !in.Has(field) {
			return ""
		}
		if _, ok := in[field].(string); !ok {
			return msg
		}
		return ""
	}}
}

// lastSegment: phần sau dấu "/" cuối (short_url đầy đủ → mã).
func lastSegment(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}
