package health

import "context"

// Checker is a dependency pinged by /readyz.
type Checker interface {
	Name() string
	Ping(ctx context.Context) error
}

// Pinger is anything that can report its availability, e.g. a provider client.
type Pinger interface {
	Ping(ctx context.Context) error
}

type namedChecker struct {
	name   string
	pinger Pinger
}

// NewChecker turns a pinger into a /readyz dependency with the given name, so providers need only
// Ping and do not depend on this package.
func NewChecker(name string, p Pinger) Checker {
	return namedChecker{name: name, pinger: p}
}

func (c namedChecker) Name() string {
	return c.name
}

func (c namedChecker) Ping(ctx context.Context) error {
	return c.pinger.Ping(ctx)
}
