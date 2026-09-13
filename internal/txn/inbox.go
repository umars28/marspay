package txn

import (
	"context"
	"fmt"
	"time"
)

type Notification struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	Amount    int64     `json:"amount,omitempty"`
	Unread    bool      `json:"unread"`
	CreatedAt time.Time `json:"created_at"`
}

type Inbox struct {
	Data   []Notification `json:"data"`
	Unread int            `json:"unread"`
	Source string         `json:"source"`
}

const unreadWindow = 24 * time.Hour

func (s *Service) Notifications(ctx context.Context, userID string, limit int) (*Inbox, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}

	page, err := s.History(ctx, userID, limit)
	if err != nil {
		return nil, err
	}

	inbox := &Inbox{
		Data:   []Notification{},
		Source: "derived from activity, money requests and points; there is no stored inbox yet",
	}
	cutoff := time.Now().Add(-unreadWindow)

	for _, a := range page.Data {
		n := Notification{
			ID:        "ntf_" + a.ID,
			Kind:      a.Kind,
			Amount:    a.Amount,
			Unread:    a.CreatedAt.After(cutoff),
			CreatedAt: a.CreatedAt,
		}

		switch a.Status {
		case "succeeded":
			n.Title = titleFor(a.Kind, a.Direction)
		case "pending":
			n.Title = titleFor(a.Kind, a.Direction) + " is still processing"
		case "failed":
			n.Title = titleFor(a.Kind, a.Direction) + " failed"
		default:
			n.Title = titleFor(a.Kind, a.Direction)
		}

		n.Detail = a.Counterpart
		if n.Detail == "" {
			n.Detail = a.Currency
		}
		inbox.Data = append(inbox.Data, n)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT r.id, COALESCE(u.full_name, r.requester_id), r.amount_minor,
		        COALESCE(r.note, ''), r.created_at
		 FROM money_requests r
		 LEFT JOIN users u ON u.id = r.requester_id
		 WHERE r.payer_id = $1 AND r.status = 'pending'
		 ORDER BY r.created_at DESC
		 LIMIT 20`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id, requester, note string
			amount              int64
			created             time.Time
		)
		if err := rows.Scan(&id, &requester, &amount, &note, &created); err != nil {
			return nil, err
		}
		inbox.Data = append(inbox.Data, Notification{
			ID:        "ntf_" + id,
			Kind:      "money_request",
			Title:     fmt.Sprintf("%s asked you for money", requester),
			Detail:    note,
			Amount:    amount,
			Unread:    true,
			CreatedAt: created,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var expiring int64
	var expiresAt *time.Time
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(SUM(e.amount), 0), min(e.expires_at)
		 FROM point_entries e
		 JOIN point_accounts a ON a.id = e.account_id
		 WHERE a.user_id = $1 AND e.amount > 0 AND e.expires_at IS NOT NULL
		   AND e.expires_at BETWEEN now() AND now() + interval '30 days'`, userID).
		Scan(&expiring, &expiresAt)
	if err != nil {
		return nil, err
	}
	if expiring > 0 && expiresAt != nil {
		inbox.Data = append(inbox.Data, Notification{
			ID:        "ntf_points_expiry",
			Kind:      "points",
			Title:     fmt.Sprintf("%d points expire soon", expiring),
			Detail:    "Redeem them before " + expiresAt.Format("2 Jan"),
			Unread:    true,
			CreatedAt: *expiresAt,
		})
	}

	for i := range inbox.Data {
		if inbox.Data[i].Unread {
			inbox.Unread++
		}
	}
	return inbox, nil
}

func titleFor(kind, direction string) string {
	switch kind {
	case "payment":
		return "Payment sent"
	case "transfer":
		if direction == "in" {
			return "Money received"
		}
		return "Transfer sent"
	case "topup":
		return "Top up"
	case "withdrawal":
		return "Withdrawal"
	case "bill_payment":
		return "Bill payment"
	case "refund":
		return "Refund received"
	default:
		return "Activity"
	}
}
