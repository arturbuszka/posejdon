// Package server wystawia PWA i proxy do REST API płytek.
//
// Proxy nie jest tylko wygodą: firmware nie wysyła nagłówków CORS, więc
// przeglądarka nie może wołać płytki bezpośrednio ze strony serwowanej stąd.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"posejdon/internal/devices"
)

// UpstreamTimeout ogranicza czekanie na płytkę. ESP32 odpowiada w
// milisekundach; jeśli milczy dłużej, to jest offline, a UI ma się o tym dowiedzieć.
const UpstreamTimeout = 5 * time.Second

// Request bodies to kilka liczb. Firmware i tak odrzuca > 256 B.
const maxBodyBytes = 4 << 10

// allowedRoutes to jedyne ścieżki, które przepuszczamy do płytki, z
// dozwolonymi metodami. Reszta firmware'u (i cokolwiek dojdzie) nie wycieka.
var allowedRoutes = map[string][]string{
	"status":   {http.MethodGet},
	"history":  {http.MethodGet},
	"config":   {http.MethodGet, http.MethodPost},
	"pump/on":  {http.MethodPost},
	"pump/off": {http.MethodPost},
}

type device struct {
	info  devices.Device
	proxy *httputil.ReverseProxy
}

type publicDevice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// New buduje handler całego serwera. static to pliki PWA.
func New(devs []devices.Device, static fs.FS) (http.Handler, error) {
	// ESP32 obsługuje kilka gniazd naraz, więc nie trzymamy ich otwartych dłużej, niż trzeba.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 1
	transport.IdleConnTimeout = 15 * time.Second

	byID := make(map[string]*device, len(devs))
	list := make([]publicDevice, 0, len(devs))
	for _, d := range devs {
		target, err := url.Parse(d.URL)
		if err != nil {
			return nil, fmt.Errorf("device %q: %w", d.ID, err)
		}
		byID[d.ID] = &device{info: d, proxy: newProxy(d, target, transport)}
		list = append(list, publicDevice{ID: d.ID, Name: d.Name, Type: d.Type})
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/devices", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("/api/devices/{id}/{path...}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := byID[r.PathValue("id")]
		if !ok {
			writeError(w, http.StatusNotFound, "Nieznane urządzenie")
			return
		}
		methods, ok := allowedRoutes[r.PathValue("path")]
		if !ok {
			writeError(w, http.StatusNotFound, "Nieznany endpoint")
			return
		}
		if !contains(methods, r.Method) {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			writeError(w, http.StatusMethodNotAllowed, "Niedozwolona metoda")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), UpstreamTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		d.proxy.ServeHTTP(w, r)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Nieznany endpoint")
	})
	mux.Handle("/", noCache(http.FileServerFS(static)))

	return mux, nil
}

func newProxy(d devices.Device, target *url.URL, transport http.RoundTripper) *httputil.ReverseProxy {
	basePath := strings.TrimSuffix(target.Path, "/")
	return &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = target.Scheme
			pr.Out.URL.Host = target.Host
			pr.Out.URL.Path = basePath + "/api/" + pr.In.PathValue("path")
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = ""
			pr.Out.Host = target.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			slog.Info("proxy", "device", d.ID, "method", resp.Request.Method,
				"path", resp.Request.URL.Path, "status", resp.StatusCode)
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("proxy failed", "device", d.ID, "path", r.URL.Path, "err", err)
			writeError(w, http.StatusBadGateway, fmt.Sprintf("Urządzenie %s nie odpowiada", d.Name))
		},
	}
}

// noCache: pliki są wbudowane w binarkę i nie mają daty modyfikacji, więc
// bez tego Safari mógłby trzymać starą wersję aplikacji po aktualizacji serwera.
func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError używa tego samego kształtu co firmware, więc UI czyta błąd
// z jednego miejsca niezależnie od tego, kto go zgłosił.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": message})
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
