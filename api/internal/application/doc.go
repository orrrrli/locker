// Package application holds the use cases, grouped by feature in subpackages.
//
// Use cases own transactions through a transaction runner, so cross-row
// invariants run in the same transaction as the write. It depends on domain only.
//
// Repository interfaces are declared here, on the consuming side: each feature
// package declares only the methods it needs.
package application
