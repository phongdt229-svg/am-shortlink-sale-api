// Package urlparams tách tham số từ long_url (§5.4b) và các chuẩn hoá dùng chung với Portal.
package urlparams

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"

	"am-shortlink-service/internal/domain"
)

const (
	MaxParams   = 50
	MaxKeyLen   = 64
	MaxValueLen = 256
	CTVKey      = "utm_extra_ctv"
)

// Rule: cấu hình xử lý một tham số (từ param_registry).
type Rule struct {
	Tracked   bool
	PII       string   // none | hash | drop
	Normalize []string // lower, trim, phone
}

// DefaultRules: dùng khi param_registry chưa có key (giống seed của Portal).
var DefaultRules = map[string]Rule{
	"utm_source":    {Tracked: true, PII: "none", Normalize: []string{"lower", "trim"}},
	"utm_medium":    {Tracked: true, PII: "none", Normalize: []string{"lower", "trim"}},
	"utm_campaign":  {Tracked: true, PII: "none", Normalize: []string{"lower", "trim"}},
	"utm_content":   {Tracked: true, PII: "none", Normalize: []string{"trim"}},
	"utm_term":      {Tracked: true, PII: "none", Normalize: []string{"trim"}},
	"utm_extra_ctv": {Tracked: true, PII: "none", Normalize: []string{"phone"}},
	"phonenumber":   {PII: "hash", Normalize: []string{"phone"}},
	"fullname":      {PII: "drop"},
	"email":         {PII: "drop"},
	"guestid":       {PII: "hash", Normalize: []string{"trim"}},
}

// Parsed: kết quả tách long_url.
type Parsed struct {
	DestHost string
	Params   []domain.Param // đã chuẩn hoá + xử lý PII, sắp theo key
	CTVRaw   string
	CTVID    string
	UTM      map[string]string // utm_* (trừ utm_extra_ctv) đã chuẩn hoá — ghi kèm click
}

// Parse tách long_url theo rules (thiếu key → DefaultRules → mặc định trim).
// salt: PII_HASH_SALT (cùng công thức Portal: hex(SHA-256(salt||value))).
func Parse(longURL string, rules map[string]Rule, salt string) Parsed {
	var p Parsed
	u, err := url.Parse(strings.TrimSpace(longURL))
	if err != nil {
		return p
	}
	p.DestHost = strings.ToLower(u.Hostname())
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		// Query hỏng một phần: ParseQuery vẫn trả phần đọc được.
		if q == nil {
			return p
		}
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		if k == "" || len(k) > MaxKeyLen {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	p.UTM = map[string]string{}
	for _, k := range keys {
		r, ok := rules[k]
		if !ok {
			if r, ok = DefaultRules[k]; !ok {
				r = Rule{PII: "none", Normalize: []string{"trim"}}
			}
		}
		for _, raw := range q[k] {
			if k == CTVKey && p.CTVRaw == "" && strings.TrimSpace(raw) != "" {
				p.CTVRaw = raw
				p.CTVID = NormalizePhone(raw)
			}
			if r.PII == "drop" {
				continue
			}
			v := Normalize(raw, r.Normalize)
			if v == "" {
				continue
			}
			if r.PII == "hash" {
				p.Params = append(p.Params, domain.Param{Key: k, Value: HashPII(salt, v)})
				continue
			}
			if len(v) > MaxValueLen {
				v = strings.ToValidUTF8(v[:MaxValueLen-17], "") + "~" + shortHash(v)
			}
			if strings.HasPrefix(k, "utm_") && k != CTVKey {
				if _, seen := p.UTM[k]; !seen {
					p.UTM[k] = v
				}
			}
			param := domain.Param{Key: k, Value: v}
			if raw != v {
				param.Raw = raw
			}
			p.Params = append(p.Params, param)
			if len(p.Params) >= MaxParams {
				return p
			}
		}
	}
	return p
}

// Normalize áp các bước chuẩn hoá.
func Normalize(v string, steps []string) string {
	v = strings.TrimSpace(v)
	for _, s := range steps {
		switch s {
		case "lower":
			v = strings.ToLower(v)
		case "phone":
			v = NormalizePhone(v)
		}
	}
	return v
}

// NormalizePhone: SĐT → 0xxxxxxxxx; không phải SĐT (hash) → giữ nguyên (trim). Giống Portal mask.NormalizePhone.
func NormalizePhone(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else if r != ' ' && r != '.' && r != '-' && r != '+' && r != '(' && r != ')' {
			return s
		}
	}
	d := b.String()
	switch {
	case len(d) == 11 && strings.HasPrefix(d, "84"):
		d = "0" + d[2:]
	case len(d) == 9 && d[0] != '0':
		d = "0" + d
	}
	if len(d) == 10 && d[0] == '0' {
		return d
	}
	return s
}

// HashPII: hex(SHA-256(salt || value)) — trùng công thức Portal.
func HashPII(salt, v string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	h.Write([]byte(v))
	return hex.EncodeToString(h.Sum(nil))
}

func shortHash(v string) string {
	s := sha256.Sum256([]byte(v))
	return hex.EncodeToString(s[:8])
}

// EscapeKey: khoá map by_* trong Mongo ("." → "．", rỗng → "(direct)", "$" đầu → "＄").
func EscapeKey(k string) string {
	if k == "" {
		return "(direct)"
	}
	k = strings.ReplaceAll(k, ".", "．")
	if strings.HasPrefix(k, "$") {
		k = "＄" + k[1:]
	}
	return k
}
