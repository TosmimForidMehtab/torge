package app_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge/config"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/app"
	"github.com/TosmimForidMehtab/torge/examples/production/internal/platform"
	"github.com/TosmimForidMehtab/torge/torgetest"
	"github.com/TosmimForidMehtab/torge/webhook"
)

func testConfig(t *testing.T) *platform.Config {
	var cfg platform.Config
	err := config.Load(&cfg, config.WithMap(map[string]string{
		"API_TOKENS":     "token-a,token-b",
		"WEBHOOK_SECRET": "whsec-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	return &cfg
}

func newClient(t *testing.T) *torgetest.Client {
	cfg := testConfig(t)
	a := app.New(cfg, torgetest.NewAppOptions(t)...)
	return torgetest.New(t, a).WithBearerToken("token-a")
}

func TestEndToEnd(t *testing.T) {
	tc := newClient(t)

	// Authentication is required.
	tc.WithHeader("Authorization", "").GET("/v1/accounts/x").Do().ExpectStatus(401)

	acct := tc.POST("/v1/accounts").JSON(map[string]string{"name": "Ada", "email": "ada@example.com"}).Do().
		ExpectStatus(201).ExpectHeaderPresent("X-Request-Id")
	id, _ := acct.JSONPath("id")
	tc.GET("/v1/accounts/"+id.(string)).Do().ExpectStatus(200).ExpectJSONPath("email", "ada@example.com")
	tc.POST("/v1/accounts").JSON(map[string]string{"name": "Ada", "email": "ada@example.com"}).Do().
		ExpectStatus(409).ExpectErrorCode("EMAIL_TAKEN")
	tc.POST("/v1/accounts").JSON(map[string]string{"name": "A", "email": "bad"}).Do().
		ExpectStatus(422).ExpectErrorCode("VALIDATION_FAILED")

	// Payments require an idempotency key and replay on retry.
	payment := map[string]any{"account_id": id, "amount": 1500, "currency": "EUR"}
	tc.POST("/v1/payments").JSON(payment).Do().ExpectStatus(400).ExpectErrorCode("IDEMPOTENCY_KEY_REQUIRED")
	first := tc.POST("/v1/payments").Header("Idempotency-Key", "order-1").JSON(payment).Do().ExpectStatus(201)
	tc.POST("/v1/payments").Header("Idempotency-Key", "order-1").JSON(payment).Do().
		ExpectStatus(201).ExpectBody(string(first.Body)).ExpectHeader("Idempotent-Replayed", "true")
	payID, _ := first.JSONPath("id")

	// Signed webhook settles the payment; replays are acknowledged only once.
	body := `{"id":"evt_1","type":"payment.succeeded","data":{"payment_id":"` + payID.(string) + `"}}`
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := webhook.Sign([]byte("whsec-test"), []byte(ts+"."+body), webhook.Hex)
	send := func() *torgetest.Response {
		return tc.WithHeader("Authorization", "").POST("/webhooks/payments").
			Header("X-Signature", sig).Header("X-Timestamp", ts).Header("X-Event-Id", "evt_1").
			Body(strings.NewReader(body), "application/json").Do()
	}
	send().ExpectStatus(204)
	send().ExpectStatus(200).ExpectHeader("Webhook-Duplicate", "true")

	// Operational endpoints.
	tc.GET("/ready").Do().ExpectStatus(200)
	tc.GET("/health").Do().ExpectStatus(200)
	doc := tc.GET("/openapi.json").Do().ExpectStatus(200)
	for _, path := range []string{`"/v1/accounts"`, `"/v1/payments"`, `"/webhooks/payments"`, `"apiToken"`} {
		if !strings.Contains(string(doc.Body), path) {
			t.Errorf("OpenAPI document is missing %s", path)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	tc := newClient(t)
	tc.OPTIONS("/v1/payments").Header("Origin", "http://localhost:3000").
		Header("Access-Control-Request-Method", "POST").Do().
		ExpectStatus(204).ExpectHeader("Access-Control-Allow-Origin", "http://localhost:3000")
}

func TestInvalidConfigurationFailsFast(t *testing.T) {
	var cfg platform.Config
	err := config.Load(&cfg, config.WithMap(map[string]string{"REQUEST_TIMEOUT": "10m"}))
	if err == nil {
		t.Fatal("expected configuration errors")
	}
	for _, want := range []string{"API_TOKENS", "WEBHOOK_SECRET", "REQUEST_TIMEOUT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %s in:\n%v", want, err)
		}
	}
}
