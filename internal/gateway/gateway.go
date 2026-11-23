package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/Prokud1n/ai-agents/internal/config"
	"github.com/Prokud1n/ai-agents/internal/providers/telegram"
)

const (
	// cleanupTimeout limits the calls made after a failed handler: unmark the event, answer the callback.
	cleanupTimeout = 5 * time.Second
	// maxPanicLength cuts the panic text in the log.
	maxPanicLength = 500
	// maxPendingEvents limits accepted events waiting for a worker slot. Polling never waits for a slot, only
	// for this queue: with one trainer it is never full, and it bounds memory if something floods the group.
	maxPendingEvents = 256
	// outboxDirMode: only the app user reads and writes outgoing files.
	outboxDirMode = 0o700
)

var errBotBroken = errors.New("telegram rejected the bot token")

// gateway polls the four bots and hands accepted events to the Handler.
//
// Reception scheme: the dispatcher (marketer, privacy mode off) gets every group message; the other bots
// ask getUpdates only for callback_query, which Telegram sends to the bot whose message has the button.
// So each message is received by one bot only. On top of that every event is marked in Redis before it
// is handled (and unmarked if the Handler fails), and each bot's offset is saved after every update: a restart
// neither repeats nor loses confirmed updates.
//
// Each bot connects on its own: a bot whose token Telegram rejects (401) is stopped and reported in /readyz,
// the others keep working.
type gateway struct {
	bots           *bots
	store          stateStore
	handler        Handler
	status         *Status
	routing        *routing
	filesDir       string
	outboxDir      string
	maxFileSize    int64
	pollTimeout    time.Duration
	retryBaseDelay time.Duration
	retryMaxDelay  time.Duration
	log            *slog.Logger
	sleep          func(ctx context.Context, d time.Duration) error

	// Semaphores of concurrent handlers: callbacks and events with a file download have their own, so a button
	// or a text message never waits for a video download.
	messageWorkers  chan struct{}
	callbackWorkers chan struct{}
	mediaWorkers    chan struct{}
	pending         chan struct{}
	pollers         sync.WaitGroup
	handlers        sync.WaitGroup
	stopPolling     context.CancelFunc
	stopHandlers    context.CancelFunc
}

func newGateway(
	lc fx.Lifecycle,
	cfg *config.Telegram,
	b *bots,
	store stateStore,
	handler Handler,
	status *Status,
	log *slog.Logger,
) *gateway {
	workers := max(cfg.Workers, 1)

	g := &gateway{
		bots:    b,
		store:   store,
		handler: handler,
		status:  status,
		routing: &routing{
			GroupID:        cfg.GroupID,
			AllowedUserIDs: cfg.AllowedUserIDs,
			Topics:         topicsOf(cfg),
			MaxFileSize:    cfg.MaxFileSize,
		},
		filesDir:        cfg.FilesDir,
		outboxDir:       cfg.OutboxDir,
		maxFileSize:     cfg.MaxFileSize,
		pollTimeout:     cfg.PollTimeout,
		retryBaseDelay:  cfg.RetryBaseDelay,
		retryMaxDelay:   cfg.RetryMaxDelay,
		log:             log,
		sleep:           sleepContext,
		messageWorkers:  make(chan struct{}, workers),
		callbackWorkers: make(chan struct{}, workers),
		mediaWorkers:    make(chan struct{}, max(cfg.MediaWorkers, 1)),
		pending:         make(chan struct{}, maxPendingEvents),
	}

	lc.Append(fx.Hook{OnStart: g.start, OnStop: g.stop})

	return g
}

// start does not wait for the Bot API: bots connect in the background, so a Bot API server that starts
// later than the app does not fail the start.
func (g *gateway) start(ctx context.Context) error {
	if !g.bots.enabled() {
		g.log.InfoContext(ctx, "telegram gateway: disabled, TELEGRAM_*_TOKEN are not set")

		return nil
	}

	if err := prepareOutbox(g.outboxDir); err != nil {
		return err
	}

	base := context.WithoutCancel(ctx)
	pollCtx, stopPolling := context.WithCancel(base)
	handlerCtx, stopHandlers := context.WithCancel(base)
	g.stopPolling, g.stopHandlers = stopPolling, stopHandlers

	rt := newRouter(g.routing, nil)

	// The dispatcher starts polling once the other bots are connected (or rejected), so @mentions and replies
	// are not routed by the topic for lack of their usernames.
	var others sync.WaitGroup

	othersReady := make(chan struct{})

	for _, b := range g.bots.list() {
		if b.role != dispatcherRole {
			others.Add(1)
		}
	}

	g.pollers.Add(1)

	go func() {
		defer g.pollers.Done()

		others.Wait()
		close(othersReady)
	}()

	for _, b := range g.bots.list() {
		g.pollers.Add(1)

		go func() {
			defer g.pollers.Done()

			if b.role == dispatcherRole {
				g.runBot(pollCtx, handlerCtx, rt, b, othersReady, nil)

				return
			}

			g.runBot(pollCtx, handlerCtx, rt, b, nil, others.Done)
		}()
	}

	g.log.InfoContext(ctx, "telegram gateway: started", "bots", len(g.bots.list()))

	return nil
}

// stop ends polling at once and lets running handlers finish until the stop timeout.
func (g *gateway) stop(ctx context.Context) error {
	if g.stopPolling == nil {
		return nil
	}

	g.stopPolling()
	defer g.stopHandlers()

	if err := waitGroup(ctx, &g.pollers); err != nil {
		return fmt.Errorf("failed to stop telegram polling: %w", err)
	}

	if err := waitGroup(ctx, &g.handlers); err != nil {
		return fmt.Errorf("failed to wait for telegram handlers: %w", err)
	}

	g.log.InfoContext(ctx, "telegram gateway: stopped")

	return nil
}

// runBot connects the bot, calls connected (if any) once it is connected or given up, waits for waitFor
// (if any) and polls.
func (g *gateway) runBot(
	ctx, handlerCtx context.Context,
	rt *router,
	b *bot,
	waitFor <-chan struct{},
	connected func(),
) {
	ident, err := g.resolveIdentity(ctx, b)
	if err == nil {
		rt.addIdentity(b.role, ident)
	}

	if connected != nil {
		connected()
	}

	if err != nil {
		return // stopped, or the token is invalid (already logged)
	}

	if waitFor != nil {
		select {
		case <-waitFor:
		case <-ctx.Done():
			return
		}
	}

	g.poll(ctx, handlerCtx, rt, b)
}

// resolveIdentity calls getMe until it succeeds (the username is needed for @mentions). An invalid token
// stops the bot: retrying would only repeat the error.
func (g *gateway) resolveIdentity(ctx context.Context, b *bot) (identity, error) {
	for attempt := 0; ; attempt++ {
		user, err := b.client.GetMe(ctx)
		if err == nil {
			g.log.InfoContext(ctx, "telegram gateway: bot connected", "bot", b.role, "username", user.Username)

			return identity{ID: user.ID, Username: user.Username}, nil
		}

		if ctx.Err() != nil {
			return identity{}, fmt.Errorf("failed to get bot identity: %w", ctx.Err())
		}

		if g.stopIfUnauthorized(ctx, b, err) {
			return identity{}, errBotBroken
		}

		g.log.ErrorContext(ctx, "telegram gateway: failed to get bot identity, retrying", "bot", b.role, "error", err)

		if err := g.sleep(ctx, telegram.Backoff(g.retryBaseDelay, g.retryMaxDelay, attempt)); err != nil {
			return identity{}, err
		}
	}
}

// stopIfUnauthorized marks the bot broken on 401 and logs it once.
func (g *gateway) stopIfUnauthorized(ctx context.Context, b *bot, err error) bool {
	var apiErr *telegram.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != http.StatusUnauthorized {
		return false
	}

	g.status.markBroken(b.role)
	g.log.ErrorContext(ctx, "telegram gateway: bot token rejected, the bot is stopped until restart",
		"bot", b.role, "variable", tokenVariable(b.role),
		"hint", "check the token and that the bot was logged out of the cloud Bot API")

	return true
}

func (g *gateway) poll(ctx, handlerCtx context.Context, rt *router, b *bot) {
	offset, err := g.store.LoadOffset(ctx, b.id)
	if err != nil {
		g.log.ErrorContext(ctx, "telegram gateway: failed to load offset", "bot", b.role, "error", err)
	}

	params := &telegram.GetUpdatesParams{
		Timeout:        int(g.pollTimeout / time.Second),
		AllowedUpdates: allowedUpdates(b.role),
	}

	for failures := 0; ctx.Err() == nil; {
		params.Offset = offset

		updates, err := b.client.GetUpdates(ctx, params)
		if err != nil {
			if ctx.Err() != nil || g.stopIfUnauthorized(ctx, b, err) {
				return
			}

			g.log.ErrorContext(ctx, "telegram gateway: failed to get updates", "bot", b.role, "error", err)
			_ = g.sleep(ctx, telegram.Backoff(g.retryBaseDelay, g.retryMaxDelay, failures))
			failures++

			continue
		}

		failures = 0

		for i := range updates {
			if ctx.Err() != nil {
				return
			}

			update := updates[i]
			g.dispatch(ctx, handlerCtx, rt, b, &update)

			offset = update.UpdateID + 1
			if err := g.store.SaveOffset(ctx, b.id, offset); err != nil {
				g.log.ErrorContext(ctx, "telegram gateway: failed to save offset", "bot", b.role, "error", err)
			}
		}
	}
}

// allowedUpdates: only the dispatcher takes messages, so a message is never handled by two bots.
func allowedUpdates(role Role) []string {
	if role == dispatcherRole {
		return []string{telegram.MessageUpdate, telegram.CallbackQueryUpdate}
	}

	return []string{telegram.CallbackQueryUpdate}
}

// dispatch marks the event and hands it to a goroutine that waits for a worker slot: polling never waits for
// slow handlers (a callback is not stuck behind video downloads). On stop waiting events are still handled until
// the stop timeout; after it their marks are removed, but their offsets are saved, so they are lost.
func (g *gateway) dispatch(ctx, handlerCtx context.Context, rt *router, b *bot, update *telegram.Update) {
	event, reason := rt.event(b.role, update)
	if event == nil {
		g.log.DebugContext(ctx, "telegram gateway: ignored update",
			"bot", b.role, "update_id", update.UpdateID, "reason", reason)

		if update.CallbackQuery != nil {
			// Stop the button's spinner for a stranger too; the answer has no text.
			g.answerCallback(ctx, b, update.CallbackQuery.ID)
		}

		return
	}

	workers := g.messageWorkers

	switch {
	case event.Type == CallbackEventType:
		workers = g.callbackWorkers
	case g.downloads(event):
		workers = g.mediaWorkers
	}

	key := seenKey(event)

	isNew, err := g.store.MarkSeen(ctx, key)
	if err != nil {
		// Redis is down: a possible duplicate is better than a lost message.
		g.log.ErrorContext(ctx, "telegram gateway: failed to mark event, handling anyway", "event", event, "error", err)
	} else if !isNew {
		g.log.DebugContext(ctx, "telegram gateway: duplicate event skipped", "event", event)

		return
	}

	select {
	case g.pending <- struct{}{}:
	case <-ctx.Done():
		g.unmark(ctx, event, key)

		return
	}

	g.handlers.Add(1)

	go func() {
		defer g.handlers.Done()
		defer func() { <-g.pending }()

		// Wait on handlerCtx: on stop the queue is still handled until the stop timeout (the offsets are saved).
		if !acquire(handlerCtx, workers) {
			g.unmark(handlerCtx, event, key)

			return
		}

		defer func() { <-workers }()

		g.handle(handlerCtx, b, event, key)
	}()
}

// handle downloads the media and calls the Handler. A panic does not stop the process; after an error or
// a panic the event is unmarked and a callback is answered without text.
func (g *gateway) handle(ctx context.Context, b *bot, event *Event, key string) {
	handled := false

	defer func() {
		if r := recover(); r != nil {
			g.log.ErrorContext(ctx, "telegram gateway: handler panicked",
				"event", event, "panic", g.sanitize(fmt.Sprint(r)), "stack", string(debug.Stack()))
		}

		if !handled {
			g.cleanupFailed(ctx, b, event, key)
		}
	}()

	if g.downloads(event) {
		g.download(ctx, b, event.Media)
	}

	if err := g.handler.HandleEvent(ctx, event); err != nil {
		g.log.ErrorContext(ctx, "telegram gateway: failed to handle event", "event", event, "error", err)

		return
	}

	handled = true
}

// downloads reports whether the event's file is downloaded before the handler: such an event takes a media slot.
func (g *gateway) downloads(event *Event) bool {
	return event.Media != nil && !event.Media.TooLarge && wantsMedia(g.handler, event)
}

func (g *gateway) cleanupFailed(ctx context.Context, b *bot, event *Event, key string) {
	g.unmark(ctx, event, key)

	if event.Callback != nil {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()

		g.answerCallback(cleanupCtx, b, event.Callback.ID)
	}
}

// acquire takes a slot, preferring a free slot to a cancelled context; false if ctx ended first.
func acquire(ctx context.Context, slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
	}

	select {
	case slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// unmark removes the event's mark even when ctx is already cancelled.
func (g *gateway) unmark(ctx context.Context, event *Event, key string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()

	if err := g.store.Unmark(cleanupCtx, key); err != nil {
		g.log.ErrorContext(cleanupCtx, "telegram gateway: failed to unmark event", "event", event, "error", err)
	}
}

// prepareOutbox creates the outbox directory (0700) and checks that it is a directory, not a symlink.
func prepareOutbox(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, outboxDirMode); err != nil {
			return fmt.Errorf("failed to create TELEGRAM_OUTBOX_DIR: %w", err)
		}

		info, err = os.Lstat(dir)
	}

	if err != nil {
		return fmt.Errorf("failed to stat TELEGRAM_OUTBOX_DIR: %w", err)
	}

	if !info.IsDir() {
		return fmt.Errorf("TELEGRAM_OUTBOX_DIR must be a directory, not a symlink or a file: %w", ErrInvalidConfig)
	}

	if err := os.Chmod(dir, outboxDirMode); err != nil {
		return fmt.Errorf("failed to set TELEGRAM_OUTBOX_DIR permissions: %w", err)
	}

	return nil
}

// answerCallback answers without text; an error is only logged (the callback may already be answered).
func (g *gateway) answerCallback(ctx context.Context, b *bot, callbackID string) {
	err := b.client.AnswerCallbackQuery(ctx, &telegram.AnswerCallbackQueryParams{CallbackQueryID: callbackID})
	if err != nil {
		g.log.WarnContext(ctx, "telegram gateway: failed to answer callback", "bot", b.role, "error", err)
	}
}

// download asks the local Bot API server for the file and validates its path. A failure is logged and
// the event goes on without the file.
func (g *gateway) download(ctx context.Context, b *bot, media *Media) {
	file, err := b.client.GetFile(ctx, media.FileID)
	if err != nil {
		g.log.ErrorContext(ctx, "telegram gateway: failed to get file", "bot", b.role, "media_type", media.Type, "error", err)

		return
	}

	var size int64

	dir, rel, err := locate([]string{g.filesDir}, file.FilePath)
	if err == nil {
		size, err = statInRoot(dir, rel, g.maxFileSize)
	}

	if errors.Is(err, ErrFileTooLarge) {
		media.TooLarge = true
		media.Size = max(media.Size, file.FileSize)

		return
	}

	if err != nil {
		g.log.ErrorContext(ctx, "telegram gateway: rejected file path", "bot", b.role, "media_type", media.Type, "error", err)

		return
	}

	media.Size = size
	media.dir, media.rel, media.maxSize = dir, rel, g.maxFileSize
}

// sanitize removes bot tokens (also inside file paths) from a text for the log and cuts it.
func (g *gateway) sanitize(text string) string {
	for _, b := range g.bots.list() {
		text = b.client.MaskToken(text)
	}

	if runes := []rune(text); len(runes) > maxPanicLength {
		return string(runes[:maxPanicLength]) + "…"
	}

	return text
}

func waitGroup(ctx context.Context, wg *sync.WaitGroup) error {
	done := make(chan struct{})

	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("failed to wait: %w", ctx.Err())
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("failed to wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
