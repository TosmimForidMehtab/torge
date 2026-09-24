package webhook_test

import (
	"encoding/base64"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/TosmimForidMehtab/torge"
	"github.com/TosmimForidMehtab/torge/cache"
	"github.com/TosmimForidMehtab/torge/torgetest"
	"github.com/TosmimForidMehtab/torge/webhook"
)

var now = time.Unix(1_700_000_000, 0)

func TestStripeWebhook(t *testing.T) {
	store := cache.NewMemory()
	defer store.Close()
	var handled atomic.Int32
	var fail atomic.Bool
	app := torgetest.NewApp(t)
	app.POST("/webhooks/stripe", func(c *torge.Context) error {
		var ev struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		}
		if err := webhook.Decode(c, &ev); err != nil {
			return err
		}
		if fail.Load() {
			return torge.ServiceUnavailable("TRY_LATER", "later")
		}
		handled.Add(1)
		if webhook.EventFrom(c).ID != "evt_1" {
			t.Error("event ID must be extracted")
		}
		return c.NoContent(204)
	}, webhook.Middleware(webhook.Config{
		Verifier: webhook.Stripe("whsec_old", "whsec_new"),
		Replay:   store,
		Now:      func() time.Time { return now },
	}))
	tc := torgetest.New(t, app)

	body := `{"id":"evt_1","type":"payment_intent.succeeded"}`
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := webhook.Sign([]byte("whsec_new"), []byte(ts+"."+body), webhook.Hex)
	send := func(header string) *torgetest.Response {
		return tc.POST("/webhooks/stripe").Header("Stripe-Signature", header).
			Body(strings.NewReader(body), "application/json").Do()
	}

	send("").ExpectStatus(401).ExpectErrorCode(webhook.CodeSignatureMissing)
	send("t=" + ts + ",v1=deadbeef").ExpectStatus(401).ExpectErrorCode(webhook.CodeSignatureInvalid)
	old := strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)
	send("t=" + old + ",v1=" + webhook.Sign([]byte("whsec_new"), []byte(old+"."+body), webhook.Hex)).
		ExpectStatus(401).ExpectErrorCode(webhook.CodeTimestamp)

	fail.Store(true)
	send("t=" + ts + ",v1=" + sig).ExpectStatus(503)
	fail.Store(false)
	send("t=" + ts + ",v1=" + sig).ExpectStatus(204)
	send("t="+ts+",v1="+sig).ExpectStatus(200).ExpectHeader("Webhook-Duplicate", "true")
	if handled.Load() != 1 {
		t.Fatalf("handled = %d, want exactly one successful processing", handled.Load())
	}
}

func TestGitHubAndStandardWebhooks(t *testing.T) {
	app := torgetest.NewApp(t)
	ok := func(c *torge.Context) error { return c.String(200, string(webhook.RawBody(c))) }
	app.POST("/github", ok, webhook.Middleware(webhook.Config{Verifier: webhook.GitHub("gh-secret")}))
	secret := base64.StdEncoding.EncodeToString([]byte("standard-secret"))
	std, err := webhook.StandardWebhooks("whsec_" + secret)
	if err != nil {
		t.Fatal(err)
	}
	app.POST("/std", ok, webhook.Middleware(webhook.Config{Verifier: std, Now: func() time.Time { return now }}))
	tc := torgetest.New(t, app)

	body := `{"action":"opened"}`
	tc.POST("/github").Header("X-Hub-Signature-256", "sha256="+webhook.Sign([]byte("gh-secret"), []byte(body), webhook.Hex)).
		Body(strings.NewReader(body), "application/json").Do().ExpectStatus(200).ExpectBody(body)
	tc.POST("/github").Header("X-Hub-Signature-256", "sha256="+webhook.Sign([]byte("other"), []byte(body), webhook.Hex)).
		Body(strings.NewReader(body), "application/json").Do().ExpectStatus(401)

	ts := strconv.FormatInt(now.Unix(), 10)
	sig := webhook.Sign([]byte("standard-secret"), []byte("msg_1."+ts+"."+body), webhook.Base64)
	tc.POST("/std").Header("webhook-id", "msg_1").Header("webhook-timestamp", ts).
		Header("webhook-signature", "v1,bogus v1,"+sig).Body(strings.NewReader(body), "application/json").Do().
		ExpectStatus(200)
}
