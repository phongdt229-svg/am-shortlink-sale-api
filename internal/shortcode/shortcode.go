// Package shortcode sinh và kiểm tra mã ngắn.
package shortcode

import (
	"crypto/rand"
	"math/big"
	"regexp"
)

// Bảng ký tự giống str_random của Laravel (base62).
const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var endingRe = regexp.MustCompile(`^[a-zA-Z0-9._+\-]+$`)

// Random sinh mã base62 độ dài n (crypto/rand). n <= 0 → 6.
func Random(n int) string {
	if n <= 0 {
		n = 6
	}
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		x, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err) // crypto/rand không lỗi trên hệ điều hành được hỗ trợ
		}
		b[i] = alphabet[x.Int64()]
	}
	return string(b)
}

// ValidEnding: luật custom_ending của hệ cũ (LinkHelper::validateEnding).
func ValidEnding(s string) bool { return endingRe.MatchString(s) }

// RandomHex sinh chuỗi hex độ dài n (dùng cho API key, giống CryptoHelper::generateRandomHex).
func RandomHex(n int) string {
	const h = "0123456789abcdef"
	b := make([]byte, n)
	max := big.NewInt(16)
	for i := range b {
		x, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic(err)
		}
		b[i] = h[x.Int64()]
	}
	return string(b)
}
