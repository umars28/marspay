package rail

import (
	"context"
	"errors"
	"time"

	"github.com/umars28/marspay/internal/money"
)

type Name string

const (
	BIFast       Name = "bifast"
	BankInternal Name = "bank_internal"
	SKNBI        Name = "sknbi"
)

var (
	ErrRejected  = errors.New("rail: the destination bank rejected the transfer")
	ErrTimeout   = errors.New("rail: the bank did not answer in time")
	ErrRailDown  = errors.New("rail: the rail is unavailable")
	ErrAmbiguous = errors.New("rail: timed out after the transfer may have succeeded")
)

type Request struct {
	PayoutID    string
	BankCode    string
	AccountNo   string
	AccountName string
	Amount      money.Minor
}

type Result struct {
	Rail          Name
	BankReference string
	Latency       time.Duration
	CallbackSent  bool
}

type Rail interface {
	Name() Name
	Send(ctx context.Context, req Request) (Result, error)
}

type Health struct {
	Rail        Name
	Share       int
	P95         time.Duration
	SuccessRate float64
	Degraded    bool
}

func Select(rails []Rail, bankCode string, health map[Name]Health) Rail {
	var fallback Rail

	for _, r := range rails {
		h, known := health[r.Name()]
		if known && h.Degraded {
			if fallback == nil {
				fallback = r
			}
			continue
		}
		if r.Name() == BankInternal && bankCode == "BCA" {
			return r
		}
		if fallback == nil {
			fallback = r
		}
	}

	for _, r := range rails {
		if h, known := health[r.Name()]; r.Name() == BIFast && (!known || !h.Degraded) {
			return r
		}
	}
	return fallback
}
