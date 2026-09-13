package money

import (
	"errors"
	"fmt"
	"strings"
)

type Minor int64

const (
	MinorPerRupiah = 100
	BasisPoints    = 10_000
)

var (
	ErrNegativeAmount = errors.New("money: amount must not be negative")
	ErrInvalidBps     = errors.New("money: basis points must be between 0 and 10000")
)

func FromRupiah(r int64) Minor {
	return Minor(r * MinorPerRupiah)
}

func (m Minor) Rupiah() int64 {
	return int64(m) / MinorPerRupiah
}

func (m Minor) IsZero() bool {
	return m == 0
}

func (m Minor) String() string {
	sign := ""
	v := int64(m)
	if v < 0 {
		sign = "-"
		v = -v
	}

	whole := v / MinorPerRupiah
	sen := v % MinorPerRupiah

	var b strings.Builder
	digits := fmt.Sprintf("%d", whole)
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}

	if sen == 0 {
		return fmt.Sprintf("%sRp %s", sign, b.String())
	}
	return fmt.Sprintf("%sRp %s,%02d", sign, b.String(), sen)
}

func FeeHalfUp(amount Minor, bps int) (Minor, error) {
	if amount < 0 {
		return 0, ErrNegativeAmount
	}
	if bps < 0 || bps > BasisPoints {
		return 0, fmt.Errorf("%w: got %d", ErrInvalidBps, bps)
	}

	product := int64(amount) * int64(bps)
	fee := product / BasisPoints
	if remainder := product % BasisPoints; remainder*2 >= BasisPoints {
		fee++
	}
	return Minor(fee), nil
}
