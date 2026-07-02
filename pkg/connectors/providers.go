package connectors

// This file is intentionally an empty package stub. An earlier draft defined a
// shared hmacProvider payment base here (a dependency-free stand-in for the
// gateways). It was superseded by the real per-provider HTTP adapters —
// razorpay.go, paytm.go, phonepe.go — each of which makes documented REST calls
// and verifies webhook signatures itself. Left empty to avoid duplicate symbols.
