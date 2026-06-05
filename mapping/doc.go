// Package mapping holds the consumer-facing types the bridge needs from
// the caller: Supplier and Address, both used as fields on
// stripenav.Config. The actual Stripe → NAV translation logic lives in
// internal/invoicemap and is not exposed.
package mapping
