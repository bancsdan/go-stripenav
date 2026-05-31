// Module: examples for github.com/bancsdan/go-stripenav.
//
// This is a SEPARATE Go module from the parent. It exists so that
// `go get github.com/bancsdan/go-stripenav` users never download the
// example code or its transitive dependencies. During local
// development the replace directive below binds against the parent
// checkout so `task dev`, `go run ./examples/...`, and editor "go to
// definition" all use the in-tree source.

module github.com/bancsdan/go-stripenav/examples

go 1.26.2

require github.com/bancsdan/go-stripenav v0.0.0

require (
	github.com/stripe/stripe-go/v82 v82.5.1 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
)

replace github.com/bancsdan/go-stripenav => ..
