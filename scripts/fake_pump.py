#!/usr/bin/env python3
"""Udaje firmware fontanny (water-pump-control), żeby klikać PWA bez płytki.

    python3 scripts/fake_pump.py [port]      # domyślnie 8099

W devices.json ustaw wtedy "url": "http://127.0.0.1:8099" (albo IP hosta, gdy
serwer chodzi w kontenerze). Walidacja odzwierciedla najważniejsze reguły
firmware'u, łącznie z odrzuceniem run_seconds ponad limit bezpieczeństwa.
"""

import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

MAX_ON_SECONDS = 1800
MODES = ("interval", "on_demand", "presence")

lock = threading.Lock()
started = time.monotonic()
cfg = {
    "mode": "interval",
    "schedule_enabled": False,
    "interval_seconds": 3600,
    "run_seconds": 1200,
    "manual_run_seconds": 30,
    "presence_cooldown_seconds": 1800,
    "presence_max_gate": 8,
    "presence_sensitivity": 50,
    "presence_no_one_seconds": 5,
}
run = {"until": 0.0, "total": 0, "cycles": 0, "source": "manual", "started": 0.0}
# Zakończone cykle, najnowsze pierwsze - jak /api/history w firmware. Kilka
# wymyślonych na start, żeby lista w PWA nie była pusta.
history = [
    {"source": "schedule", "at": started - 900, "seconds": 1200, "seconds_requested": 1200, "stopped_early": False},
    {"source": "presence", "at": started - 4000, "seconds": 30, "seconds_requested": 30, "stopped_early": False},
    {"source": "manual", "at": started - 90000, "seconds": 12, "seconds_requested": 60, "stopped_early": True},
]
HISTORY_LEN = 10


def finish_run(now, stopped_early):
    """Zapisuje bieżący cykl do historii, jeśli jakiś trwał."""
    if run["total"] == 0 or run["started"] == 0:
        return
    ran = min(run["total"], int(now - run["started"] + 0.5))
    history.insert(0, {
        "source": run["source"], "at": run["started"], "seconds": ran,
        "seconds_requested": run["total"], "stopped_early": stopped_early,
    })
    del history[HISTORY_LEN:]
    run["started"] = 0.0


def next_run_in(now):
    if cfg["mode"] != "interval" and not cfg["schedule_enabled"]:
        return None
    interval = cfg["interval_seconds"]
    return int(interval - (now - started) % interval)


def status():
    now = time.monotonic()
    left = max(0, int(run["until"] - now + 0.999))
    if left == 0:
        finish_run(now, False)
    extra = {"source": run["source"]} if left > 0 else {}
    return {
        **extra,
        "next_run_in_s": next_run_in(now),
        "pumping": left > 0,
        "seconds_left": left,
        "seconds_total": run["total"] if left > 0 else 0,
        "cycle_count": run["cycles"],
        "uptime_s": int(now - started),
        "wifi_rssi": -58,
        # Co ~20 s ktoś "przychodzi" na 5 s, żeby było widać zmianę.
        "presence_detected": int(now) % 20 < 5,
        **cfg,
    }


def validate(new):
    for key, value in new.items():
        if key == "mode":
            if value not in MODES:
                return "mode must be interval, on_demand or presence"
        elif key == "schedule_enabled":
            if not isinstance(value, bool):
                return "schedule_enabled must be true or false"
        elif key not in cfg:
            continue
        elif not isinstance(value, (int, float)) or value < 0:
            return f"{key} must be a non-negative number"
    merged = {**cfg, **new}
    for key in ("run_seconds", "manual_run_seconds"):
        if not 1 <= merged[key] <= MAX_ON_SECONDS:
            return f"{key} exceeds the safety cap"
    if merged["interval_seconds"] < 60:
        return "interval_seconds must be at least 60"
    if merged["run_seconds"] >= merged["interval_seconds"]:
        return "run_seconds must be shorter than interval_seconds"
    if not 0 <= merged["presence_max_gate"] <= 8:
        return "presence_max_gate must be 0..8"
    if not 0 <= merged["presence_sensitivity"] <= 100:
        return "presence_sensitivity must be 0..100"
    return None


class Handler(BaseHTTPRequestHandler):
    def send(self, code, body):
        raw = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n == 0:
            return {}
        return json.loads(self.rfile.read(n))

    def do_GET(self):
        with lock:
            if self.path == "/api/status":
                return self.send(200, status())
            if self.path == "/api/config":
                return self.send(200, cfg)
            if self.path == "/api/history":
                status()  # domyka cykl, który właśnie się skończył
                now = time.monotonic()
                runs = [{k: v for k, v in r.items() if k != "at"} | {"started_ago_s": int(now - r["at"])}
                        for r in history]
                return self.send(200, {"runs": runs})
        self.send(404, {"ok": False, "error": "not found"})

    def do_POST(self):
        try:
            body = self.body()
        except json.JSONDecodeError:
            return self.send(400, {"ok": False, "error": "invalid JSON"})
        with lock:
            if self.path == "/api/pump/on":
                seconds = body.get("seconds", cfg["manual_run_seconds"])
                if not isinstance(seconds, int) or seconds < 0:
                    return self.send(400, {"ok": False, "error": "seconds must be a non-negative number"})
                if seconds > MAX_ON_SECONDS:
                    return self.send(400, {"ok": False, "error": "seconds exceeds the safety cap"})
                now = time.monotonic()
                if run["until"] > now:
                    finish_run(now, True)
                run.update(until=now + seconds, total=seconds, cycles=run["cycles"] + 1,
                           source="manual", started=now)
                return self.send(200, {"ok": True, "seconds": seconds})
            if self.path == "/api/pump/off":
                now = time.monotonic()
                if run["until"] > now:
                    finish_run(now, True)
                run["until"] = 0
                return self.send(200, {"ok": True})
            if self.path == "/api/config":
                err = validate(body)
                if err:
                    return self.send(400, {"ok": False, "error": err})
                cfg.update({k: v for k, v in body.items() if k in cfg})
                return self.send(200, {"ok": True, **cfg})
        self.send(404, {"ok": False, "error": "not found"})


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8099
    print(f"fake pump on http://0.0.0.0:{port}")
    ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
