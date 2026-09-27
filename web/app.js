'use strict';

// Firmware nie wypycha zmian, więc status odpytujemy - tylko gdy aplikacja jest
// na ekranie, żeby telefon nie walił w sieć w tle.
const POLL_MS = 2000;
const REQUEST_TIMEOUT_MS = 8000;
const DEVICE_KEY = 'posejdon.device';

const MODE_LABELS = {
  interval: 'Interwałowy',
  on_demand: 'Na żądanie',
  presence: 'Czujnik obecności',
};

const SOURCE_LABELS = {
  schedule: 'harmonogram',
  manual: 'ręcznie',
  presence: 'czujnik',
};

const $ = (id) => document.getElementById(id);

const state = {
  devices: [],
  deviceId: null,
  status: null, // null = nieznany
  busy: false,
  // Suwak podąża za manual_run_seconds z urządzenia, dopóki użytkownik go nie ruszy.
  sliderTouched: false,
  // Ostatnie zakończone cykle, najnowsze pierwsze, z godziną startu jako Date.
  history: [],
  // Historię pobieramy tylko, gdy ten klucz się zmieni - czyli gdy cykl
  // ruszył albo się skończył - a nie przy każdym odpytaniu statusu.
  historyKey: null,
};

let pollTimer = null;
let pollSeq = 0;

// ---------- API ----------

class ApiError extends Error {}

async function api(path, { method = 'GET', body } = {}) {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), REQUEST_TIMEOUT_MS);
  let resp;
  try {
    resp = await fetch(path, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
      signal: ctrl.signal,
      cache: 'no-store',
    });
  } catch {
    throw new ApiError('Brak połączenia z serwerem');
  } finally {
    clearTimeout(timer);
  }

  let data = {};
  try {
    data = await resp.json();
  } catch {
    // Pusta albo nie-JSON odpowiedź - poniżej wystarczy kod HTTP.
  }
  if (!resp.ok) {
    // Serwer i firmware zgłaszają błąd w tym samym polu `error`.
    throw new ApiError(data.error || `HTTP ${resp.status}`);
  }
  return data;
}

const devicePath = (path) => `api/devices/${encodeURIComponent(state.deviceId)}/${path}`;

// ---------- Formatowanie (jak w kliencie Flutter) ----------

// To samo co pump_schedule_active() w firmware: tryb interwałowy zawsze leje
// z harmonogramu, pozostałe tylko z włączonym schedule_enabled.
const scheduleActive = (cfg) => cfg.mode === 'interval' || !!cfg.schedule_enabled;

function formatUptime(seconds) {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0) return `${hours}h ${minutes}min`;
  return `${minutes}min`;
}

function formatInterval(seconds) {
  if (seconds >= 3600 && seconds % 3600 === 0) return `${seconds / 3600} h`;
  if (seconds >= 60) return `${Math.floor(seconds / 60)} min`;
  return `${seconds} s`;
}

// Płytka nie ma zegara, więc podaje czasy względem "teraz"; zamieniamy je na
// godziny zegarem telefonu.
function formatClock(date) {
  const time = date.toLocaleTimeString('pl-PL', { hour: '2-digit', minute: '2-digit' });
  const today = new Date();
  const yesterday = new Date(today);
  yesterday.setDate(today.getDate() - 1);
  if (date.toDateString() === today.toDateString()) return time;
  if (date.toDateString() === yesterday.toDateString()) return `wczoraj ${time}`;
  const day = date.toLocaleDateString('pl-PL', { day: '2-digit', month: '2-digit' });
  return `${day} ${time}`;
}

function formatDuration(seconds) {
  if (seconds < 60) return `${seconds} s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  return s ? `${m} min ${s} s` : `${m} min`;
}

function formatLeft(seconds) {
  if (seconds < 60) return `${seconds} s`;
  const m = Math.floor(seconds / 60);
  const s = String(seconds % 60).padStart(2, '0');
  return `${m}:${s}`;
}

// ---------- Render ----------

function renderStatus() {
  const s = state.status;
  const dot = $('status-dot');
  dot.className = 'dot' + (s ? (s.pumping ? ' pumping' : ' idle') : '');

  let summary = 'Nieznany';
  if (s) {
    const why = SOURCE_LABELS[s.source] ? ` (${SOURCE_LABELS[s.source]})` : '';
    summary = s.pumping
      ? (s.seconds_left > 0 ? `Leje${why} – zostało ${formatLeft(s.seconds_left)}` : `Leje${why}`)
      : 'Bezczynna';
  }
  $('status-summary').textContent = summary;

  const progress = $('status-progress');
  const showProgress = !!(s && s.pumping && s.seconds_total > 0);
  progress.hidden = !showProgress;
  if (showProgress) progress.value = (s.seconds_total - s.seconds_left) / s.seconds_total;

  $('status-details').hidden = !s;
  if (s) {
    $('d-mode').textContent = MODE_LABELS[s.mode] || s.mode;
    const hasNext = typeof s.next_run_in_s === 'number';
    $('d-next-row').hidden = !hasNext;
    if (hasNext) {
      const at = new Date(Date.now() + s.next_run_in_s * 1000);
      const left = s.next_run_in_s < 60 ? 'za chwilę' : `za ${formatInterval(s.next_run_in_s)}`;
      $('d-next').textContent = `${formatClock(at)} (${left})`;
    }
    const scheduled = scheduleActive(s);
    $('d-interval-row').hidden = !scheduled;
    $('d-run-row').hidden = !scheduled;
    $('d-interval').textContent = formatInterval(s.interval_seconds);
    $('d-run').textContent = `${s.run_seconds} s`;
    $('d-presence-row').hidden = s.mode !== 'presence';
    $('d-presence').textContent = s.presence_detected ? 'Wykryto' : 'Brak';
    $('d-cycles').textContent = String(s.cycle_count);
    $('d-uptime').textContent = formatUptime(s.uptime_s);
    $('d-rssi-row').hidden = !s.wifi_rssi;
    $('d-rssi').textContent = `${s.wifi_rssi} dBm`;

    if (!state.sliderTouched && !state.busy && s.manual_run_seconds > 0) {
      setSlider(s.manual_run_seconds);
    }
  }
  renderControls();
}

function renderHistory() {
  const list = $('history-list');
  list.replaceChildren(...state.history.map((run) => {
    const li = document.createElement('li');
    const time = document.createElement('time');
    time.dateTime = run.started.toISOString();
    time.textContent = formatClock(run.started);
    const meta = document.createElement('span');
    meta.className = 'meta';
    const parts = [SOURCE_LABELS[run.source] || run.source, formatDuration(run.seconds)];
    if (run.stopped_early) parts.push(`przerwane z ${formatDuration(run.seconds_requested)}`);
    meta.textContent = parts.join(' · ');
    li.append(time, meta);
    return li;
  }));
  $('history-empty').hidden = state.history.length > 0;
}

async function loadHistory() {
  const deviceId = state.deviceId;
  let data;
  try {
    data = await api(devicePath('history'));
  } catch {
    // Starsza płytka bez /api/history albo chwilowy błąd - spróbujemy przy
    // następnej zmianie cyklu.
    state.historyKey = null;
    return;
  }
  if (deviceId !== state.deviceId) return;
  const now = Date.now();
  state.history = (data.runs || []).map((run) => ({
    ...run,
    started: new Date(now - run.started_ago_s * 1000),
  }));
  renderHistory();
}

function renderControls() {
  $('pour-btn').disabled = state.busy;
  $('duration').disabled = state.busy;
  $('stop-btn').disabled = state.busy || !(state.status && state.status.pumping);
}

function setSlider(seconds) {
  const v = Math.min(120, Math.max(1, Math.round(seconds)));
  $('duration').value = v;
  $('duration-value').textContent = `${v} s`;
}

let toastTimer = null;
function toast(message) {
  const el = $('toast');
  el.textContent = message;
  el.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.remove('show'), 3500);
}

// ---------- Polling ----------

async function poll() {
  clearTimeout(pollTimer);
  if (!state.deviceId) return;
  const seq = ++pollSeq;

  let status = null;
  try {
    status = await api(devicePath('status'));
  } catch {
    // Chwilowo nieosiągalne: pokazujemy "nieznany" zamiast starych danych,
    // ale nie przerywamy odpytywania - następna próba może się udać.
  }
  // Nowsze odpytanie (albo zmiana urządzenia) już ruszyło - ten wynik jest nieaktualny.
  if (seq !== pollSeq) return;

  state.status = status;
  renderStatus();
  if (status) {
    const key = `${status.cycle_count}:${status.pumping}`;
    if (key !== state.historyKey) {
      state.historyKey = key;
      loadHistory();
    }
  }
  if (document.visibilityState === 'visible') {
    pollTimer = setTimeout(poll, POLL_MS);
  }
}

function stopPolling() {
  clearTimeout(pollTimer);
  pollSeq++;
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible') poll();
  else stopPolling();
});

// ---------- Akcje ----------

// Blokuje przyciski na czas wywołania i pokazuje błąd, zamiast zostawiać
// ekran w stanie "kliknąłem i nic".
async function run(action) {
  if (state.busy) return;
  state.busy = true;
  renderControls();
  try {
    await action();
  } catch (e) {
    toast(e.message);
  } finally {
    state.busy = false;
    renderControls();
    poll();
  }
}

$('duration').addEventListener('input', (e) => {
  state.sliderTouched = true;
  $('duration-value').textContent = `${e.target.value} s`;
});

$('pour-btn').addEventListener('click', () =>
  run(() => api(devicePath('pump/on'), {
    method: 'POST',
    body: { seconds: Number($('duration').value) },
  })));

$('stop-btn').addEventListener('click', () =>
  run(() => api(devicePath('pump/off'), { method: 'POST' })));

// ---------- Urządzenia ----------

function selectDevice(id) {
  const device = state.devices.find((d) => d.id === id) || state.devices[0];
  state.deviceId = device.id;
  state.status = null;
  state.sliderTouched = false;
  state.history = [];
  state.historyKey = null;
  renderHistory();
  try { localStorage.setItem(DEVICE_KEY, device.id); } catch { /* prywatny tryb */ }

  $('title').textContent = device.name;
  document.title = `${device.name} – Posejdon`;
  $('device-select').value = device.id;
  $('settings-btn').disabled = false;
  $('status-summary').textContent = 'Łączenie…';
  poll();
}

async function loadDevices() {
  let all;
  try {
    all = await api('api/devices');
  } catch (e) {
    $('status-summary').textContent = 'Nieznany';
    toast(`${e.message}. Ponawiam…`);
    setTimeout(loadDevices, 5000);
    return;
  }

  // Na razie PWA umie tylko fontanny. Płytki innych typów dojdą z własnymi ekranami.
  state.devices = all.filter((d) => d.type === 'pump');
  if (state.devices.length === 0) {
    $('empty').hidden = false;
    $('pump').hidden = true;
    return;
  }

  const select = $('device-select');
  select.replaceChildren(...state.devices.map((d) => new Option(d.name, d.id)));
  select.hidden = state.devices.length < 2;
  $('pump').hidden = false;

  let saved = null;
  try { saved = localStorage.getItem(DEVICE_KEY); } catch { /* prywatny tryb */ }
  selectDevice(saved);
}

$('device-select').addEventListener('change', (e) => selectDevice(e.target.value));

// ---------- Ustawienia ----------

const form = $('settings-form');
const dialog = $('settings');

// Pola formularza ↔ pola API. Interwał i odstęp czujnika pokazujemy w minutach,
// bo w sekundach to nieczytelne liczby.
const FIELDS = [
  ['interval_minutes', 'interval_seconds', 60],
  ['run_seconds', 'run_seconds', 1],
  ['manual_run_seconds', 'manual_run_seconds', 1],
  ['presence_cooldown_minutes', 'presence_cooldown_seconds', 60],
  ['presence_max_gate', 'presence_max_gate', 1],
  ['presence_sensitivity', 'presence_sensitivity', 1],
  ['presence_no_one_seconds', 'presence_no_one_seconds', 1],
];

function settingsError(message) {
  const el = $('settings-error');
  el.textContent = message || '';
  el.hidden = !message;
}

function syncModeFields() {
  const mode = form.elements.mode.value;
  $('presence-fields').hidden = mode !== 'presence';
  $('schedule-toggle').hidden = mode === 'interval';
  $('schedule-fields').hidden = !scheduleActive({
    mode,
    schedule_enabled: form.elements.schedule_enabled.checked,
  });
}

function setFormEnabled(enabled) {
  for (const el of form.elements) {
    if (el.id !== 'settings-close') el.disabled = !enabled;
  }
}

async function openSettings() {
  settingsError(null);
  setFormEnabled(false);
  dialog.showModal();
  try {
    const cfg = await api(devicePath('config'));
    form.elements.mode.value = cfg.mode;
    form.elements.schedule_enabled.checked = !!cfg.schedule_enabled;
    for (const [name, key, scale] of FIELDS) {
      form.elements[name].value = Math.floor(cfg[key] / scale);
    }
    syncModeFields();
    setFormEnabled(true);
  } catch (e) {
    settingsError(e.message);
  }
}

$('settings-btn').addEventListener('click', openSettings);
$('settings-close').addEventListener('click', () => dialog.close());
form.elements.mode.addEventListener('change', syncModeFields);
form.elements.schedule_enabled.addEventListener('change', syncModeFields);

form.addEventListener('submit', async (e) => {
  e.preventDefault();
  settingsError(null);

  const body = {
    mode: form.elements.mode.value,
    schedule_enabled: form.elements.schedule_enabled.checked,
  };
  for (const [name, key, scale] of FIELDS) {
    const raw = form.elements[name].value.trim();
    if (!/^\d+$/.test(raw)) {
      settingsError('Wszystkie pola muszą być liczbami.');
      return;
    }
    body[key] = Number(raw) * scale;
  }

  setFormEnabled(false);
  try {
    await api(devicePath('config'), { method: 'POST', body });
    dialog.close();
    toast('Ustawienia zapisane na urządzeniu.');
    state.sliderTouched = false;
    poll();
  } catch (err) {
    // Firmware odrzuca np. czas pracy ponad limit bezpieczeństwa - jego
    // komunikat jest konkretniejszy niż cokolwiek, co dopisalibyśmy tutaj.
    settingsError(err.message);
  } finally {
    setFormEnabled(true);
  }
});

// ---------- Start ----------

// Service worker wymaga HTTPS (albo localhost). Po zwykłym HTTP w LAN/Tailscale
// aplikacja działa tak samo, tylko bez cache offline.
if ('serviceWorker' in navigator && window.isSecureContext) {
  navigator.serviceWorker.register('sw.js').catch(() => {});
}

loadDevices();
