package telegram

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseSize = 32 << 20 // a getUpdates batch of 100 messages is far below this
	maxBackoffShift = 20       // caps 2^attempt so the delay never overflows
	maskedToken     = "<token>"
	jsonContentType = "application/json"
)

// Options of one bot's client. Timeouts are per attempt.
type Options struct {
	BaseURL string
	Token   string
	// RequestTimeout limits ordinary calls; getUpdates gets its long-polling timeout on top.
	RequestTimeout time.Duration
	// FileTimeout limits getFile (the local server downloads the whole file before answering) and uploads.
	FileTimeout    time.Duration
	MaxRetries     int
	RetryBaseDelay time.Duration
	RetryMaxDelay  time.Duration
}

// Client is a thin client of the Bot API for one bot. Every call takes a context; 429 (with retry_after),
// 5xx and network errors are retried with exponential backoff. The token is part of every URL, so it is
// masked in all errors the client returns and never logged.
type Client struct {
	httpClient     *http.Client
	baseURL        string
	token          string
	requestTimeout time.Duration
	fileTimeout    time.Duration
	maxRetries     int
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
	log            *slog.Logger
	// sleep waits between retries; tests replace it.
	sleep func(ctx context.Context, d time.Duration) error
}

func NewClient(opts *Options, log *slog.Logger) *Client {
	return &Client{
		httpClient:     newHTTPClient(),
		baseURL:        strings.TrimRight(opts.BaseURL, "/"),
		token:          opts.Token,
		requestTimeout: opts.RequestTimeout,
		fileTimeout:    opts.FileTimeout,
		maxRetries:     max(opts.MaxRetries, 0),
		retryBaseDelay: opts.RetryBaseDelay,
		retryMaxDelay:  opts.RetryMaxDelay,
		log:            log,
		sleep:          sleep,
	}
}

// newHTTPClient: the URL holds the token, so it is sent only to the configured Bot API address. No proxy from
// the environment (HTTP(S)_PROXY) and no redirects, which could carry the token to another host. No client-wide
// timeout: long polling and 2 GB uploads need their own, set per request.
func newHTTPClient() *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}

	transport = transport.Clone()
	transport.Proxy = nil

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// BotIDFromToken returns the bot ID, the part of the token before the colon. It is public, unlike the token.
func BotIDFromToken(token string) (int64, error) {
	idPart, secret, found := strings.Cut(token, ":")
	if !found || secret == "" {
		return 0, ErrInvalidToken
	}

	id, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil || id <= 0 {
		return 0, ErrInvalidToken
	}

	return id, nil
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var user User
	if err := c.callJSON(ctx, "getMe", struct{}{}, c.requestTimeout, true, &user); err != nil {
		return nil, err
	}

	return &user, nil
}

// GetUpdates long-polls for updates; the attempt timeout is params.Timeout plus RequestTimeout.
func (c *Client) GetUpdates(ctx context.Context, params *GetUpdatesParams) ([]Update, error) {
	timeout := time.Duration(params.Timeout)*time.Second + c.requestTimeout

	var updates []Update
	if err := c.callJSON(ctx, "getUpdates", params, timeout, true, &updates); err != nil {
		return nil, err
	}

	return updates, nil
}

// GetFile: with the local Bot API server the file is downloaded to its disk before the answer, so the
// call may take minutes for a large video and is limited by FileTimeout.
func (c *Client) GetFile(ctx context.Context, fileID string) (*File, error) {
	var file File
	if err := c.callJSON(ctx, "getFile", &getFileParams{FileID: fileID}, c.fileTimeout, true, &file); err != nil {
		return nil, err
	}

	return &file, nil
}

// SendMessage is retried after 429, 5xx and connection errors but not after a timeout. A 5xx or a dropped
// connection may still follow a delivered message, so a rare duplicate is possible; that is accepted.
func (c *Client) SendMessage(ctx context.Context, params *SendMessageParams) (*Message, error) {
	var msg Message
	if err := c.callJSON(ctx, "sendMessage", params, c.requestTimeout, false, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

func (c *Client) EditMessageText(ctx context.Context, params *EditMessageTextParams) (*Message, error) {
	var msg Message
	if err := c.callJSON(ctx, "editMessageText", params, c.requestTimeout, true, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

func (c *Client) EditMessageReplyMarkup(ctx context.Context, params *EditMessageReplyMarkupParams) (*Message, error) {
	var msg Message
	if err := c.callJSON(ctx, "editMessageReplyMarkup", params, c.requestTimeout, true, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, params *AnswerCallbackQueryParams) error {
	return c.callJSON(ctx, "answerCallbackQuery", params, c.requestTimeout, true, nil)
}

// SetMessageReaction replaces the bot's reactions on a message.
func (c *Client) SetMessageReaction(ctx context.Context, params *SetMessageReactionParams) error {
	return c.callJSON(ctx, "setMessageReaction", params, c.requestTimeout, true, nil)
}

// SetMyCommands replaces the bot's command menu in the scope.
func (c *Client) SetMyCommands(ctx context.Context, params *SetMyCommandsParams) error {
	return c.callJSON(ctx, "setMyCommands", params, c.requestTimeout, true, nil)
}

// DeleteMyCommands removes the bot's command menu in the scope: the client falls back to a wider scope.
func (c *Client) DeleteMyCommands(ctx context.Context, params *DeleteMyCommandsParams) error {
	return c.callJSON(ctx, "deleteMyCommands", params, c.requestTimeout, true, nil)
}

// PinChatMessage needs an administrator with the right to pin messages.
func (c *Client) PinChatMessage(ctx context.Context, params *PinChatMessageParams) error {
	return c.callJSON(ctx, "pinChatMessage", params, c.requestTimeout, true, nil)
}

func (c *Client) UnpinChatMessage(ctx context.Context, params *UnpinChatMessageParams) error {
	return c.callJSON(ctx, "unpinChatMessage", params, c.requestTimeout, true, nil)
}

func (c *Client) SendVideo(ctx context.Context, params *SendFileParams) (*Message, error) {
	return c.sendFile(ctx, "sendVideo", "video", params)
}

func (c *Client) SendPhoto(ctx context.Context, params *SendFileParams) (*Message, error) {
	return c.sendFile(ctx, "sendPhoto", "photo", params)
}

func (c *Client) SendDocument(ctx context.Context, params *SendFileParams) (*Message, error) {
	return c.sendFile(ctx, "sendDocument", "document", params)
}

func (c *Client) sendFile(ctx context.Context, method, field string, params *SendFileParams) (*Message, error) {
	fields, err := fileFields(params)
	if err != nil {
		return nil, fmt.Errorf("failed to call %s: %w", method, err)
	}

	req := &request{
		method:     method,
		timeout:    c.fileTimeout,
		idempotent: false,
		body: func() (*requestBody, error) {
			return newMultipartBody(fields, field, params.File, params.FileName)
		},
	}

	var msg Message
	if err := c.do(ctx, req, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

func (c *Client) callJSON(
	ctx context.Context,
	method string,
	params any,
	timeout time.Duration,
	idempotent bool,
	result any,
) error {
	data, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("failed to encode %s params: %w", method, err)
	}

	req := &request{
		method:     method,
		timeout:    timeout,
		idempotent: idempotent,
		body: func() (*requestBody, error) {
			return &requestBody{
				reader:      io.NopCloser(bytes.NewReader(data)),
				contentType: jsonContentType,
				length:      int64(len(data)),
			}, nil
		},
	}

	return c.do(ctx, req, result)
}

type request struct {
	method  string
	timeout time.Duration
	// idempotent requests are retried after a timeout too: the server may have processed the first attempt.
	idempotent bool
	// body builds a fresh body for every attempt (an upload reopens the file).
	body func() (*requestBody, error)
}

type requestBody struct {
	reader      io.ReadCloser
	contentType string
	length      int64
}

func (c *Client) do(ctx context.Context, req *request, result any) error {
	for attempt := 0; ; attempt++ {
		retryAfter, err := c.attempt(ctx, req, result)
		if err == nil {
			return nil
		}

		if attempt >= c.maxRetries || !retryable(ctx, err, req.idempotent) {
			return fmt.Errorf("failed to call %s: %w", req.method, err)
		}

		delay := max(equalJitter(Backoff(c.retryBaseDelay, c.retryMaxDelay, attempt)), retryAfter)
		c.log.WarnContext(ctx, "telegram: retrying request",
			"method", req.method, "error", err, "attempt", attempt+1, "delay", delay.String())

		if err := c.sleep(ctx, delay); err != nil {
			return fmt.Errorf("failed to call %s: %w", req.method, err)
		}
	}
}

// attempt makes one request and returns the server's retry_after (zero if absent) for retries.
func (c *Client) attempt(ctx context.Context, req *request, result any) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, req.timeout)
	defer cancel()

	body, err := req.body()
	if err != nil {
		return 0, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/bot"+c.token+"/"+req.method, body.reader)
	if err != nil {
		_ = body.reader.Close()

		return 0, fmt.Errorf("failed to create http request: %w", c.maskURLError(err))
	}

	httpReq.ContentLength = body.length
	httpReq.Header.Set("Content-Type", body.contentType)

	httpResp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return 0, &transportError{err: c.maskURLError(err)}
	}
	defer httpResp.Body.Close()

	return decodeResponse(httpResp, result)
}

type responseEnvelope struct {
	OK          bool                `json:"ok"`
	Result      json.RawMessage     `json:"result"`
	ErrorCode   int                 `json:"error_code"`
	Description string              `json:"description"`
	Parameters  *responseParameters `json:"parameters"`
}

func decodeResponse(httpResp *http.Response, result any) (time.Duration, error) {
	data, err := io.ReadAll(io.LimitReader(httpResp.Body, maxResponseSize))
	if err != nil {
		return 0, &transportError{err: fmt.Errorf("failed to read response: %w", err)}
	}

	var envelope responseEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		if httpResp.StatusCode != http.StatusOK {
			// Not the Bot API itself: e.g. an HTML 502 of a proxy.
			return 0, &APIError{Code: httpResp.StatusCode, Description: http.StatusText(httpResp.StatusCode)}
		}

		return 0, fmt.Errorf("failed to decode response: %w", err)
	}

	if !envelope.OK {
		apiErr := &APIError{Code: envelope.ErrorCode, Description: envelope.Description}
		if apiErr.Code == 0 {
			apiErr.Code = httpResp.StatusCode
		}

		if envelope.Parameters != nil && envelope.Parameters.RetryAfter > 0 {
			apiErr.RetryAfter = time.Duration(envelope.Parameters.RetryAfter) * time.Second
		}

		return apiErr.RetryAfter, apiErr
	}

	if result == nil {
		return 0, nil
	}

	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return 0, fmt.Errorf("failed to decode result: %w", err)
	}

	return 0, nil
}

// maskURLError replaces the token in the URL of a *url.Error (http.Client and url.Parse errors print
// the whole URL). Other errors do not contain the URL.
func (c *Client) maskURLError(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		return err
	}

	return &url.Error{Op: urlErr.Op, URL: c.MaskToken(urlErr.URL), Err: urlErr.Err}
}

// MaskToken replaces the bot token in s, e.g. in a local file path, which contains it as a directory name.
func (c *Client) MaskToken(s string) string {
	if c.token == "" {
		return s
	}

	return strings.ReplaceAll(s, c.token, maskedToken)
}

// retryable: API errors with 429/5xx and network errors, but never a cancelled or expired context.
// A timeout is retried only for idempotent requests: a message may have been sent after the client gave up.
func retryable(ctx context.Context, err error, idempotent bool) bool {
	if ctx.Err() != nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable()
	}

	var transportErr *transportError
	if !errors.As(err, &transportErr) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return idempotent
	}

	return true
}

// Backoff returns base * 2^attempt capped by maxDelay.
func Backoff(base, maxDelay time.Duration, attempt int) time.Duration {
	if shift := min(attempt, maxBackoffShift); base <= maxDelay>>shift {
		return base << shift
	}

	return maxDelay
}

// equalJitter returns a random delay in [d/2, d]: the four bots do not retry in lockstep.
func equalJitter(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}

	n, err := rand.Int(rand.Reader, big.NewInt(int64(half)+1))
	if err != nil {
		return d
	}

	return half + time.Duration(n.Int64())
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("failed to wait before retry: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
