package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
)

var (
	errNoFile         = errors.New("no file to upload")
	errNotRegularFile = errors.New("upload is not a regular file")
)

type formField struct {
	name  string
	value string
}

func fileFields(params *SendFileParams) ([]formField, error) {
	fields := []formField{{name: "chat_id", value: strconv.FormatInt(params.ChatID, 10)}}

	if params.MessageThreadID != 0 {
		fields = append(fields, formField{name: "message_thread_id", value: strconv.FormatInt(params.MessageThreadID, 10)})
	}

	if params.Caption != "" {
		fields = append(fields, formField{name: "caption", value: params.Caption})
	}

	if params.ParseMode != "" {
		fields = append(fields, formField{name: "parse_mode", value: params.ParseMode})
	}

	if params.Width > 0 && params.Height > 0 {
		fields = append(fields,
			formField{name: "width", value: strconv.Itoa(params.Width)},
			formField{name: "height", value: strconv.Itoa(params.Height)})
	}

	if params.SupportsStreaming {
		fields = append(fields, formField{name: "supports_streaming", value: "true"})
	}

	if params.ReplyParameters != nil {
		data, err := json.Marshal(params.ReplyParameters)
		if err != nil {
			return nil, fmt.Errorf("failed to encode reply parameters: %w", err)
		}

		fields = append(fields, formField{name: "reply_parameters", value: string(data)})
	}

	if params.ReplyMarkup != nil {
		data, err := json.Marshal(params.ReplyMarkup)
		if err != nil {
			return nil, fmt.Errorf("failed to encode reply markup: %w", err)
		}

		fields = append(fields, formField{name: "reply_markup", value: string(data)})
	}

	return fields, nil
}

// newMultipartBody streams the file from disk instead of buffering up to 2 GB in memory. The form parts
// around the file are prepared in advance, so the body has a known Content-Length (no chunked encoding).
// The file is read with ReadAt and not closed: it belongs to the caller.
func newMultipartBody(fields []formField, fileField string, file *os.File, fileName string) (*requestBody, error) {
	if file == nil {
		return nil, errNoFile
	}

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat file: %w", err)
	}

	if !info.Mode().IsRegular() {
		return nil, errNotRegularFile
	}

	var head bytes.Buffer

	writer := multipart.NewWriter(&head)

	for _, f := range fields {
		if err := writer.WriteField(f.name, f.value); err != nil {
			return nil, fmt.Errorf("failed to write form field: %w", err)
		}
	}

	if _, err := writer.CreateFormFile(fileField, filepath.Base(fileName)); err != nil {
		return nil, fmt.Errorf("failed to write form file header: %w", err)
	}

	prefix := bytes.Clone(head.Bytes())
	head.Reset()

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("failed to write form end: %w", err)
	}

	suffix := bytes.Clone(head.Bytes())

	return &requestBody{
		reader: io.NopCloser(io.MultiReader(
			bytes.NewReader(prefix),
			io.NewSectionReader(file, 0, info.Size()),
			bytes.NewReader(suffix),
		)),
		contentType: writer.FormDataContentType(),
		length:      int64(len(prefix)) + info.Size() + int64(len(suffix)),
	}, nil
}
