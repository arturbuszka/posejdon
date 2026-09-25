# Posejdon

Domowy serwer do sterowania płytkami ESP32 z telefonu. Na razie obsługuje jedną
fontannę, czyli firmware [water-pump-control](../water-pump-control).

Serwer robi dwie rzeczy:

- **serwuje aplikację PWA**, którą na iPhonie instaluje się z Safari na ekranie
  głównym, bez App Store i bez Xcode;
- **przekazuje żądania do płytek** (reverse proxy). Firmware nie wysyła nagłówków
  CORS, więc przeglądarka nie może rozmawiać z płytką bezpośrednio. Serwer stoi
  pośrodku i firmware zostaje bez zmian.

Całość to jedna binarka w Go ze standardową biblioteką i aplikacją wbudowaną przez
`embed`. Obraz Dockera ma około 10 MB.

```
iPhone (PWA) ──HTTP──► posejdon :8080 ──HTTP──► ESP32 fontanna  (LAN)
                         │                  └─► kolejne płytki
                         └─ /api/devices/{id}/...
```

## Uruchomienie

```sh
cp devices.example.json devices.json    # wpisz IP płytki
docker compose up -d --build            # albo: podman compose up -d --build
```

Aplikacja jest pod `http://<host>:8080`.

### devices.json

```json
[
  { "id": "fontanna", "name": "Fontanna", "type": "pump", "url": "http://192.168.1.42" }
]
```

- `id` to identyfikator w URL, dozwolone są `a-z`, `0-9`, `-` i `_`.
- `type` wybiera ekran w PWA. Na razie jedyny typ to `pump`.
- `url` to adres płytki. **Wpisz IP, nie `waterpump.local`**, bo mDNS nie działa
  wewnątrz kontenera. Warto ustawić płytce stały adres (rezerwacja DHCP w routerze).

Po zmianie pliku uruchom serwer ponownie: `docker compose restart`.

## Dostęp z telefonu: Tailscale

Tailscale działa tu jako zwykły VPN i nie trzeba go konfigurować w projekcie.

1. Zainstaluj Tailscale na maszynie z serwerem i na iPhonie. Na obu zaloguj się
   tym samym kontem.
2. Na iPhonie otwórz w Safari `http://<nazwa-hosta>:8080` albo
   `http://100.x.x.x:8080` (adres widać w aplikacji Tailscale).
3. Wybierz Udostępnij → **Do ekranu początkowego**. Aplikacja startuje wtedy
   pełnoekranowo, z własną ikoną.

Działa to także poza domem. Serwer musi stać w tej samej sieci lokalnej co płytki;
same płytki Tailscale nie potrzebują.

**Opcjonalnie HTTPS.** Na hoście uruchom `tailscale serve --bg 8080`, a w panelu
Tailscale włącz *HTTPS Certificates*. Aplikacja jest wtedy pod
`https://<nazwa-hosta>.<tailnet>.ts.net`. Po HTTPS włącza się service worker, więc
aplikacja startuje z cache i ładuje się także bez sieci. Po zwykłym HTTP działa tak
samo, tylko bez cache.

**Nie ma autoryzacji.** Jest tak samo jak w firmware: każdy w LAN albo w tailnecie
może uruchomić pompę. Nie wystawiaj portu 8080 do internetu.

## API serwera

| Metoda | Ścieżka | Opis |
|---|---|---|
| `GET` | `/api/devices` | Lista płytek `{id, name, type}` bez adresów |
| `GET` | `/api/devices/{id}/status` | Proxy do `GET /api/status` na płytce |
| `GET`, `POST` | `/api/devices/{id}/config` | Proxy do `/api/config` |
| `POST` | `/api/devices/{id}/pump/on` | Proxy do `/api/pump/on`, body `{"seconds": N}` |
| `POST` | `/api/devices/{id}/pump/off` | Proxy do `/api/pump/off` |

Przepuszczane są tylko ścieżki z tej tabeli. Odpowiedzi płytki, także błędy 400 z
polem `error`, trafiają do aplikacji bez zmian. Gdy płytka nie odpowiada przez 5 s,
serwer zwraca `502 {"error": "Urządzenie … nie odpowiada"}`.

```sh
curl http://localhost:8080/api/devices/fontanna/status
curl -X POST http://localhost:8080/api/devices/fontanna/pump/on -d '{"seconds":10}'
```

## Rozwój

Do pracy nie trzeba instalować Go, wystarczy kontener:

```sh
podman run --rm -v "$PWD":/src:Z -w /src docker.io/library/golang:1.23 \
  sh -c 'gofmt -l . ; go vet ./... && go test ./...'
```

Bez płytki pod ręką możesz uruchomić jej atrapę. Odpowiada jak firmware, odlicza
czas lania i odrzuca wartości ponad limit bezpieczeństwa:

```sh
python3 scripts/fake_pump.py 8099
# devices.json: "url": "http://host.containers.internal:8099"   (podman)
#               "url": "http://host.docker.internal:8099"       (docker, + extra_hosts)
```

Struktura:

```
main.go                     konfiguracja z env (ADDR, DEVICES_FILE), start
internal/devices/           wczytywanie i walidacja devices.json
internal/server/            routing, proxy z allowlistą, pliki statyczne
web/                        PWA: index.html, app.js, style.css, manifest, sw.js, ikony
scripts/fake_pump.py        atrapa płytki
```

Po zmianie plików w `web/` trzeba przebudować obraz, bo są wbudowane w binarkę.
Nowy typ płytki to wpis w `allowedRoutes` w
[internal/server/server.go](internal/server/server.go) (jeśli ma inne endpointy) i
nowy ekran w `web/app.js`.
