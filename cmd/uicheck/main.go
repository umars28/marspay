package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

var (
	base     string
	origin   string
	consumer string
	friendNo string
	pin      string
	merchant string
	operator string
	apiKey   string
	run      = fmt.Sprint(time.Now().UnixNano())
	seq      int
	failures int
)

func call(method, path, token string, body any) (int, map[string]any, http.Header) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, r)
	req.Header.Set("Origin", origin)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method != "GET" {
		seq++
		req.Header.Set("Idempotency-Key", fmt.Sprintf("uicheck-%s-%d", run, seq))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("transport:", err)
		os.Exit(1)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return resp.StatusCode, out, resp.Header
}

func check(label string, got int, want int, out map[string]any, fields ...string) {
	status := "ok "
	if got != want {
		status = "FAIL"
		failures++
	}
	missing := []string{}
	for _, f := range fields {
		if _, present := out[f]; !present {
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 && got == want {
		status = "FAIL"
		failures++
	}
	line := fmt.Sprintf("%-4s %-34s %d", status, label, got)
	if len(missing) > 0 {
		line += fmt.Sprintf("  missing fields the UI reads: %v", missing)
	}
	if got != want {
		line += fmt.Sprintf("  want %d  %v", want, out)
	}
	fmt.Println(line)
}

func main() {
	flag.StringVar(&base, "base", "http://127.0.0.1:8080", "API base URL")
	flag.StringVar(&origin, "origin", "http://127.0.0.1:8932", "origin the UI is served from")
	flag.StringVar(&consumer, "consumer", "081200000001", "demo consumer phone")
	flag.StringVar(&friendNo, "friend", "081200000002", "a second consumer, used where velocity rules forbid reuse")
	flag.StringVar(&pin, "pin", "294715", "demo PIN")
	flag.StringVar(&merchant, "merchant", "merch_demo", "demo merchant id")
	flag.StringVar(&operator, "operator", "081200000009", "demo operator phone")
	flag.StringVar(&apiKey, "merchant-key", "", "merchant API key; skips the merchant checks when empty")
	flag.Parse()

	fmt.Printf("==> checking what the consumer UI calls, from origin %s\n\n", origin)

	preflight, _ := http.NewRequest("OPTIONS", base+"/v1/payments", nil)
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	preflight.Header.Set("Access-Control-Request-Headers", "authorization,content-type,idempotency-key")
	pr, err := http.DefaultClient.Do(preflight)
	if err != nil {
		fmt.Println("preflight transport:", err)
		os.Exit(1)
	}
	pr.Body.Close()
	allow := pr.Header.Get("Access-Control-Allow-Origin")
	if pr.StatusCode != 204 || allow != origin {
		fmt.Printf("FAIL CORS preflight: status %d allow-origin %q\n", pr.StatusCode, allow)
		failures++
	} else {
		fmt.Printf("ok   CORS preflight from %s\n", origin)
	}

	code, ch, _ := call("POST", "/v1/auth/otp", "", map[string]any{"phone": consumer})
	check("POST /v1/auth/otp", code, 201, ch, "id", "code")

	code, tok, hdr := call("POST", "/v1/auth/token", "", map[string]any{
		"challenge_id": ch["id"], "code": ch["code"], "pin": pin,
		"platform": "web", "model": "Mockup browser"})
	check("POST /v1/auth/token", code, 201, tok, "access_token", "refresh_token", "device_id")
	if hdr.Get("Access-Control-Allow-Origin") != origin {
		fmt.Println("FAIL the token response carries no CORS header")
		failures++
	}
	access, _ := tok["access_token"].(string)
	lastConsumerToken = access

	code, out, _ := call("GET", "/v1/balance", access, nil)
	check("GET  /v1/balance", code, 200, out, "available", "held", "cache_agreed")

	code, out, _ = call("GET", "/v1/me", access, nil)
	check("GET  /v1/me", code, 200, out, "name", "phone", "kyc_tier", "limits")

	code, out, _ = call("GET", "/v1/transactions?limit=25", access, nil)
	check("GET  /v1/transactions", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/points", access, nil)
	check("GET  /v1/points", code, 200, out, "balance")

	code, out, _ = call("GET", "/v1/devices", access, nil)
	check("GET  /v1/devices", code, 200, out, "data")

	code, out, _ = call("POST", "/v1/payments", access, map[string]any{
		"merchant_id": merchant, "method": "qris", "amount": 3200000, "currency": "IDR"})
	check("POST /v1/payments", code, 201, out, "amount", "fee", "ledger_transaction_id")
	lastPaymentID, _ = out["id"].(string)

	code, out, _ = call("POST", "/v1/transfers", access, map[string]any{
		"to": "081200000002", "amount": 5000000, "currency": "IDR"})
	check("POST /v1/transfers", code, 201, out, "amount", "payee")

	_, ch2, _ := call("POST", "/v1/auth/otp", "", map[string]any{"phone": friendNo})
	_, tok2, _ := call("POST", "/v1/auth/token", "", map[string]any{
		"challenge_id": ch2["id"], "code": ch2["code"], "pin": pin,
		"platform": "web", "model": "Mockup browser"})
	friend, _ := tok2["access_token"].(string)

	code, out, _ = call("POST", "/v1/withdrawals", friend, map[string]any{
		"bank_code": "BCA", "account_number": "1234567890",
		"account_name": "Demo Friend", "amount": 1000000, "currency": "IDR"})
	check("POST /v1/withdrawals", code, 201, out, "total_debited", "admin_fee")

	code, out, _ = call("POST", "/v1/topups", access, map[string]any{
		"source": "bank_va", "provider_code": "bca", "amount": 50000000, "currency": "IDR"})
	check("POST /v1/topups", code, 201, out, "virtual_account", "status")

	checkWallet(access)
	checkMerchant()
	checkOperator()

	fmt.Println()
	if failures == 0 {
		fmt.Println("every call the UI makes works against the running API")
		return
	}
	fmt.Printf("%d problems the UI would hit\n", failures)
	os.Exit(1)
}

func checkWallet(access string) {
	fmt.Println()

	code, out, _ := call("GET", "/v1/billers", access, nil)
	check("GET  /v1/billers", code, 200, out, "data")

	code, out, _ = call("POST", "/v1/billers/PLN_POSTPAID/inquire", access,
		map[string]any{"customer_ref": "512201884471"})
	check("POST /v1/billers/{code}/inquire", code, 200, out, "amount", "total_payable")

	quoted, _ := out["amount"].(float64)
	code, out, _ = call("POST", "/v1/bill-payments", access, map[string]any{
		"biller_code": "PLN_POSTPAID", "customer_ref": "512201884471",
		"amount": int64(quoted), "currency": "IDR"})
	check("POST /v1/bill-payments", code, 201, out, "status")

	code, out, _ = call("GET", "/v1/promos", access, nil)
	check("GET  /v1/promos", code, 200, out, "data")
	if list, ok := out["data"].([]any); !ok || len(list) == 0 {
		fmt.Println("FAIL the promo screen would be empty: no active offer is seeded")
		failures++
	}

	code, out, _ = call("POST", "/v1/promos/apply", access, map[string]any{
		"code": "COFFEE30", "spend": 3200000})
	check("POST /v1/promos/apply", code, 201, out, "value", "points")

	code, out, _ = call("GET", "/v1/points", access, nil)
	check("GET  /v1/points", code, 200, out, "balance")

	code, out, _ = call("GET", "/v1/money-requests", access, nil)
	check("GET  /v1/money-requests", code, 200, out, "data")

	code, out, _ = call("POST", "/v1/money-requests", access, map[string]any{
		"payer_id": friendNo, "amount": 1200000, "currency": "IDR", "note": "uicheck"})
	check("POST /v1/money-requests", code, 201, out, "amount", "status")

	code, out, _ = call("POST", "/v1/bill-splits", access, map[string]any{
		"title": "uicheck dinner", "total": 4800000, "currency": "IDR",
		"payers": []string{friendNo}})
	check("POST /v1/bill-splits", code, 201, out, "total", "participants")

	code, out, _ = call("GET", "/v1/notifications", access, nil)
	check("GET  /v1/notifications", code, 200, out, "data", "unread")
}

func checkMerchant() {
	fmt.Println()
	if apiKey == "" {
		fmt.Println("--   merchant screens skipped: pass -merchant-key")
		return
	}

	code, out, _ := call("GET", "/v1/payments?limit=50", apiKey, nil)
	check("GET  /v1/payments", code, 200, out, "data", "totals")

	code, out, _ = call("GET", "/v1/payouts?limit=50", apiKey, nil)
	check("GET  /v1/payouts", code, 200, out, "data", "summary")

	code, out, _ = call("GET", "/v1/payouts/config", apiKey, nil)
	if code == 404 {
		fmt.Println("ok   GET  /v1/payouts/config          404  no score recorded yet, the UI keeps its sample")
	} else {
		check("GET  /v1/payouts/config", code, 200, out, "holdback_bps", "score")
	}

	code, out, _ = call("GET", "/v1/settlements", apiKey, nil)
	check("GET  /v1/settlements", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/api-keys", apiKey, nil)
	check("GET  /v1/api-keys", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/outlets", apiKey, nil)
	check("GET  /v1/outlets", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/staff", apiKey, nil)
	check("GET  /v1/staff", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/webhook-endpoints", apiKey, nil)
	check("GET  /v1/webhook-endpoints", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/webhook-deliveries", apiKey, nil)
	check("GET  /v1/webhook-deliveries", code, 200, out, "data")

	code, out, _ = call("GET", "/v1/volume", apiKey, nil)
	check("GET  /v1/volume", code, 200, out, "data")

	code, out, _ = call("POST", "/v1/charges", apiKey, map[string]any{
		"description": "uicheck invoice", "amount": 1240000, "currency": "IDR",
		"expires_in": "2h"})
	check("POST /v1/charges", code, 201, out, "id", "reference", "expires_at")
	chargeID, _ := out["id"].(string)

	code, out, _ = call("GET", "/v1/charges", apiKey, nil)
	check("GET  /v1/charges", code, 200, out, "data")

	if chargeID != "" {
		code, out, _ = call("GET", "/v1/charges/"+chargeID, lastConsumerToken, nil)
		check("GET  /v1/charges/{id} (payer)", code, 200, out, "amount", "description")

		code, out, _ = call("POST", "/v1/payments", lastConsumerToken,
			map[string]any{"charge_id": chargeID})
		check("POST /v1/payments (link)", code, 201, out, "amount", "ledger_transaction_id")

		code, _, _ = call("POST", "/v1/payments", lastConsumerToken,
			map[string]any{"charge_id": chargeID})
		if code != 409 {
			fmt.Printf("FAIL a paid link was payable again: %d, want 409\n", code)
			failures++
		} else {
			fmt.Println("ok   a paid link cannot be paid twice             409")
		}
	}

	code, out, _ = call("POST", "/v1/api-keys", apiKey, map[string]any{
		"name": "uicheck", "mode": "test", "scopes": []string{"read"}})
	check("POST /v1/api-keys", code, 201, out, "prefix", "secret")

	code, out, _ = call("POST", "/v1/outlets", apiKey, map[string]any{"name": "uicheck outlet"})
	check("POST /v1/outlets", code, 201, out, "id", "name")

	if lastPaymentID != "" {
		code, out, _ = call("POST", "/v1/refunds", apiKey, map[string]any{
			"payment_id": lastPaymentID, "reason": "uicheck exercising the refund path"})
		check("POST /v1/refunds", code, 201, out, "id", "status")
	}
}

func checkOperator() {
	fmt.Println()

	_, ch, _ := call("POST", "/v1/auth/otp", "", map[string]any{"phone": operator})
	codeValue, _ := ch["code"].(string)
	challengeID, _ := ch["id"].(string)

	code, tok, _ := call("POST", "/v1/auth/token", "", map[string]any{
		"challenge_id": challengeID, "code": codeValue, "pin": pin,
		"platform": "web", "model": "Mockup browser"})
	check("POST /v1/auth/token (operator)", code, 201, tok, "access_token")
	token, _ := tok["access_token"].(string)

	code, out, _ := call("GET", "/internal/v1/float", token, nil)
	check("GET  /internal/v1/float", code, 200, out, "position", "top_exposure")

	code, out, _ = call("GET", "/internal/v1/payouts/engine", token, nil)
	check("GET  /internal/v1/payouts/engine", code, 200, out, "rails", "needing_attention")

	code, out, _ = call("GET", "/internal/v1/reconciliation", token, nil)
	check("GET  /internal/v1/reconciliation", code, 200, out, "runs", "open")

	code, out, _ = call("GET", "/internal/v1/audit?limit=40", token, nil)
	check("GET  /internal/v1/audit", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/blocks", token, nil)
	check("GET  /internal/v1/blocks", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/disputes", token, nil)
	check("GET  /internal/v1/disputes", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/velocity/rules", token, nil)
	check("GET  /internal/v1/velocity/rules", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/velocity/alerts", token, nil)
	check("GET  /internal/v1/velocity/alerts", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/search?q="+merchant, token, nil)
	check("GET  /internal/v1/search", code, 200, out, "data")

	code, out, _ = call("GET", "/internal/v1/accounts/acc_usr_demo_user_wallet", token, nil)
	check("GET  /internal/v1/accounts/{id}", code, 200, out, "balance", "entries")

	code, out, _ = call("GET", "/internal/v1/queues", token, nil)
	check("GET  /internal/v1/queues", code, 200, out, "queues", "jobs")

	code, out, _ = call("GET", "/internal/v1/transitions?limit=20", token, nil)
	check("GET  /internal/v1/transitions", code, 200, out, "data")
	if list, ok := out["data"].([]any); !ok || len(list) == 0 {
		fmt.Println("FAIL nothing recorded a state change, although a payout was settled")
		failures++
	}

	checkOpsActions(token)

	consumerToken := lastConsumerToken
	if consumerToken != "" {
		code, _, _ = call("GET", "/internal/v1/audit", consumerToken, nil)
		if code != 403 {
			fmt.Printf("FAIL a consumer token reached the audit log: %d, want 403\n", code)
			failures++
		} else {
			fmt.Println("ok   a consumer token is refused by the ops endpoints  403")
		}
	}
}

var lastConsumerToken string
var lastPaymentID string

func checkOpsActions(token string) {
	fmt.Println()

	code, out, _ := call("GET", "/internal/v1/kyc", token, nil)
	check("GET  /internal/v1/kyc", code, 200, out, "data")

	if queue, ok := out["data"].([]any); ok && len(queue) > 0 {
		if first, ok := queue[0].(map[string]any); ok {
			id, _ := first["id"].(string)
			code, out, _ = call("POST", "/internal/v1/kyc/"+id+"/review", token,
				map[string]any{"decision": "approved", "reason": "documents match"})
			check("POST /internal/v1/kyc/{id}/review", code, 200, out, "status")

			code, _, _ = call("POST", "/internal/v1/kyc/"+id+"/review", token,
				map[string]any{"decision": "approved", "reason": ""})
			if code != 422 {
				fmt.Printf("FAIL a review without a reason was accepted: %d, want 422\n", code)
				failures++
			} else {
				fmt.Println("ok   a review with no reason is refused                422")
			}
		}
	} else {
		fmt.Println("FAIL the KYC queue is empty, so the screen has nothing to show")
		failures++
	}

	if lastPaymentID != "" {
		code, out, _ = call("POST", "/v1/disputes", lastConsumerToken, map[string]any{
			"payment_id": lastPaymentID, "reason": "goods never arrived"})
		check("POST /v1/disputes (consumer)", code, 201, out, "id", "status")

		if id, ok := out["id"].(string); ok {
			code, out, _ = call("POST", "/internal/v1/disputes/"+id+"/resolve", token,
				map[string]any{"outcome": "resolved_user", "reason": "merchant did not respond"})
			check("POST /internal/v1/disputes/{id}/resolve", code, 200, out, "status")
		}
	}

	code, out, _ = call("POST", "/internal/v1/blocks", token, map[string]any{
		"subject_type": "user", "subject_id": "usr_demo_new",
		"reason": "uicheck exercising the block path"})
	check("POST /internal/v1/blocks", code, 201, out, "id", "subject_id")

	code, out, _ = call("POST", "/internal/v1/blocks/user/usr_demo_new/unblock", token,
		map[string]any{"reason": "uicheck finished"})
	check("POST /internal/v1/blocks/{..}/unblock", code, 200, out, "status")
}
