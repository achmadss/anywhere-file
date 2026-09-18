// Package qa holds the failure suite (#40): the promises in docs/new-arch.md about what
// happens when something breaks, each one a test against real processes. There is no code
// here, only tests, and they run when RFM_E2E_DATABASE_URL names a database to point the
// control plane at.
package qa
