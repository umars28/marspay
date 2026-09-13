package wallet

import (
	"context"
	"errors"
	"sync"

	"github.com/umars28/marspay/internal/money"
)

var (
	ErrInsufficientFunds = errors.New("wallet: available balance is below the requested amount")
	ErrAccountNotCached  = errors.New("wallet: account balance is not in the cache")
)

type Reserver interface {
	Reserve(ctx context.Context, accountID string, amount money.Minor) (money.Minor, error)
	Release(ctx context.Context, accountID string, amount money.Minor) (money.Minor, error)
	Available(ctx context.Context, accountID string) (money.Minor, error)
	Warm(ctx context.Context, accountID string, balance money.Minor) error
	Invalidate(ctx context.Context, accountID string) error
}

type Memory struct {
	mu       sync.Mutex
	balances map[string]money.Minor
}

func NewMemory() *Memory {
	return &Memory{balances: make(map[string]money.Minor)}
}

func (m *Memory) Warm(_ context.Context, accountID string, balance money.Minor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.balances[accountID] = balance
	return nil
}

func (m *Memory) Invalidate(_ context.Context, accountID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.balances, accountID)
	return nil
}

func (m *Memory) Available(_ context.Context, accountID string) (money.Minor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	balance, ok := m.balances[accountID]
	if !ok {
		return 0, ErrAccountNotCached
	}
	return balance, nil
}

func (m *Memory) Reserve(_ context.Context, accountID string, amount money.Minor) (money.Minor, error) {
	if amount <= 0 {
		return 0, money.ErrNegativeAmount
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	balance, ok := m.balances[accountID]
	if !ok {
		return 0, ErrAccountNotCached
	}
	if balance < amount {
		return 0, ErrInsufficientFunds
	}

	m.balances[accountID] = balance - amount
	return m.balances[accountID], nil
}

func (m *Memory) Release(_ context.Context, accountID string, amount money.Minor) (money.Minor, error) {
	if amount <= 0 {
		return 0, money.ErrNegativeAmount
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	balance, ok := m.balances[accountID]
	if !ok {
		return 0, ErrAccountNotCached
	}

	m.balances[accountID] = balance + amount
	return m.balances[accountID], nil
}
