// Package connectors provides the concrete, dependency-light adapters that
// implement the capability interfaces declared in pkg/connector: payment
// gateways (razorpay, paytm, phonepe), notification providers (twilio, msg91),
// and delivery aggregators (zomato, swiggy), plus mock adapters for tests.
//
// Every adapter is self-contained in its own file and implements
// connector.Connector plus exactly one capability interface. Manifest() returns
// the marketplace descriptor (id, name, capabilities, JSON config schema) that
// connector-hub lists; New() (registry.go) is the factory that payments,
// notifications, aggregators and the hub use to instantiate an adapter from a
// tenant's stored config map.
//
// Adapters depend only on the standard library (net/http, crypto/*,
// encoding/json) — no vendor SDKs — so they compile anywhere and stay easy to
// audit. Credentials are read from the cfg map passed to Init/New.
package connectors
