package urlparams

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNormalizePhone(t *testing.T) {
	cases := map[string]string{
		"0901234567":      "0901234567",
		"84901234567":     "0901234567",
		"+84 901 234 567": "0901234567",
		"0901 234 567":    "0901234567",
		"901234567":       "0901234567",
		"abc123hash":      "abc123hash",
		"  0901234567 ":   "0901234567",
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizePhone(in), in)
	}
}

func TestParse(t *testing.T) {
	p := Parse("https://Shop.FPT.vn/khuyen-mai?utm_source=Zalo&utm_extra_ctv=84901234567&phonenumber=0909&fullname=A&ref=R1#x", nil, "salt")
	assert.Equal(t, "shop.fpt.vn", p.DestHost)
	assert.Equal(t, "84901234567", p.CTVRaw)
	assert.Equal(t, "0901234567", p.CTVID)
	assert.Equal(t, "zalo", p.UTM["utm_source"])
	got := map[string]string{}
	for _, x := range p.Params {
		got[x.Key] = x.Value
	}
	assert.Equal(t, "zalo", got["utm_source"])
	assert.Equal(t, "0901234567", got["utm_extra_ctv"])
	assert.Equal(t, HashPII("salt", "0909"), got["phonenumber"], "pii=hash: chỉ lưu băm")
	_, hasName := got["fullname"]
	assert.False(t, hasName, "pii=drop: không lưu")
	assert.Equal(t, "R1", got["ref"])
}

func TestEscapeKey(t *testing.T) {
	assert.Equal(t, "zalo．me", EscapeKey("zalo.me"))
	assert.Equal(t, "(direct)", EscapeKey(""))
	assert.Equal(t, "＄x", EscapeKey("$x"))
}
