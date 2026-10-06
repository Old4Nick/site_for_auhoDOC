// Package money keeps all arithmetic in integer kopecks.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

var ErrInvalid = errors.New("укажите неотрицательную сумму в рублях с точкой и двумя знаками после неё")

// Parse accepts the same exact decimal syntax for HTTP forms and CSV.
// Signs, separators, whitespace and exponent notation are not accepted.
func Parse(value string) (int64, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[0]) == 0 || len(parts[1]) != 2 {
		return 0, ErrInvalid
	}
	for _, part := range parts {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, ErrInvalid
			}
		}
	}
	minor, err := strconv.ParseUint(strings.TrimLeft(parts[0]+parts[1], "0"), 10, 63)
	if strings.Trim(parts[0]+parts[1], "0") == "" {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("%w: превышен допустимый диапазон", ErrInvalid)
	}
	return int64(minor), nil
}

func Decimal(minor int64) string {
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

func Format(minor int64) string {
	rubles := strconv.FormatInt(minor/100, 10)
	for i := len(rubles) - 3; i > 0; i -= 3 {
		rubles = rubles[:i] + " " + rubles[i:]
	}
	return fmt.Sprintf("%s руб. %02d коп.", rubles, minor%100)
}

// ParseRate returns a canonical exact decimal rate, retaining fractional digits
// where needed. NUMERIC(5,2) permits values from 0 through 999.99.
func ParseRate(value string) (string, error) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return "", errors.New("укажите ставку от 0 до 999.99, не более двух знаков после точки")
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return "", errors.New("в ставке должно быть не более двух знаков после точки")
		}
		fraction = parts[1] + strings.Repeat("0", 2-len(parts[1]))
	}
	n, err := Parse(parts[0] + "." + fraction)
	if err != nil || n > 99999 {
		return "", errors.New("укажите ставку от 0 до 999.99")
	}
	return strings.TrimSuffix(strings.TrimRight(Decimal(n), "0"), "."), nil
}

func Cost(price, vat int64, mode, rate string) string {
	if mode == "none" {
		return Format(price) + ", без НДС"
	}
	return Format(price) + ", в т. ч. НДС " + rate + "% — " + Format(vat)
}

func Add(total, amount int64) (int64, error) {
	if total < 0 || amount < 0 || total > math.MaxInt64-amount {
		return 0, errors.New("общая сумма превышает допустимый диапазон")
	}
	return total + amount, nil
}
