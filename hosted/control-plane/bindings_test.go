package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// enrolDeviceFor enrols a fresh key for the account and returns its device id.
func enrolDeviceFor(t *testing.T, h http.Handler, email, name string) string {
	t.Helper()
	priv := newDeviceKey(t)
	body := `{"name":"` + name + `","enrolment_token":"` + enrolToken(t, h, email) + `"}`
	if rec := enrol(t, h, priv, body, nil); rec.Code != http.StatusOK {
		t.Fatalf("enrol %s: status = %d (body %s)", name, rec.Code, rec.Body)
	}
	return derivedDeviceID(priv)
}

// bind writes a binding straight to the table, as an invite (#86) will, and returns a
// session for the bound account.
func bind(t *testing.T, h http.Handler, pool *pgxpool.Pool, deviceID, email, role string) string {
	t.Helper()
	signupReq(t, h, email, "correct horse battery")
	if _, err := pool.Exec(t.Context(),
		`INSERT INTO device_users (device_id, user_id, role) VALUES ($1, $2::uuid, $3)`,
		deviceID, accountIDByEmail(t, pool, email), role); err != nil {
		t.Fatal(err)
	}
	return signinToken(t, h, email, "correct horse battery")
}

func revoke(t *testing.T, h http.Handler, session, deviceID, userID string) int {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/devices/"+deviceID+"/users/"+userID+"/revoke", nil, bearer(session)).Code
}

func activeAdmins(t *testing.T, pool *pgxpool.Pool, deviceID string) int {
	t.Helper()
	return countRows(t, pool,
		`SELECT count(*) FROM device_users WHERE device_id = $1 AND role = 'admin' AND revoked_at IS NULL`, deviceID)
}

func TestGuestSeesOwnDevicesAndNothingElse(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	enrolDeviceFor(t, h, "other@example.com", "pc2")
	guest := bind(t, h, pool, pc1, "guest@example.com", "guest")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	if _, err := pool.Exec(t.Context(), `INSERT INTO device_apps (device_id, name, type) VALUES ($1, 'files', 'dufs')`, pc1); err != nil {
		t.Fatal(err)
	}
	ownerID := accountIDByEmail(t, pool, "owner@example.com")

	rec := doJSON(t, h, http.MethodGet, "/v1/devices", nil, bearer(guest))
	var list struct {
		Devices []struct {
			DeviceID  string   `json:"device_id"`
			Role      string   `json:"role"`
			RevokedAt *string  `json:"revoked_at"`
			Apps      []string `json:"apps"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("guest list: status = %d body %s err %v", rec.Code, rec.Body, err)
	}
	if len(list.Devices) != 1 || list.Devices[0].DeviceID != pc1 || list.Devices[0].Role != "guest" || list.Devices[0].RevokedAt != nil {
		t.Errorf("guest list = %+v, want only pc1 as an active guest", list.Devices)
	}
	if len(list.Devices) == 1 && fmt.Sprint(list.Devices[0].Apps) != "[files]" {
		t.Errorf("guest list apps = %v, want [files]", list.Devices[0].Apps)
	}

	if rec := doJSON(t, h, http.MethodGet, "/v1/devices/"+pc1+"/users", nil, bearer(guest)); rec.Code != http.StatusNotFound {
		t.Errorf("guest list users: status = %d, want 404", rec.Code)
	}
	if code := revoke(t, h, guest, pc1, ownerID); code != http.StatusNotFound {
		t.Errorf("guest revoke: status = %d, want 404", code)
	}
	if code := revoke(t, h, owner, pc1, "not-a-uuid"); code != http.StatusNotFound {
		t.Errorf("revoke of a malformed user id: status = %d, want 404", code)
	}

	rec = doJSON(t, h, http.MethodGet, "/v1/devices/"+pc1+"/users", nil, bearer(owner))
	var users struct {
		Users []struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		} `json:"users"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &users); err != nil || rec.Code != http.StatusOK || len(users.Users) != 2 {
		t.Fatalf("owner list users: status = %d body %s", rec.Code, rec.Body)
	}
}

func TestRevokeKeepsTheRowAndShowsInTheList(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	guest := bind(t, h, pool, pc1, "guest@example.com", "guest")
	guestID := accountIDByEmail(t, pool, "guest@example.com")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	if _, err := pool.Exec(t.Context(), `INSERT INTO device_apps (device_id, name, type) VALUES ($1, 'files', 'dufs')`, pc1); err != nil {
		t.Fatal(err)
	}

	if code := revoke(t, h, owner, pc1, guestID); code != http.StatusOK {
		t.Fatalf("revoke guest: status = %d, want 200", code)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM device_users WHERE device_id = $1 AND user_id = $2::uuid AND revoked_at IS NOT NULL`, pc1, guestID); n != 1 {
		t.Errorf("revoked rows = %d, want 1 (row must stay)", n)
	}
	if n := countRows(t, pool, `SELECT count(*) FROM audit_events WHERE action = $1 AND device_id = $2`, ActionBindingRevoked, pc1); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if code := revoke(t, h, owner, pc1, guestID); code != http.StatusNotFound {
		t.Errorf("second revoke: status = %d, want 404", code)
	}

	rec := doJSON(t, h, http.MethodGet, "/v1/devices", nil, bearer(guest))
	var list struct {
		Devices []struct {
			RevokedAt *string  `json:"revoked_at"`
			Apps      []string `json:"apps"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Devices) != 1 || list.Devices[0].RevokedAt == nil {
		t.Errorf("guest list after revoke = %s, want one device with revoked_at set", rec.Body)
	}
	if len(list.Devices) == 1 && len(list.Devices[0].Apps) != 0 {
		t.Errorf("revoked guest still sees apps %v", list.Devices[0].Apps)
	}
}

func TestLastAdminCannotBeRevoked(t *testing.T) {
	pool := freshDB(t, 4)
	h := newHandler(pool, discard, NewMetrics())
	pc1 := enrolDeviceFor(t, h, "owner@example.com", "pc1")
	owner := signinToken(t, h, "owner@example.com", "correct horse battery")
	ownerID := accountIDByEmail(t, pool, "owner@example.com")

	if code := revoke(t, h, owner, pc1, ownerID); code != http.StatusConflict {
		t.Errorf("sole admin revoking themself: status = %d, want 409", code)
	}
	second := bind(t, h, pool, pc1, "second@example.com", "admin")
	secondID := accountIDByEmail(t, pool, "second@example.com")
	if code := revoke(t, h, owner, pc1, ownerID); code != http.StatusOK {
		t.Errorf("admin leaving with another admin present: status = %d, want 200", code)
	}
	if code := revoke(t, h, second, pc1, secondID); code != http.StatusConflict {
		t.Errorf("new sole admin revoking themself: status = %d, want 409", code)
	}
	if code := revoke(t, h, owner, pc1, secondID); code != http.StatusNotFound {
		t.Errorf("revoked admin revoking: status = %d, want 404", code)
	}
	if n := activeAdmins(t, pool, pc1); n != 1 {
		t.Errorf("active admins = %d, want 1", n)
	}
}

// Two admins revoke each other at the same instant. Each request on its own is allowed,
// since the other admin is still there when it starts. Only one may win.
func TestConcurrentMutualRevokeLeavesOneAdmin(t *testing.T) {
	pool := freshDB(t, 8)
	h := newHandler(pool, discard, NewMetrics())
	ctx := t.Context()
	for _, email := range []string{"a@example.com", "b@example.com"} {
		signupReq(t, h, email, "correct horse battery")
	}
	a := signinToken(t, h, "a@example.com", "correct horse battery")
	b := signinToken(t, h, "b@example.com", "correct horse battery")
	aID, bID := accountIDByEmail(t, pool, "a@example.com"), accountIDByEmail(t, pool, "b@example.com")

	const rounds = 5
	for i := range rounds {
		var pc string
		if err := pool.QueryRow(ctx,
			`INSERT INTO devices (public_key, name) VALUES ($1, 'pc') RETURNING device_id`, fmt.Sprintf("%064x", i+1)).Scan(&pc); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO device_users (device_id, user_id, role) VALUES ($1, $2::uuid, 'admin'), ($1, $3::uuid, 'admin')`,
			pc, aID, bID); err != nil {
			t.Fatal(err)
		}

		var start, done sync.WaitGroup
		start.Add(1)
		codes := make([]int, 2)
		for j, who := range []struct{ session, target string }{{a, bID}, {b, aID}} {
			done.Add(1)
			go func() {
				defer done.Done()
				start.Wait()
				codes[j] = revoke(t, h, who.session, pc, who.target)
			}()
		}
		start.Done()
		done.Wait()

		wins := 0
		for _, c := range codes {
			if c == http.StatusOK {
				wins++
			}
		}
		if wins != 1 || activeAdmins(t, pool, pc) != 1 {
			t.Fatalf("round %d: codes %v, active admins %d; want one win and one admin left", i, codes, activeAdmins(t, pool, pc))
		}
	}
}
