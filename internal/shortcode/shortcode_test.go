package shortcode

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		c := Random(6)
		assert.Len(t, c, 6)
		assert.True(t, ValidEnding(c))
		seen[c] = true
	}
	assert.Greater(t, len(seen), 990)
}

func TestValidEnding(t *testing.T) {
	assert.True(t, ValidEnding("km-2026_T9.a+b"))
	assert.False(t, ValidEnding("có dấu"))
	assert.False(t, ValidEnding("a/b"))
	assert.False(t, ValidEnding(""))
}
