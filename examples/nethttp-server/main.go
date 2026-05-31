// Minimal example server wiring the stripenav handler into net/http.
//
// Stripe-side setup:
//  1. Create a webhook endpoint pointed at https://your-host/webhooks/stripe
//  2. Subscribe at least to: invoice.finalized, invoice.voided,
//     invoice.marked_uncollectible, credit_note.created, credit_note.voided
//  3. Copy the endpoint signing secret (whsec_…) into STRIPE_WEBHOOK_SECRET.
//
// NAV-side setup:
//  1. Provision a "technical user" (műszaki felhasználó) on the NAV
//     Online Számla portal.
//  2. Copy the login, password, taxNumber, signKey, and exchangeKey into
//     the matching NAV_* env vars below.
//  3. Set NAV_BASE_URL to either of the URLs exported as
//     nav.ProductionBaseURL or nav.TestBaseURL.
//
// This example uses the bundled InMemoryStore, which is not durable. For
// production wire your own SubmissionStore against your database.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	stripenav "github.com/bancsdan/go-stripenav"
	"github.com/bancsdan/go-stripenav/mapping"
	"github.com/bancsdan/go-stripenav/nav"
	"github.com/bancsdan/go-stripenav/storeinmem"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := stripenav.Config{
		StripeWebhookSecret: mustEnv("STRIPE_WEBHOOK_SECRET"),
		NAV: nav.Config{
			BaseURL:     getenv("NAV_BASE_URL", nav.TestBaseURL),
			Login:       mustEnv("NAV_LOGIN"),
			Password:    mustEnv("NAV_PASSWORD"),
			TaxNumber:   mustEnv("NAV_TAX_NUMBER"),
			SignKey:     mustEnv("NAV_SIGN_KEY"),
			ExchangeKey: mustEnv("NAV_EXCHANGE_KEY"),
			Debug:       os.Getenv("NAV_DEBUG") == "true",
			Software: nav.Software{
				ID:             getenv("NAV_SOFTWARE_ID", "GOSTRIPENAV000001"),
				Name:           "go-stripenav example",
				Operation:      "LOCAL_SOFTWARE",
				MainVersion:    "0.1.0",
				DevName:        getenv("NAV_DEV_NAME", "example"),
				DevContact:     getenv("NAV_DEV_CONTACT", "noreply@example.com"),
				DevCountryCode: "HU",
			},
		},
		Supplier: mapping.Supplier{
			TaxNumber: mustEnv("SUPPLIER_TAX_NUMBER"),
			Name:      mustEnv("SUPPLIER_NAME"),
			Address: mapping.Address{
				CountryCode: getenv("SUPPLIER_COUNTRY", "HU"),
				PostalCode:  mustEnv("SUPPLIER_POSTAL_CODE"),
				City:        mustEnv("SUPPLIER_CITY"),
				AdditionalDetail: getenv("SUPPLIER_ADDRESS", ""),
			},
		},
		Store:                storeinmem.New(),
		ExchangeRateProvider: devRateProvider,
	}

	h, err := stripenav.Handler(cfg)
	if err != nil {
		logger.Error("stripenav.Handler", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/webhooks/stripe", h)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	addr := getenv("LISTEN_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	go func() {
		logger.Info("server listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("listen", "err", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	logger.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server shutdown", "err", err)
	}
	if err := h.Shutdown(ctx); err != nil {
		logger.Error("handler shutdown", "err", err)
	}
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("missing required env var", "key", key)
		os.Exit(1)
	}
	return v
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// devRateProvider is a dev-time stub: fixed approximate rates so that
// invoices triggered via `stripe trigger` succeed mapping end-to-end.
// Replace with a real rate source (MNB, ECB, your billing system) in
// production.
func devRateProvider(_ context.Context, currency string, _ time.Time) (string, error) {
	rates := map[string]string{
		"eur": "395.00",
		"usd": "362.00",
		"gbp": "456.00",
		"chf": "401.00",
	}
	if r, ok := rates[strings.ToLower(currency)]; ok {
		return r, nil
	}
	return "", fmt.Errorf("no dev exchange rate for %s", currency)
}
