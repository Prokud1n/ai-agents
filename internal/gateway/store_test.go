package gateway

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRedisStoreKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		prefix         string
		expectedOffset string
		expectedSeen   string
	}{
		{
			name:           "production keys without prefix",
			expectedOffset: "telegram:offset:42",
			expectedSeen:   "telegram:seen:message:-100:7",
		},
		{
			name:           "test prefix",
			prefix:         "test:1:2:3:",
			expectedOffset: "test:1:2:3:telegram:offset:42",
			expectedSeen:   "test:1:2:3:telegram:seen:message:-100:7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &redisStore{prefix: tt.prefix}

			assert.Equal(t, tt.expectedOffset, s.offsetKey(42))
			assert.Equal(t, tt.expectedSeen, s.markKey("message:-100:7"))
		})
	}
}
