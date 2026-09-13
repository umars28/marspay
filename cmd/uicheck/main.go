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
	run      = fmt.Sprint(time.Now().UnixNano())
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
		req.Header.Set("Idempotency-Key", fmt.Sprintf("uicheck-%s-%s", run, path))
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

	fmt.Println()
	if failures == 0 {
		fmt.Println("every call the consumer UI makes works against the running API")
		return
	}
	fmt.Printf("%d problems the UI would hit\n", failures)
	os.Exit(1)
}
