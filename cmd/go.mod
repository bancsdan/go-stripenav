// Module: cmd binaries for github.com/bancsdan/go-stripenav.
//
// Separate from the parent module so that `go get github.com/bancsdan/
// go-stripenav` users never download the binary source or its
// transitive deps. During local development the replace directive
// below binds against the parent checkout, so `task dev`, `go run
// ./cmd/...`, and editor "go to definition" all use the in-tree source.

module github.com/bancsdan/go-stripenav/cmd

go 1.26.2

require (
	github.com/bancsdan/go-stripenav v0.0.0
	github.com/jackc/pgx/v5 v5.9.2
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/stripe/stripe-go/v82 v82.5.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

replace github.com/bancsdan/go-stripenav => ..
