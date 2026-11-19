// Package extract turns brand materials (.md, .txt, .docx, .pdf) into plain text.
// Headings of .docx documents become markdown headings, so the chunker can split by them.
package extract

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupportedFormat = errors.New("unsupported file format")
	ErrInvalidDocument   = errors.New("invalid document")
	ErrTooLarge          = errors.New("file is too large")
)

// MaxFileBytes is the largest file accepted for extraction: brand documents are far smaller,
// and every format here is read into memory whole.
const MaxFileBytes = 32 << 20

const (
	markdownExt  = ".md"
	markdownLong = ".markdown"
	textExt      = ".txt"
	docxExt      = ".docx"
	pdfExt       = ".pdf"
)

// Supported reports whether files with the name's extension can be extracted.
func Supported(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case markdownExt, markdownLong, textExt, docxExt, pdfExt:
		return true
	default:
		return false
	}
}

// Text extracts plain text from a file; the format is chosen by the file name's extension.
// PDF parsing is limited by PDFTimeout (and ctx).
func Text(ctx context.Context, name string, data []byte) (string, error) {
	if len(data) > MaxFileBytes {
		return "", fmt.Errorf("%d bytes, at most %d: %w", len(data), MaxFileBytes, ErrTooLarge)
	}

	switch strings.ToLower(filepath.Ext(name)) {
	case markdownExt, markdownLong, textExt:
		return plainText(data)
	case docxExt:
		return docxText(data)
	case pdfExt:
		return pdfText(ctx, data)
	default:
		return "", fmt.Errorf("%q: %w", filepath.Ext(name), ErrUnsupportedFormat)
	}
}

func plainText(data []byte) (string, error) {
	if !utf8.Valid(data) {
		return "", fmt.Errorf("text is not valid UTF-8 (convert it from Windows-1251 first): %w", ErrInvalidDocument)
	}

	return strings.TrimPrefix(string(data), "\ufeff"), nil
}
