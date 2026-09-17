package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// History must survive its writers. The schema trigger rejects updates and
// deletes; this test proves the trigger is installed, not just written.
func TestAuditLogIsAppendOnly(t *testing.T) {
	pool := freshDB(t, 4)
	ctx := t.Context()

	if err := appendAudit(ctx, pool, "dev-1", "actor-1", ActionAccountDeleted, AuditDetails{AccountID: "acc-1"}); err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE audit_events SET action = 'tampered'`); err == nil {
		t.Error("UPDATE audit_events succeeded, want the append-only trigger to reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("UPDATE failed with %v, want the append-only error", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_events`); err == nil {
		t.Error("DELETE audit_events succeeded, want the append-only trigger to reject it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Errorf("DELETE failed with %v, want the append-only error", err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("%d rows after rejected writes, want the original 1", n)
	}
}

// The hard constraint: the cloud never receives a filesystem path (r3 section
// 7), and the audit log is where one would leak in by accident. Three locks:
// the detail struct has a fixed field set with no path-shaped name, every
// value rejects path characters, and a serialized payload never contains one.
func TestAuditPayloadCannotHoldAPath(t *testing.T) {
	wantFields := []string{"account_id", "device_key", "role", "prev_role", "from_status", "to_status", "reason"}
	gotFields := []string{}
	typ := reflect.TypeOf(AuditDetails{})
	for i := range typ.NumField() {
		tag := typ.Field(i).Tag.Get("json")
		name := strings.TrimSuffix(tag, ",omitempty")
		gotFields = append(gotFields, name)
		lower := strings.ToLower(typ.Field(i).Name + " " + name)
		for _, banned := range []string{"path", "file", "dir", "root"} {
			if strings.Contains(lower, banned) {
				t.Errorf("AuditDetails field %q looks able to hold a path", typ.Field(i).Name)
			}
		}
	}
	if !reflect.DeepEqual(gotFields, wantFields) {
		t.Errorf("AuditDetails fields = %v, want %v; a new field needs a path review", gotFields, wantFields)
	}

	paths := []string{
		"/etc/passwd", "a/b", `C:\keys\x`, "../escape", "ws/../x",
		"share/file.txt", "x\x00y", "/",
	}
	for _, p := range paths {
		if err := appendAudit(t.Context(), nil, p, "actor", ActionAccountDeleted, AuditDetails{}); err == nil {
			t.Errorf("device %q accepted, want rejection", p)
		}
		if err := appendAudit(t.Context(), nil, "", p, ActionAccountDeleted, AuditDetails{}); err == nil {
			t.Errorf("actor %q accepted, want rejection", p)
		}
		for field, fill := range map[string]func(*AuditDetails){
			"account_id":  func(d *AuditDetails) { d.AccountID = p },
			"device_key":  func(d *AuditDetails) { d.DeviceKey = p },
			"role":        func(d *AuditDetails) { d.Role = p },
			"prev_role":   func(d *AuditDetails) { d.PrevRole = p },
			"from_status": func(d *AuditDetails) { d.FromStatus = p },
			"to_status":   func(d *AuditDetails) { d.ToStatus = p },
			"reason":      func(d *AuditDetails) { d.Reason = p },
		} {
			var det AuditDetails
			fill(&det)
			if err := appendAudit(t.Context(), nil, "", "actor", ActionAccountDeleted, det); err == nil {
				t.Errorf("details.%s = %q accepted, want rejection", field, p)
			}
		}
	}

	// Unknown actions and empty identifiers fail before any database is touched.
	if err := appendAudit(t.Context(), nil, "", "actor", "remote_access.enabled-typo", AuditDetails{}); err == nil {
		t.Error("unknown action accepted, want rejection")
	}
	if err := appendAudit(t.Context(), nil, "", "", ActionAccountDeleted, AuditDetails{}); err == nil {
		t.Error("empty actor accepted, want rejection")
	}

	// Representative payloads serialize with no path separator anywhere.
	valid := []AuditDetails{
		{},
		{AccountID: "11111111-1111-1111-1111-111111111111", Role: "manager"},
		{DeviceKey: "abcdef0123456789", AccountID: "22222222-2222-2222-2222-222222222222"},
		{FromStatus: "active", ToStatus: "suspended", Reason: "subscription_lapsed"},
		{PrevRole: "member", Role: "manager"},
	}
	for _, det := range valid {
		raw, err := json.Marshal(det)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.ContainsAny(string(raw), "/\\\x00") {
			t.Errorf("payload %s contains a path character", raw)
		}
	}
}
