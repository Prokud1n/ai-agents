package gateway

import "go.uber.org/fx"

// Module runs the Telegram gateway when bot tokens are set and provides the Sender either way
// (without tokens its methods return ErrDisabled) and the Status for /readyz. The default Handler is
// AckHandler and the Menu is empty; in the application teamchat.Module replaces both with fx.Decorate.
var Module = fx.Module("gateway",
	fx.Provide(newBots, newStateStore, newGateway, fx.Private),
	fx.Provide(
		NewSender,
		NewStatus,
		NewMenu,
		fx.Annotate(NewAckHandler, fx.As(new(Handler))),
	),
	fx.Invoke(func(*gateway) {}),
)
