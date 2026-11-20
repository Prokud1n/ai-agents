package knowledge

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testSource   = "a.md"
	voiceSource  = "brand/voice.md"
	unknownKind  = "recipe"
	testAgent    = "copywriter"
	formalDraft  = "Уважаемые клиенты"
	checksumText = "голос"
	brandTitle   = "Голос бренда"
)

func TestFormatVector(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []float32
		expected string
	}{
		{name: "empty", input: nil, expected: "[]"},
		{name: "components", input: []float32{0.5, -1, 0.1, 3e-8}, expected: "[0.5,-1,0.1,3e-08]"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, formatVector(tt.input))
		})
	}
}

func TestParseKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  Kind
		expectErr bool
	}{
		{name: "brand", input: string(BrandKind), expected: BrandKind},
		{name: "post kind", input: string(PostKind), expected: PostKind},
		{name: "unknown", input: unknownKind, expectErr: true},
		{name: "case sensitive", input: "Brand", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseKind(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrValidation)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestNormalizeIngestData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     IngestDocumentData
		expected  IngestDocumentData
		expectErr bool
	}{
		{
			name:     "defaults: title from source, kind resolved later",
			input:    IngestDocumentData{Source: " " + voiceSource + " ", Text: "текст\r\n"},
			expected: IngestDocumentData{Source: voiceSource, Title: voiceSource, Kind: "", Text: "текст"},
		},
		{
			name:     "kept values",
			input:    IngestDocumentData{Source: testSource, Title: " " + brandTitle + " ", Kind: BrandKind, Text: "x"},
			expected: IngestDocumentData{Source: testSource, Title: brandTitle, Kind: BrandKind, Text: "x"},
		},
		{name: "no source", input: IngestDocumentData{Text: "x"}, expectErr: true},
		{name: "no text", input: IngestDocumentData{Source: testSource, Text: " \n "}, expectErr: true},
		{name: "bad kind", input: IngestDocumentData{Source: testSource, Kind: unknownKind, Text: "x"}, expectErr: true},
		{
			name:      "too long",
			input:     IngestDocumentData{Source: testSource, Text: strings.Repeat("я", MaxDocumentRunes+1)},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeIngestData(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrValidation)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDocumentSameVersion(t *testing.T) {
	t.Parallel()

	stored := Document{Checksum: "c", EmbeddingModel: "m", ChunkerVersion: 1, Title: "old", Kind: OtherKind}

	tests := []struct {
		name     string
		input    documentRow
		expected bool
	}{
		{name: "same version, other title and kind", input: documentRow{Checksum: "c", EmbeddingModel: "m", ChunkerVersion: 1, Title: "new", Kind: BrandKind}, expected: true},
		{name: "text changed", input: documentRow{Checksum: "c2", EmbeddingModel: "m", ChunkerVersion: 1}, expected: false},
		{name: "model changed", input: documentRow{Checksum: "c", EmbeddingModel: "m2", ChunkerVersion: 1}, expected: false},
		{name: "chunker changed", input: documentRow{Checksum: "c", EmbeddingModel: "m", ChunkerVersion: 2}, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.input.sameVersion(&stored))
		})
	}
}

func TestDocumentCheckKind(t *testing.T) {
	t.Parallel()

	stored := Document{Kind: BrandKind}

	tests := []struct {
		name      string
		input     documentRow
		expectErr bool
	}{
		{name: "same kind", input: documentRow{Kind: BrandKind}},
		{name: "other kind", input: documentRow{Kind: ProductKind}, expectErr: true},
		{name: "other kind with force", input: documentRow{Kind: ProductKind, Force: true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.input.checkKind(&stored)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrConflict)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestNormalizeCorrectionData(t *testing.T) {
	t.Parallel()

	valid := SaveCorrectionData{Agent: testAgent, TaskKind: reelsScriptTask, Original: "a", Corrected: "b"}

	tests := []struct {
		name      string
		input     SaveCorrectionData
		expectErr bool
	}{
		{name: "valid without comment", input: valid},
		{name: "no agent", input: SaveCorrectionData{TaskKind: "x", Original: "a", Corrected: "b"}, expectErr: true},
		{name: "no task kind", input: SaveCorrectionData{Agent: "x", Original: "a", Corrected: "b"}, expectErr: true},
		{name: "blank original", input: SaveCorrectionData{Agent: "x", TaskKind: "y", Original: " ", Corrected: "b"}, expectErr: true},
		{name: "no corrected", input: SaveCorrectionData{Agent: "x", TaskKind: "y", Original: "a"}, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeCorrectionData(&tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrValidation)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.input, got)
		})
	}
}

func TestNormalizeLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    int
		expected int
	}{
		{name: "zero is default", input: 0, expected: DefaultLimit},
		{name: "negative is default", input: -3, expected: DefaultLimit},
		{name: "kept", input: 7, expected: 7},
		{name: "capped", input: 1000, expected: MaxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, normalizeLimit(tt.input))
		})
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()

	// sha256("a"), hex.
	assert.Equal(t, "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb", Checksum("a"))
	assert.NotEqual(t, Checksum(checksumText), Checksum(checksumText+"!"))
	assert.Len(t, Checksum(""), 64)
}

func TestCorrectionEmbeddingText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		comment  string
		original string
		expected string
	}{
		{name: "comment first", comment: "слишком официально", original: formalDraft, expected: "слишком официально\n\n" + formalDraft},
		{name: "no comment", original: formalDraft, expected: formalDraft},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, CorrectionEmbeddingText(tt.comment, tt.original))
		})
	}
}

func TestFormatSearchResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    []SearchResult
		expected string
	}{
		{name: "nothing found", input: nil, expected: noResultsText},
		{
			name: "fragments with sources",
			input: []SearchResult{
				{Source: voiceSource, Title: brandTitle, Kind: BrandKind, Content: "Питание > Тон\n\nБез запретов.", Score: 0.8349},
				{Source: "posts/best.md", Title: "Лучшие посты", Kind: PostKind, Content: "Пост про воду.", Score: 0.5},
			},
			expected: "[1] brand/voice.md — «Голос бренда» (brand, score 0.83)\nПитание > Тон\n\nБез запретов.\n\n" +
				"[2] posts/best.md — «Лучшие посты» (post, score 0.50)\nПост про воду.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, FormatSearchResults(tt.input))
		})
	}
}

func TestFormatCorrections(t *testing.T) {
	t.Parallel()

	got := FormatCorrections([]Correction{
		{Agent: testAgent, TaskKind: reelsScriptTask, Original: "Худеем к лету!", Corrected: "Двигаемся с удовольствием", Comment: "без диетной культуры"},
		{Agent: testAgent, TaskKind: "carousel", Original: "a", Corrected: "b", CreatedAt: time.Now()},
	})

	expected := "Пример 1 (copywriter, reels-script)\nКак не надо:\nХудеем к лету!\nКак надо:\nДвигаемся с удовольствием\n" +
		"Комментарий тренера: без диетной культуры\n\n" +
		"Пример 2 (copywriter, carousel)\nКак не надо:\na\nКак надо:\nb"

	assert.Equal(t, expected, got)
}

// Validation happens before the repository and the embedder are touched, so nil dependencies are safe.
func TestServiceValidation(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, slog.New(slog.DiscardHandler))
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
	}{
		{name: "empty search query", call: func() error {
			_, err := svc.Search(ctx, SearchChunksData{Query: "  "})
			return err
		}},
		{name: "unknown kind in search", call: func() error {
			_, err := svc.Search(ctx, SearchChunksData{Query: "питание", Kinds: []Kind{unknownKind}})
			return err
		}},
		{name: "ingest without text", call: func() error {
			_, err := svc.Ingest(ctx, IngestDocumentData{Source: testSource})
			return err
		}},
		{name: "correction without agent", call: func() error {
			_, err := svc.SaveCorrection(ctx, &SaveCorrectionData{TaskKind: "x", Original: "a", Corrected: "b"})
			return err
		}},
		{name: "corrections without agent", call: func() error {
			_, err := svc.FindSimilarCorrections(ctx, FindSimilarCorrectionsData{Query: "сладкое", TaskKind: "post"})
			return err
		}},
		{name: "delete without source", call: func() error {
			return svc.Delete(ctx, " ")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.ErrorIs(t, tt.call(), ErrValidation)
		})
	}
}
