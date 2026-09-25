// Package devices wczytuje listę płytek, którymi steruje serwer.
package devices

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
)

// Device to jedna płytka w sieci lokalnej.
type Device struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type mówi PWA, jaki ekran pokazać. Na razie jedyny typ to "pump".
	Type string `json:"type"`
	// URL to bazowy adres płytki, np. http://192.168.1.42. IP, nie nazwa .local:
	// mDNS nie działa w kontenerze.
	URL string `json:"url"`
}

// id trafia do ścieżki URL, więc trzymamy je w bezpiecznym alfabecie.
var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Load czyta plik devices.json.
func Load(path string) ([]Device, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	devs, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return devs, nil
}

// Parse dekoduje i waliduje listę płytek. Literówka w nazwie pola jest błędem,
// a nie cichym pominięciem - inaczej płytka "znikałaby" bez wyjaśnienia.
func Parse(r io.Reader) ([]Device, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()

	var devs []Device
	if err := dec.Decode(&devs); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	seen := make(map[string]bool, len(devs))
	for i := range devs {
		d := &devs[i]
		if !validID.MatchString(d.ID) {
			return nil, fmt.Errorf("device #%d: id %q must match %s", i, d.ID, validID)
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("device %q: duplicate id", d.ID)
		}
		seen[d.ID] = true

		if d.Type == "" {
			return nil, fmt.Errorf("device %q: missing type", d.ID)
		}
		if d.Name == "" {
			d.Name = d.ID
		}

		u, err := url.Parse(d.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("device %q: url %q must be http(s)://host", d.ID, d.URL)
		}
	}
	return devs, nil
}
