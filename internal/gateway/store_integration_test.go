package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/internal/redistest"
)

const (
	testBotID      = int64(1001)
	otherTestBotID = int64(1002)
	testEventKey   = "message:-100:42"
)

// newTestStore returns a store on the test Redis whose keys live under a prefix unique to the test.
func newTestStore(t *testing.T) *redisStore {
	t.Helper()

	client := redistest.Connect(t)

	return newPrefixedStore(client, redistest.Prefix(t, client))
}

func TestIntegrationRedisStoreOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		saves []int64 // offsets saved one after another for testBotID
		other []int64 // offsets saved for otherTestBotID
		// expected offset of testBotID after the saves
		expected int64
	}{
		{name: "nothing stored", expected: 0},
		{name: "saved", saves: []int64{42}, expected: 42},
		{name: "overwritten", saves: []int64{42, 43, 100}, expected: 100},
		{name: "saved zero", saves: []int64{42, 0}, expected: 0},
		{name: "large", saves: []int64{1 << 40}, expected: 1 << 40},
		{name: "other bot does not affect", saves: []int64{7}, other: []int64{9, 10}, expected: 7},
		{name: "only other bot stored", other: []int64{9}, expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestStore(t)
			ctx := context.Background()

			for _, offset := range tt.saves {
				require.NoError(t, s.SaveOffset(ctx, testBotID, offset))
			}

			for _, offset := range tt.other {
				require.NoError(t, s.SaveOffset(ctx, otherTestBotID, offset))
			}

			offset, err := s.LoadOffset(ctx, testBotID)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, offset)

			if len(tt.other) > 0 {
				other, err := s.LoadOffset(ctx, otherTestBotID)
				require.NoError(t, err)
				assert.Equal(t, tt.other[len(tt.other)-1], other, "the other bot keeps its own offset")
			}
		})
	}
}

func TestIntegrationRedisStoreCorruptOffset(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
	}{
		{name: "not a number", value: "abc"},
		{name: "float", value: "1.5"},
		{name: "empty", value: ""},
		{name: "overflow", value: "99999999999999999999"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestStore(t)
			ctx := context.Background()

			require.NoError(t, s.rdb.Set(ctx, s.offsetKey(testBotID), tt.value, 0).Err())

			offset, err := s.LoadOffset(ctx, testBotID)
			require.ErrorContains(t, err, "failed to parse telegram offset")
			assert.Zero(t, offset)
		})
	}
}

func TestIntegrationRedisStoreMarkSeen(t *testing.T) {
	t.Parallel()

	type step int

	const (
		mark step = iota
		unmark
	)

	tests := []struct {
		name  string
		steps []step
		// expected results of the mark steps, in order
		expected []bool
	}{
		{name: "first mark is new", steps: []step{mark}, expected: []bool{true}},
		{name: "repeated mark is not new", steps: []step{mark, mark, mark}, expected: []bool{true, false, false}},
		{name: "mark after unmark is new again", steps: []step{mark, unmark, mark, mark}, expected: []bool{true, true, false}},
		{name: "unmark of a missing key", steps: []step{unmark, mark}, expected: []bool{true}},
		{name: "repeated unmark", steps: []step{mark, unmark, unmark, mark}, expected: []bool{true, true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestStore(t)
			ctx := context.Background()
			results := make([]bool, 0, len(tt.steps))

			for _, st := range tt.steps {
				if st == unmark {
					require.NoError(t, s.Unmark(ctx, testEventKey))

					continue
				}

				isNew, err := s.MarkSeen(ctx, testEventKey)
				require.NoError(t, err)

				results = append(results, isNew)
			}

			assert.Equal(t, tt.expected, results)
		})
	}
}

func TestIntegrationRedisStoreMarkSeenKeysAreIndependent(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	ctx := context.Background()
	keys := []string{
		seenKey(&Event{ChatID: -100, MessageID: 42}),
		seenKey(&Event{ChatID: -100, MessageID: 43}),
		seenKey(&Event{ChatID: -200, MessageID: 42}),
		seenKey(&Event{Callback: &Callback{ID: "cb-1"}}),
	}

	for _, key := range keys {
		isNew, err := s.MarkSeen(ctx, key)
		require.NoError(t, err)
		assert.True(t, isNew, key)
	}

	require.NoError(t, s.Unmark(ctx, keys[0]))

	for i, key := range keys {
		isNew, err := s.MarkSeen(ctx, key)
		require.NoError(t, err)
		assert.Equal(t, i == 0, isNew, "only the unmarked key is new again: %s", key)
	}
}

func TestIntegrationRedisStoreTTL(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.SaveOffset(ctx, testBotID, 42))

	isNew, err := s.MarkSeen(ctx, testEventKey)
	require.NoError(t, err)
	require.True(t, isNew)

	seenTTLLeft, err := s.rdb.TTL(ctx, s.markKey(testEventKey)).Result()
	require.NoError(t, err)
	assert.Positive(t, seenTTLLeft, "the mark expires")
	assert.LessOrEqual(t, seenTTLLeft, seenTTL)

	// A repeated mark does not prolong the mark.
	isNew, err = s.MarkSeen(ctx, testEventKey)
	require.NoError(t, err)
	require.False(t, isNew)

	repeatedTTLLeft, err := s.rdb.TTL(ctx, s.markKey(testEventKey)).Result()
	require.NoError(t, err)
	assert.LessOrEqual(t, repeatedTTLLeft, seenTTLLeft)

	// TTL reports -1 (as a duration of -1ns in go-redis) for a key without expiry.
	offsetTTL, err := s.rdb.TTL(ctx, s.offsetKey(testBotID)).Result()
	require.NoError(t, err)
	assert.Negative(t, offsetTTL, "the offset does not expire")

	exists, err := s.rdb.Exists(ctx, s.offsetKey(testBotID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), exists)
}

func TestIntegrationRedisStoreKeysUnderPrefix(t *testing.T) {
	t.Parallel()

	s := newTestStore(t)
	ctx := context.Background()

	require.NotEmpty(t, s.prefix)
	require.NoError(t, s.SaveOffset(ctx, testBotID, 42))

	_, err := s.MarkSeen(ctx, testEventKey)
	require.NoError(t, err)

	keys, err := s.rdb.Keys(ctx, s.prefix+"*").Result()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		s.prefix + offsetKeyPrefix + "1001",
		s.prefix + seenKeyPrefix + testEventKey,
	}, keys)

	// Nothing is written under the production keys.
	exists, err := s.rdb.Exists(ctx, offsetKeyPrefix+"1001", seenKeyPrefix+testEventKey).Result()
	require.NoError(t, err)
	assert.Zero(t, exists)

	// Another test's store with its own prefix sees nothing of this one.
	other := newTestStore(t)

	offset, err := other.LoadOffset(ctx, testBotID)
	require.NoError(t, err)
	assert.Zero(t, offset)

	isNew, err := other.MarkSeen(ctx, testEventKey)
	require.NoError(t, err)
	assert.True(t, isNew)

	for _, key := range keys {
		assert.True(t, strings.HasPrefix(key, s.prefix), key)
	}
}
