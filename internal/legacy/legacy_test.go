package legacy

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPHPJSON(t *testing.T) {
	b, err := phpJSON(envelope{Error: 0, ErrorDescription: "Xử lý thành công!", Data: o("short_url", "https://fpt.vn/sale/abc")})
	require.NoError(t, err)
	assert.Equal(t, "{\"error\":0,\"error_description\":\"X\\u1eed l\\u00fd th\\u00e0nh c\\u00f4ng!\",\"data\":{\"short_url\":\"https:\\/\\/fpt.vn\\/sale\\/abc\"}}", string(b))

	b, _ = phpJSON("😀")
	assert.Equal(t, "\"\\ud83d\\ude00\"", string(b))
}

func TestIsURL(t *testing.T) {
	assert.True(t, isURL("https://fpt.vn/a?b=1"))
	assert.True(t, isURL(encodeSpaces("https://fpt.vn/a b")))
	assert.False(t, isURL("fpt.vn/a"))
	assert.False(t, isURL("https://"))
	assert.False(t, isURL("https://fpt.vn/ả")) // FILTER_VALIDATE_URL chỉ nhận ASCII
}

func TestShortenRulesOrderAndMessages(t *testing.T) {
	assert.Equal(t, "Đường link dài không được trống!", firstError(Input{}, shortenRules))
	assert.Equal(t, "Đường link dài không đúng định dạng!", firstError(Input{"url": "abc"}, shortenRules))
	assert.Equal(t, "Link rút gọn phải lớn hơn 5 ký tự!", firstError(Input{"url": "https://fpt.vn", "custom_ending": "abc"}, shortenRules))
	assert.Equal(t, "", firstError(Input{"url": "https://fpt.vn", "custom_ending": ""}, shortenRules), "rỗng = bỏ qua như Laravel")
	assert.Equal(t, "Code chiến dịch  không được lớn hơn 40 ký tự!", firstError(Input{"url": "https://fpt.vn", "code": strings.Repeat("x", 41)}, shortenRules))
}

func TestParseInputFormArray(t *testing.T) {
	body := "key=k&urls[0][path]=https://a.vn&urls[0][key_item]=1&urls[1][path]=https://b.vn"
	r := httptest.NewRequest("POST", "/api/v2/shorten-multi?x=1", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	in, err := parseInput(r)
	require.NoError(t, err)
	assert.Equal(t, "k", in.Str("key"))
	assert.Equal(t, "1", in.Str("x"))
	list, ok := in.List("urls")
	require.True(t, ok)
	require.Len(t, list, 2)
	m, _ := item(list[0])
	assert.Equal(t, "https://a.vn", itemStr(m, "path"))
	m, _ = item(list[1])
	assert.Equal(t, "https://b.vn", itemStr(m, "path"))
}

func TestParseInputJSON(t *testing.T) {
	r := httptest.NewRequest("POST", "/api/v3/shorten?key=q", strings.NewReader(`{"key":"k","url":"https://fpt.vn","n":5}`))
	r.Header.Set("Content-Type", "application/json")
	in, err := parseInput(r)
	require.NoError(t, err)
	assert.Equal(t, "k", in.Str("key"), "body ưu tiên hơn query")
	assert.Equal(t, "5", in.Str("n"))
}

func TestPaginator(t *testing.T) {
	p := paginator([]obj{{}, {}}, 12, 2, 10)
	assert.Equal(t, int64(2), g(p, "last_page"))
	assert.Equal(t, int64(11), g(p, "from"))
	assert.Equal(t, int64(12), g(p, "to"))
	empty := paginator([]obj{}, 0, 1, 10)
	assert.Nil(t, g(empty, "from"))
	assert.Equal(t, int64(1), g(empty, "last_page"))
}

func TestLastSegment(t *testing.T) {
	assert.Equal(t, "abc", lastSegment("https://fpt.vn/sale/abc/"))
	assert.Equal(t, "", lastSegmentRaw("https://fpt.vn/sale/abc/"))
	assert.Equal(t, "abc", lastSegmentRaw("https://fpt.vn/sale/abc"))
}

func TestIPAllowed(t *testing.T) {
	nets := parseNets([]string{"10.0.0.0/8", "113.161.1.2"})
	assert.True(t, ipAllowed("10.1.2.3", nets))
	assert.True(t, ipAllowed("113.161.1.2", nets))
	assert.False(t, ipAllowed("113.161.1.3", nets))
}

func g(x obj, k string) any { v, _ := x.get(k); return v }
