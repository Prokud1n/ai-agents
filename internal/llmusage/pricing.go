package llmusage

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	// Prices are kept in thousandths of a dollar per million tokens, which is also
	// nano-dollars per token: costs are exact integers, without float rounding.
	priceFractionDigits = 3
	costFractionDigits  = 9
	nanoPerUnit         = 1_000_000_000
	pricePartsCount     = 4 // input/output/cache_write/cache_read
	decimalBase         = 10
	int64Bits           = 64
)

var errInvalidPrice = errors.New("invalid price")

// Price is a model price in nano-dollars per token (= thousandths of a dollar per million tokens).
type Price struct {
	Input      int64
	Output     int64
	CacheWrite int64
	CacheRead  int64
}

// Prices maps a model name to its price.
type Prices map[string]Price

// Tokens are billed tokens of one or more calls.
type Tokens struct {
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
}

func (t Tokens) Add(other Tokens) Tokens {
	return Tokens{
		Input:      t.Input + other.Input,
		Output:     t.Output + other.Output,
		CacheRead:  t.CacheRead + other.CacheRead,
		CacheWrite: t.CacheWrite + other.CacheWrite,
	}
}

// Cost returns the cost in nano-dollars.
func (p Price) Cost(t Tokens) int64 {
	return t.Input*p.Input + t.Output*p.Output + t.CacheWrite*p.CacheWrite + t.CacheRead*p.CacheRead
}

// Cost returns the cost in nano-dollars; false if the model has no price.
func (p Prices) Cost(model string, t Tokens) (int64, bool) {
	price, ok := p[model]
	if !ok {
		return 0, false
	}

	return price.Cost(t), true
}

// ParsePrices parses "model=input/output/cache_write/cache_read,..." with prices in USD per million tokens,
// up to 3 fraction digits. An empty string means no prices.
func ParsePrices(s string) (Prices, error) {
	prices := make(Prices)

	for entry := range strings.SplitSeq(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		model, values, ok := strings.Cut(entry, "=")
		model = strings.TrimSpace(model)

		if !ok || model == "" {
			return nil, fmt.Errorf("failed to parse price %q: expected model=input/output/cache_write/cache_read: %w",
				entry, errInvalidPrice)
		}

		if _, exists := prices[model]; exists {
			return nil, fmt.Errorf("failed to parse prices: model %q is listed twice: %w", model, errInvalidPrice)
		}

		price, err := parsePrice(values)
		if err != nil {
			return nil, fmt.Errorf("failed to parse price of %q: %w", model, err)
		}

		prices[model] = price
	}

	return prices, nil
}

func parsePrice(s string) (Price, error) {
	parts := strings.Split(s, "/")
	if len(parts) != pricePartsCount {
		return Price{}, fmt.Errorf("expected 4 values input/output/cache_write/cache_read, got %q: %w",
			s, errInvalidPrice)
	}

	values := make([]int64, 0, len(parts))

	for _, part := range parts {
		v, err := parseThousandths(strings.TrimSpace(part))
		if err != nil {
			return Price{}, err
		}

		values = append(values, v)
	}

	return Price{Input: values[0], Output: values[1], CacheWrite: values[2], CacheRead: values[3]}, nil
}

// parseThousandths parses a non-negative decimal with up to 3 fraction digits into thousandths: "2.5" -> 2500.
func parseThousandths(s string) (int64, error) {
	return parseFixed(s, priceFractionDigits)
}

// parseFixed parses a non-negative decimal with up to digits fraction digits into an integer
// scaled by 10^digits.
func parseFixed(s string, digits int) (int64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if whole == "" || len(frac) > digits || !digitsOnly(whole) || !digitsOnly(frac) {
		return 0, fmt.Errorf("expected a non-negative number with up to %d fraction digits, got %q: %w",
			digits, s, errInvalidPrice)
	}

	frac += strings.Repeat("0", digits-len(frac))

	v, err := strconv.ParseInt(whole+frac, decimalBase, int64Bits)
	if err != nil {
		return 0, fmt.Errorf("failed to parse %q: %w", s, errInvalidPrice)
	}

	return v, nil
}

func digitsOnly(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}

	return true
}

// FormatUSD formats nano-dollars as a decimal with 9 fraction digits: 1234500 -> "0.001234500".
func FormatUSD(nano int64) string {
	sign := ""
	if nano < 0 {
		sign = "-"
		nano = -nano
	}

	return fmt.Sprintf("%s%d.%0*d", sign, nano/nanoPerUnit, costFractionDigits, nano%nanoPerUnit)
}

// parseUSD parses a non-negative decimal (as returned by PostgreSQL numeric::text) into nano-dollars.
func parseUSD(s string) (int64, error) {
	return parseFixed(s, costFractionDigits)
}
