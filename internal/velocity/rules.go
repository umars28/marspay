package velocity

import (
	"fmt"
	"time"

	"github.com/umars28/marspay/internal/money"
)

type Action string

const (
	ActionAllow          Action = "allow"
	ActionFlag           Action = "flag"
	ActionRequireOTP     Action = "require_otp"
	ActionHoldWithdrawal Action = "hold_withdrawal"
	ActionFreeze         Action = "freeze"
)

var severityOf = map[Action]int{
	ActionAllow:          0,
	ActionFlag:           1,
	ActionRequireOTP:     2,
	ActionHoldWithdrawal: 3,
	ActionFreeze:         4,
}

func (a Action) strongerThan(other Action) bool {
	return severityOf[a] > severityOf[other]
}

type Mode string

const (
	ModeMonitor  Mode = "monitor"
	ModeActive   Mode = "active"
	ModeDisabled Mode = "disabled"
)

type Metric string

const (
	MetricCount Metric = "count"
	MetricSum   Metric = "sum"
)

const ReportingThreshold = money.Minor(100_000_000_00)

type Kind string

const (
	KindPayment    Kind = "payment"
	KindTransfer   Kind = "transfer"
	KindTopup      Kind = "topup"
	KindWithdrawal Kind = "withdrawal"
	KindBill       Kind = "bill_payment"
)

type Subject struct {
	UserID       string
	Kind         Kind
	Amount       money.Minor
	NewRecipient bool
	NewDevice    bool
	NewBank      bool
}

type Rule struct {
	Code        string
	Description string
	Metric      Metric
	Window      time.Duration
	Threshold   int64
	Action      Action
	Mode        Mode

	triggersOn func(Subject) bool
	checksOn   func(Subject) bool
}

func (r Rule) Key(userID string) string {
	return fmt.Sprintf("velocity:%s:%s", r.Code, userID)
}

func (r Rule) Triggers(s Subject) bool {
	return r.triggersOn != nil && r.triggersOn(s)
}

func (r Rule) Checks(s Subject) bool {
	if r.checksOn != nil {
		return r.checksOn(s)
	}
	return r.Triggers(s)
}

func (r Rule) delta(s Subject) int64 {
	if r.Metric == MetricSum {
		return int64(s.Amount)
	}
	return 1
}

func outbound(s Subject) bool {
	switch s.Kind {
	case KindPayment, KindTransfer, KindWithdrawal, KindBill:
		return true
	default:
		return false
	}
}

func DefaultRules() []Rule {
	return []Rule{
		{
			Code:        "VR-02",
			Description: "New device and new bank account within a day",
			Metric:      MetricCount,
			Window:      24 * time.Hour,
			Threshold:   1,
			Action:      ActionFlag,
			Mode:        ModeMonitor,
			triggersOn: func(s Subject) bool {
				return s.NewDevice && s.NewBank
			},
		},
		{
			Code:        "VR-03",
			Description: "Transfers to new recipients in a short window",
			Metric:      MetricCount,
			Window:      5 * time.Minute,
			Threshold:   10,
			Action:      ActionFreeze,
			Mode:        ModeActive,
			triggersOn: func(s Subject) bool {
				return s.Kind == KindTransfer && s.NewRecipient
			},
		},
		{
			Code:        "VR-05",
			Description: "Amounts sitting just under the reporting threshold",
			Metric:      MetricCount,
			Window:      7 * 24 * time.Hour,
			Threshold:   3,
			Action:      ActionFreeze,
			Mode:        ModeActive,
			triggersOn: func(s Subject) bool {
				if !outbound(s) {
					return false
				}
				floor := ReportingThreshold * 90 / 100
				return s.Amount >= floor && s.Amount < ReportingThreshold
			},
		},
		{
			Code:        "VR-07",
			Description: "Withdrawing straight after a top up",
			Metric:      MetricCount,
			Window:      10 * time.Minute,
			Threshold:   1,
			Action:      ActionHoldWithdrawal,
			Mode:        ModeActive,
			triggersOn: func(s Subject) bool {
				return s.Kind == KindTopup
			},
			checksOn: func(s Subject) bool {
				return s.Kind == KindWithdrawal
			},
		},
		{
			Code:        "VR-08",
			Description: "Total outbound value in an hour",
			Metric:      MetricSum,
			Window:      time.Hour,
			Threshold:   int64(money.Minor(50_000_000_00)),
			Action:      ActionRequireOTP,
			Mode:        ModeActive,
			triggersOn:  outbound,
		},
	}
}
