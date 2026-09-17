package main

import (
	"net/http"
	"strings"
	"testing"
)

func setStatus(t *testing.T, h http.Handler, accountID, status string, headers map[string]string) int {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/admin/subscriptions/"+accountID,
		map[string]string{"status": status}, headers).Code
}

func operator(token string) map[string]string {
	return map[string]string{operatorHeader: token}
}

// A new account starts active, an operator can suspend it, and the gate routing (#88)
// reads answers from the stored status.
func TestOperatorFlipsTheSubscriptionGate(t *testing.T) {
	t.Setenv(operatorTokenEnv, "operator-secret")
	pool := freshDB(t, 4)
	m := NewMetrics()
	h := newHandler(pool, discard, m)
	signupReq(t, h, "owner@example.com", "correct horse battery")
	id := accountIDByEmail(t, pool, "owner@example.com")

	if n := countRows(t, pool, `SELECT count(*) FROM subscriptions WHERE account_id = $1::uuid AND status = 'active'`, id); n != 1 {
		t.Fatalf("active subscriptions after signup = %d, want 1", n)
	}
	active, err := subscriptionActive(t.Context(), pool, id)
	if err != nil || !active {
		t.Fatalf("new account active = %v (err %v), want true", active, err)
	}

	if code := setStatus(t, h, id, "suspended", operator("operator-secret")); code != http.StatusOK {
		t.Fatalf("suspend: status = %d, want 200", code)
	}
	if active, err := subscriptionActive(t.Context(), pool, id); err != nil || active {
		t.Errorf("suspended account active = %v (err %v), want false", active, err)
	}
	if code := setStatus(t, h, id, "active", operator("operator-secret")); code != http.StatusOK {
		t.Fatalf("restore: status = %d, want 200", code)
	}
	if active, err := subscriptionActive(t.Context(), pool, id); err != nil || !active {
		t.Errorf("restored account active = %v (err %v), want true", active, err)
	}

	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1`, ActionSubscriptionChanged); n != 2 {
		t.Errorf("audit rows = %d, want 2", n)
	}
	want := `rfm_subscription_transitions_total{from_status="active",to_status="suspended"} 1`
	if got := scrape(t, m); !strings.Contains(got, want) {
		t.Errorf("scrape missing %q:\n%s", want, got)
	}
}

func TestSubscriptionEndpointRefusals(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	signupReq(t, h, "owner@example.com", "correct horse battery")
	id := accountIDByEmail(t, pool, "owner@example.com")

	// No secret configured at all: the endpoint answers as if it were not there.
	if code := setStatus(t, h, id, "suspended", operator("operator-secret")); code != http.StatusNotFound {
		t.Errorf("flip with no secret configured: status = %d, want 404", code)
	}

	t.Setenv(operatorTokenEnv, "operator-secret")
	for name, tc := range map[string]struct {
		account string
		status  string
		headers map[string]string
		want    int
	}{
		"no token":          {id, "suspended", nil, http.StatusNotFound},
		"wrong token":       {id, "suspended", operator("guess"), http.StatusNotFound},
		"unknown account":   {"11111111-1111-1111-1111-111111111111", "suspended", operator("operator-secret"), http.StatusNotFound},
		"malformed account": {"not-a-uuid", "suspended", operator("operator-secret"), http.StatusNotFound},
		"unknown status":    {id, "cancelled", operator("operator-secret"), http.StatusBadRequest},
	} {
		if code := setStatus(t, h, tc.account, tc.status, tc.headers); code != tc.want {
			t.Errorf("%s: status = %d, want %d", name, code, tc.want)
		}
	}
	if n := countRows(t, pool, `SELECT count(*) FROM subscriptions WHERE account_id = $1::uuid AND status = 'active'`, id); n != 1 {
		t.Errorf("account left %d active rows, want 1 (no refusal may change the status)", n)
	}
}
