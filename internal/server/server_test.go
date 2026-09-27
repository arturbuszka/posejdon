package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"posejdon/internal/devices"
)

// fakePump udaje firmware: zapisuje ostatnie żądanie i odpowiada jak ESP32.
type fakePump struct {
	lastMethod, lastPath, lastBody string
}

func (f *fakePump) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.lastMethod, f.lastPath, f.lastBody = r.Method, r.URL.Path, string(body)
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/status":
		io.WriteString(w, `{"pumping":false,"mode":"interval"}`)
	case "/api/config":
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"ok":false,"error":"run_seconds exceeds the safety cap"}`)
	default:
		io.WriteString(w, `{"ok":true}`)
	}
}

func newTestServer(t *testing.T, devs []devices.Device) *httptest.Server {
	t.Helper()
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>posejdon</h1>")}}
	h, err := New(devs, static)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func withPump(t *testing.T) (*fakePump, *httptest.Server) {
	t.Helper()
	pump := &fakePump{}
	board := httptest.NewServer(pump)
	t.Cleanup(board.Close)
	srv := newTestServer(t, []devices.Device{
		{ID: "fontanna", Name: "Fontanna", Type: "pump", URL: board.URL},
	})
	return pump, srv
}

func do(t *testing.T, method, url, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var decoded map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&decoded)
	return resp.StatusCode, decoded
}

func TestListHidesURLs(t *testing.T) {
	_, srv := withPump(t)
	resp, err := http.Get(srv.URL + "/api/devices")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if strings.Contains(string(raw), "127.0.0.1") {
		t.Fatalf("device URL leaked: %s", raw)
	}
	if !strings.Contains(string(raw), `"id":"fontanna"`) {
		t.Fatalf("device missing: %s", raw)
	}
}

func TestProxiesStatus(t *testing.T) {
	pump, srv := withPump(t)
	code, body := do(t, "GET", srv.URL+"/api/devices/fontanna/status", "")
	if code != 200 || body["mode"] != "interval" {
		t.Fatalf("got %d %v", code, body)
	}
	if pump.lastPath != "/api/status" {
		t.Fatalf("board saw path %q", pump.lastPath)
	}
}

func TestProxiesHistory(t *testing.T) {
	pump, srv := withPump(t)
	code, _ := do(t, "GET", srv.URL+"/api/devices/fontanna/history", "")
	if code != 200 || pump.lastPath != "/api/history" {
		t.Fatalf("got %d, board saw path %q", code, pump.lastPath)
	}
}

func TestForwardsBody(t *testing.T) {
	pump, srv := withPump(t)
	code, _ := do(t, "POST", srv.URL+"/api/devices/fontanna/pump/on", `{"seconds":5}`)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if pump.lastMethod != "POST" || pump.lastPath != "/api/pump/on" || pump.lastBody != `{"seconds":5}` {
		t.Fatalf("board saw %s %s %q", pump.lastMethod, pump.lastPath, pump.lastBody)
	}
}

func TestPassesFirmwareErrorThrough(t *testing.T) {
	_, srv := withPump(t)
	code, body := do(t, "POST", srv.URL+"/api/devices/fontanna/config", `{"run_seconds":9999}`)
	if code != 400 || body["error"] != "run_seconds exceeds the safety cap" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestDeadBoardIs502(t *testing.T) {
	board := httptest.NewServer(http.NotFoundHandler())
	deadURL := board.URL
	board.Close()
	srv := newTestServer(t, []devices.Device{
		{ID: "fontanna", Name: "Fontanna", Type: "pump", URL: deadURL},
	})
	code, body := do(t, "GET", srv.URL+"/api/devices/fontanna/status", "")
	if code != 502 || body["error"] != "Urządzenie Fontanna nie odpowiada" {
		t.Fatalf("got %d %v", code, body)
	}
}

func TestRejects(t *testing.T) {
	pump, srv := withPump(t)
	cases := []struct {
		name, method, path string
		want               int
	}{
		{"unknown device", "GET", "/api/devices/nope/status", 404},
		{"path outside allowlist", "GET", "/api/devices/fontanna/ota", 404},
		{"wrong method", "GET", "/api/devices/fontanna/pump/on", 405},
		{"unknown api", "GET", "/api/whatever", 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pump.lastPath = ""
			code, body := do(t, c.method, srv.URL+c.path, "")
			if code != c.want {
				t.Fatalf("got %d, want %d", code, c.want)
			}
			if _, ok := body["error"]; !ok {
				t.Fatalf("no JSON error: %v", body)
			}
			if pump.lastPath != "" {
				t.Fatalf("request reached the board: %s", pump.lastPath)
			}
		})
	}
}

func TestServesApp(t *testing.T) {
	_, srv := withPump(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(raw), "posejdon") {
		t.Fatalf("got %d %s", resp.StatusCode, raw)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("missing no-cache")
	}
}
