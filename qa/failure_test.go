package qa

// One test per row of #40's table, plus the promise underneath it: no failure takes the
// device key or a user's file with it.

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// No Internet: LAN discovery and local apps work, remote fails with a clear error.
func TestWithNoInternetTheLANWorksAndTheServerSaysTheDeviceIsOffline(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	h.wire.cut()

	if got := string(h.lanGet(t, "/hello.txt")); got != hello {
		t.Errorf("on the LAN the file reads %q, want %q", got, hello)
	}
	// The document a client reads after it finds the PC on the network, which is what
	// local mode starts from and which never needed an account.
	resp, err := lanClient.Get("https://" + h.gatewayAddr + "/.well-known/anywhere-file")
	if err != nil {
		t.Fatalf("the discovery document: %v", err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the discovery document: status %d", resp.StatusCode)
	}

	h.until(t, time.Minute, "the remote path to report the PC offline", func() bool {
		resp, err := h.remote(h.admin, "/hello.txt")
		if err != nil {
			return false
		}
		defer drain(resp)
		return resp.StatusCode == http.StatusServiceUnavailable
	})
	// The refusal says which of the many reasons it was.
	resp, err = h.remote(h.admin, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("device offline")) {
		t.Errorf("the server answered %q, want it to say the device is offline", body)
	}
}

// Server down: as above, and the agent reconnects when the server returns, with nobody
// restarting it.
func TestTheAgentComesBackWhenTheServerDoes(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	was := h.agent.cmd.Process.Pid
	h.server.kill()

	if got := string(h.lanGet(t, "/hello.txt")); got != hello {
		t.Errorf("on the LAN the file reads %q, want %q", got, hello)
	}
	if _, err := h.admin.do(http.MethodGet, "/healthz", nil); err == nil {
		t.Error("the server answered after it was stopped")
	}

	if err := h.startServer(); err != nil {
		t.Fatal(err)
	}
	h.until(t, 2*time.Minute, "the tunnel to come back on its own", h.remoteWorks)
	if !h.agent.alive() || h.agent.cmd.Process.Pid != was {
		t.Errorf("the agent is pid %d, want the same process %d that was running before", h.agent.cmd.Process.Pid, was)
	}
}

// Agent restart: same device id, tunnel back, apps served.
func TestARestartedAgentIsTheSamePC(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	h.agent.stop()
	if err := h.startAgent(); err != nil {
		t.Fatal(err)
	}

	id, err := h.readDeviceID()
	if err != nil {
		t.Fatal(err)
	}
	if id != h.deviceID {
		t.Errorf("device id = %s after a restart, want %s", id, h.deviceID)
	}
	h.until(t, 2*time.Minute, "the tunnel to come back", h.remoteWorks)
	if got := string(h.lanGet(t, "/hello.txt")); got != hello {
		t.Errorf("on the LAN the file reads %q, want %q", got, hello)
	}
}

// Tunnel drops mid-request: the request fails cleanly, the next one succeeds after the
// agent has reconnected.
func TestATunnelDroppedMidRequestFailsCleanly(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	resp, err := h.remote(h.admin, "/big.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	// Enough to know the file is arriving, little enough that the rest of it cannot
	// already be in flight.
	if _, err := io.ReadFull(resp.Body, make([]byte, 32<<10)); err != nil {
		t.Fatalf("the download never started: %v", err)
	}

	h.wire.cut()
	read, err := io.Copy(io.Discard, resp.Body)
	if err == nil {
		t.Errorf("the download ended without an error after %d more bytes, want a failure rather than a short file", read)
	}
	h.wire.restore()

	h.until(t, 2*time.Minute, "the next request to succeed", h.remoteWorks)
}

// Duplicate tunnel: one mapping survives and the stale one is closed. Two agents holding
// one device key is what a restored machine image looks like.
func TestASecondTunnelReplacesTheFirst(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	replaced := `rfm_tunnel_disconnects_total{reason="replaced"}`
	before := h.metric(t, replaced)

	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	// The same directory, so the same device key: to the server this is the same PC
	// dialling twice. Its own gateway is on another port and its own dufs cannot have the
	// one that is taken, which is why the file below still comes from the first one.
	cmd := h.agentCmd("run")
	cmd.Env = append(cmd.Env, "RFM_AGENT_ADDR=127.0.0.1:"+strconv.Itoa(port))
	second, err := start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer second.stop()

	h.until(t, time.Minute, "the server to close the first tunnel", func() bool {
		return h.metric(t, replaced) > before
	})
	h.until(t, time.Minute, "requests to keep working through the tunnel that survived", h.remoteWorks)
	if live := h.metric(t, "rfm_tunnels_live"); live != 1 {
		t.Errorf("%v tunnels live, want the one mapping a device is allowed", live)
	}
}

// User revoked mid-session: the next request is denied, and the LAN is untouched.
func TestARevokedGuestIsDeniedAndTheLANIsUntouched(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	guest, err := newAccount(h.serverURL, "guest@example.com")
	if err != nil {
		t.Fatal(err)
	}
	var invite struct {
		Code string `json:"code"`
	}
	if code, err := h.admin.json(http.MethodPost, "/v1/devices/"+h.deviceID+"/invites", map[string]string{"role": "guest"}, &invite); err != nil || code != http.StatusOK {
		t.Fatalf("invite: status %d, %v", code, err)
	}
	if code, err := guest.json(http.MethodPost, "/v1/invites/redeem", map[string]string{"code": invite.Code}, nil); err != nil || code != http.StatusOK {
		t.Fatalf("redeem: status %d, %v", code, err)
	}

	resp, err := h.remote(guest, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the guest was refused before being revoked: status %d", resp.StatusCode)
	}

	var users struct {
		Users []struct {
			UserID string `json:"user_id"`
			Email  string `json:"email"`
		} `json:"users"`
	}
	if code, err := h.admin.json(http.MethodGet, "/v1/devices/"+h.deviceID+"/users", nil, &users); err != nil || code != http.StatusOK {
		t.Fatalf("device users: status %d, %v", code, err)
	}
	target := ""
	for _, u := range users.Users {
		if u.Email == "guest@example.com" {
			target = u.UserID
		}
	}
	if target == "" {
		t.Fatalf("the guest is not bound to the device: %+v", users.Users)
	}
	if code, err := h.admin.json(http.MethodPost, "/v1/devices/"+h.deviceID+"/users/"+target+"/revoke", nil, nil); err != nil || code != http.StatusOK {
		t.Fatalf("revoke: status %d, %v", code, err)
	}

	// The next request, with the same session the guest already held.
	resp, err = h.remote(guest, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	drain(resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("the revoked guest got status %d, want 404", resp.StatusCode)
	}
	if !h.remoteWorks() {
		t.Error("the admin was refused too, so revocation took more than the one binding")
	}
	if got := string(h.lanGet(t, "/hello.txt")); got != hello {
		t.Errorf("on the LAN the file reads %q, want %q", got, hello)
	}
}

// PC switched off: the client is told, inside one heartbeat.
func TestAPCThatIsSwitchedOffIsReportedOffline(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	h.agent.kill()
	h.killStrays()

	// The request path knows at once: there is nothing on the other end of the tunnel.
	h.until(t, 30*time.Second, "a request to be refused", func() bool {
		resp, err := h.remote(h.admin, "/hello.txt")
		if err != nil {
			return false
		}
		defer drain(resp)
		return resp.StatusCode == http.StatusServiceUnavailable
	})
	// The device list is what the client shows before anyone asks for a file, so the PC
	// has to stop looking reachable there too. The server pings every 30 seconds and
	// gives up 10 seconds later.
	h.untilEvery(t, 2*time.Minute, 3*time.Second, "the device list to say the PC is offline", func() bool {
		return !h.deviceOnline(t)
	})
}

// The promise under the table: no failure deletes the device key or a user's file. The
// agent is killed in the middle of an upload, which is the worst moment there is.
func TestNoFailureCostsTheDeviceKeyOrAFile(t *testing.T) {
	t.Cleanup(func() { h.settle(t) })
	kept := make([]byte, 3<<20)
	if _, err := rand.Read(kept); err != nil {
		t.Fatal(err)
	}
	put(t, h.lanURL("/kept.bin"), bytes.NewReader(kept))

	// An upload the harness can hold open, so the agent dies in the middle of it rather
	// than at whatever moment a size happens to produce.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPut, h.lanURL("/half.bin"), pr)
		if err != nil {
			done <- err
			return
		}
		resp, err := lanClient.Do(req)
		if err == nil {
			drain(resp)
		}
		done <- err
	}()
	if _, err := pw.Write(make([]byte, 1<<20)); err != nil {
		t.Fatalf("the upload never started: %v", err)
	}

	h.agent.kill()
	h.killStrays()
	_, _ = pw.Write(make([]byte, 1<<20))
	_ = pw.Close()
	if err := <-done; err == nil {
		t.Error("the upload reported success although the PC died during it")
	}

	if err := h.startAgent(); err != nil {
		t.Fatal(err)
	}
	if id, err := h.readDeviceID(); err != nil || id != h.deviceID {
		t.Fatalf("device id = %s (%v) after the agent was killed, want %s", id, err, h.deviceID)
	}
	if got := h.lanGet(t, "/kept.bin"); !bytes.Equal(got, kept) {
		t.Errorf("kept.bin is %d bytes after the failure, want the %d that went up", len(got), len(kept))
	}
	// Whatever is left of the interrupted upload, the length dufs reports for it is the
	// length it has. A file that reads short of what it promises is the corruption this
	// row is about.
	reported(t)
}

// reported compares every file on disk with what dufs says about it through the gateway.
func reported(t *testing.T) {
	t.Helper()
	err := filepath.WalkDir(h.shared, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(h.shared, path)
		if err != nil {
			return err
		}
		resp, err := lanClient.Head(h.lanURL("/" + filepath.ToSlash(rel)))
		if err != nil {
			return err
		}
		defer drain(resp)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s is on disk and dufs answers %d for it", rel, resp.StatusCode)
		}
		if resp.ContentLength != info.Size() {
			return fmt.Errorf("%s is %d bytes on disk and dufs reports %d", rel, info.Size(), resp.ContentLength)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func put(t *testing.T, url string, body io.Reader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url, body)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := lanClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", url, err)
	}
	defer drain(resp)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		t.Fatalf("PUT %s: status %d", url, resp.StatusCode)
	}
}
