# Parte A (estrutura e aspecto) — plano de implementação

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A interface passa a ter três separadores (Visão geral, Eventos, Definições), cartões de serviço compactos, espera/estabilidade como listas no painel do serviço, e 30 dias de eventos guardados num ficheiro próprio.

**Architecture:** O agente Go passa a guardar os eventos em `events.jsonl` (append por evento, aparado no arranque e uma vez por dia) com um mutex próprio, expõe `GET /api/events` e aceita alterações parciais em `POST /api/config`. A página continua a ser um único `index.html` embebido (HTML + CSS + JS sem dependências), agora com rotas por `#` e três `<section class="page">`.

**Tech Stack:** Go 1.27 (stdlib + `go.yaml.in/yaml/v3` + `x/crypto`, já existentes), HTML/CSS/JS sem framework, Node 24 + Chrome headless do Playwright (só para screenshots de verificação, fora do repo).

**Spec:** `docs/superpowers/specs/2026-09-28-ui-estrutura-design.md`

## Global Constraints

- Sem dependências novas: nem Go, nem JS, nem CDN. Tudo embebido no binário.
- Textos da interface em português de Portugal; comentários de código em inglês, no estilo dos que já existem.
- Escala de texto: só 11.5 / 12.5 / 13 / 15 / 18 px.
- Raios: 8 px controlos, 12 px cartões internos, 14 px cartões de secção (pílulas continuam a 999px).
- Números em `font-variant-numeric: tabular-nums`.
- Todas as animações atuais mantêm-se; `prefers-reduced-motion` continua a desligá-las.
- Eventos: 30 dias **e** no máximo 5000; `/api/status` leva os 50 mais recentes.
- Minutos disponíveis: 0, 1, 2, 3, 5, 10, 15, 20, 30, 45, 60 (a espera começa em 1). Intervalo: 10, 15, 30, 60, 120, 300 s.
- Validação: `go vet ./... && go test ./... && golangci-lint run ./...`; `shellcheck`/`shfmt -d` se tocar em scripts; `make e2e` no fim.
- O e2e e o `make start` partilham containers: nunca correr os dois ao mesmo tempo; `make stop` antes do `make e2e`.

## Review Focus

1. **`events.jsonl` sem permissão de escrita** (disco cheio, diretório só de leitura): o agente continua, o evento fica em memória e vai um erro para o log. Teste em Task 1.
2. **Espera/estabilidade com um valor fora da lista** (ex.: 7, vindo do ficheiro): o seletor mostra "7 min" selecionado e não o troca por outro. Verificação em Task 4.
3. **Cancelar a confirmação de "Automático"**: o botão de rádio volta a "Observação" e nada é enviado. Verificação em Task 3.
4. **Endereço desconhecido** (`#/xpto`, `#/definicoes/xpto`): abre a Visão geral ou as Definições sem erro na consola. Verificação em Task 3.
5. **Voltar à Visão geral depois de outro separador**: os fios da topologia desenham-se (a topologia esteve escondida, com tamanho zero). Verificação em Task 3.

---

## Ferramenta de verificação visual (usada nas Tasks 3–6)

Não fica no repositório. Criar uma vez em `$S` (o scratchpad da sessão: `/tmp/claude-1000/-home-miguelmariz-homelab/a12f28a6-b50e-45b4-ac55-bda493fabf2b/scratchpad`).

`$S/ui.mjs`:

```js
// node ui.mjs <hash> <width> <dark|light> <out.png> [expression]
// Logs in to the demo (admin/admin), opens #hash, screenshots the page and
// prints the value of expression (evaluated in the page) as JSON.
import {spawn} from 'node:child_process';
import {writeFileSync} from 'node:fs';
const [hash = '/', width = '1280', theme = 'dark', out = 'shot.png', expr] = process.argv.slice(2);
const base = 'http://localhost:18099';
const login = await fetch(base + '/login', {method: 'POST', redirect: 'manual',
  headers: {'Content-Type': 'application/x-www-form-urlencoded'}, body: 'username=admin&password=admin'});
const tok = /failover_session=([^;]+)/.exec(login.headers.get('set-cookie') || '')?.[1];
if (!tok) throw new Error('login falhou');
const bin = process.env.HOME + '/.cache/ms-playwright/chromium_headless_shell-1243/chrome-headless-shell-linux64/chrome-headless-shell';
const p = spawn(bin, ['--remote-debugging-port=9333', '--no-sandbox', 'about:blank']);
await new Promise(r => setTimeout(r, 1500));
const [t] = await (await fetch('http://127.0.0.1:9333/json')).json();
const ws = new WebSocket(t.webSocketDebuggerUrl); let id = 0; const pend = {};
ws.onmessage = e => { const m = JSON.parse(e.data); pend[m.id]?.(m.result); };
await new Promise(r => ws.onopen = r);
const send = (method, params = {}) => new Promise(r => { pend[++id] = r; ws.send(JSON.stringify({id, method, params})); });
await send('Network.enable');
await send('Emulation.setDeviceMetricsOverride', {width: +width, height: 900, deviceScaleFactor: 1, mobile: +width < 600});
await send('Emulation.setEmulatedMedia', {features: [{name: 'prefers-color-scheme', value: theme}]});
await send('Network.setCookie', {name: 'failover_session', value: tok, domain: 'localhost', path: '/'});
await send('Page.navigate', {url: base + '/#' + hash});
await new Promise(r => setTimeout(r, 4000));
const {contentSize} = await send('Page.getLayoutMetrics');
const {data} = await send('Page.captureScreenshot', {clip: {x: 0, y: 0, width: +width, height: Math.min(contentSize.height, 2400), scale: 1}});
writeFileSync(out, Buffer.from(data, 'base64'));
if (expr) {
  const {result} = await send('Runtime.evaluate', {expression: expr, returnByValue: true, awaitPromise: true});
  console.log(JSON.stringify(result.value ?? result.description));
}
p.kill();
process.exit(0);
```

Uso: `make start` (uma vez; reconstrói o agente com o HTML atual), depois `cd $S && node ui.mjs /eventos 1280 dark ev.png "document.title"`, e abrir o PNG com a ferramenta Read. Depois de mudar o HTML: `make stop && make start` para o demo apanhar o binário novo.

---

### Task 1: Eventos em `events.jsonl`

**Files:**
- Create: `internal/agent/events.go`
- Modify: `internal/agent/agent.go` (struct `State`, struct `Agent`, `NewAgent`, `Tick`, remover `event()` e `maxEvents`, `publish()`)
- Test: `internal/agent/events_test.go` (novo); `internal/agent/agent_test.go` (trocar `a.st.Events` por `a.events`)

**Interfaces:**
- Produces:
  - `func eventsPath(statePath string) string`
  - `func loadEvents(path string) ([]Event, error)` — `fs.ErrNotExist` quando não há ficheiro
  - `func (a *Agent) event(svc, msg string)` (mesma assinatura de hoje)
  - `func (a *Agent) trimEvents()`
  - campos `Agent.events []Event`, `Agent.evMu sync.Mutex`, `Agent.eventsPath string`, `Agent.trimmedOn string`
  - `func (a *Agent) eventsSnapshot() []Event` — cópia, do mais recente para o mais antigo (usada na Task 2)
  - constantes `maxEvents = 5000`, `eventMaxAge = 30 * 24 * time.Hour`, `statusEvents = 50`

- [ ] **Step 1: Trocar `a.st.Events` por `a.events` nos testes existentes**

```bash
sed -i 's/a\.st\.Events/a.events/g' internal/agent/agent_test.go
```

- [ ] **Step 2: Escrever os testes novos**

`internal/agent/events_test.go`:

```go
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func evLine(t time.Time, msg string) string {
	b, _ := json.Marshal(Event{T: t, Msg: msg})
	return string(b)
}

// Events survive a restart; a line cut short by a crash is skipped, and on
// start the file keeps only the last 30 days.
func TestEventsPersistAndTrim(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeLines(t, filepath.Join(dir, "events.jsonl"),
		evLine(now.Add(-40*24*time.Hour), "velho"),
		`{"t":"2026-09-2`, // cut short
		evLine(now.Add(-time.Hour), "recente"))
	a := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(a.events) != 1 || a.events[0].Msg != "recente" {
		t.Fatalf("eventos lidos: %+v", a.events)
	}
	a.event("", "novo")
	b, _ := loadEvents(a.eventsPath)
	if len(b) != 2 || b[1].Msg != "novo" {
		t.Fatalf("ficheiro depois de aparar e acrescentar: %+v", b)
	}
	again := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(again.events) != 2 {
		t.Fatalf("depois de reiniciar: %+v", again.events)
	}
}

// At most maxEvents stay, in memory and, after a trim, in the file.
func TestEventsCap(t *testing.T) {
	a, _ := setup(t)
	for range maxEvents + 3 {
		a.event("", "e")
	}
	if len(a.events) != maxEvents {
		t.Fatalf("em memória: %d", len(a.events))
	}
	a.trimEvents()
	if b, _ := loadEvents(a.eventsPath); len(b) != maxEvents {
		t.Fatalf("no ficheiro: %d", len(b))
	}
}

// The events of a state.json from before events.jsonl move to the new file once.
func TestEventsMigrate(t *testing.T) {
	dir := t.TempDir()
	old := `{"router_ok":true,"services":{},"events":[` + evLine(time.Now().Add(-time.Hour), "antigo") + `]}`
	writeLines(t, filepath.Join(dir, "state.json"), old)
	a := newTestAgent(t, dir, &fake{noPing: map[string]bool{}, down: map[string]bool{}})
	if len(a.events) != 1 || a.events[0].Msg != "antigo" {
		t.Fatalf("não migrou: %+v", a.events)
	}
	a.save()
	if b, _ := os.ReadFile(filepath.Join(dir, "state.json")); strings.Contains(string(b), `"events"`) {
		t.Fatalf("state.json ainda guarda eventos: %s", b)
	}
	if b, _ := loadEvents(a.eventsPath); len(b) != 1 {
		t.Fatalf("events.jsonl: %+v", b)
	}
}

// A file that cannot be written does not stop the agent: the event stays in memory.
func TestEventsUnwritable(t *testing.T) {
	a, _ := setup(t)
	a.eventsPath = filepath.Join(t.TempDir(), "nao-existe", "events.jsonl")
	a.event("vaultwarden", "sem disco")
	a.trimEvents()
	if !hasEvent(a, "sem disco") {
		t.Fatal("o evento perdeu-se")
	}
}

// The status carries only the most recent events.
func TestStatusEvents(t *testing.T) {
	a, _ := setup(t)
	for range statusEvents + 10 {
		a.event("", "x")
	}
	a.publish()
	var v struct{ Events []Event }
	if err := json.Unmarshal(*a.view.Load(), &v); err != nil || len(v.Events) != statusEvents {
		t.Fatalf("eventos no estado: %d (%v)", len(v.Events), err)
	}
}
```

- [ ] **Step 3: Correr e ver falhar**

Run: `go test ./internal/agent -run 'TestEvents|TestStatusEvents'`
Expected: FAIL a compilar (`a.events`, `loadEvents`, `maxEvents`… não existem / `maxEvents` redeclarado mais tarde).

- [ ] **Step 4: Implementar `events.go`**

`internal/agent/events.go`:

```go
package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Events live in events.jsonl next to state.json, one JSON line each, so a
// month of them is not rewritten with the state on every tick.
const (
	maxEvents    = 5000
	eventMaxAge  = 30 * 24 * time.Hour
	statusEvents = 50 // the most recent ones, sent with every status
)

func eventsPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), "events.jsonl")
}

// loadEvents reads the file; a line that does not parse (a write cut short) is skipped.
func loadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var evs []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil && !e.T.IsZero() {
			evs = append(evs, e)
		}
	}
	return evs, sc.Err()
}

// event records msg in the log, in memory and at the end of the file. A
// failed write only goes to the log: the agent must not stop over it.
func (a *Agent) event(svc, msg string) {
	slog.Info(msg, "svc", cmp.Or(svc, "global"))
	e := Event{T: a.now, Svc: svc, Msg: msg}
	a.evMu.Lock()
	a.events = append(a.events, e)
	if n := len(a.events); n > maxEvents {
		a.events = slices.Clone(a.events[n-maxEvents:])
	}
	a.evMu.Unlock()
	b, _ := json.Marshal(e)
	f, err := os.OpenFile(a.eventsPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		_, err = f.Write(append(b, '\n'))
		err = cmp.Or(err, f.Close())
	}
	if err != nil {
		slog.Error("guardar evento", "error", err)
	}
}

// trimEvents keeps the last 30 days, at most maxEvents, and rewrites the file
// with them. It runs at start and on the first tick of each day.
func (a *Agent) trimEvents() {
	a.trimmedOn = a.now.Format(time.DateOnly)
	cut := a.now.Add(-eventMaxAge)
	a.evMu.Lock()
	i := slices.IndexFunc(a.events, func(e Event) bool { return !e.T.Before(cut) })
	if i < 0 {
		i = len(a.events)
	}
	a.events = slices.Clone(a.events[max(i, len(a.events)-maxEvents):])
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range a.events {
		_ = enc.Encode(e)
	}
	a.evMu.Unlock()
	if err := writeAtomic(a.eventsPath, buf.Bytes()); err != nil {
		slog.Error("guardar eventos", "error", err)
	}
}

// eventsSnapshot is every event in memory, newest first. It takes only evMu,
// so the page gets it even while a tick holds mu through a compose up.
func (a *Agent) eventsSnapshot() []Event {
	a.evMu.Lock()
	evs := slices.Clone(a.events)
	a.evMu.Unlock()
	slices.Reverse(evs)
	return evs
}
```

- [ ] **Step 5: Ligar ao `agent.go`**

1. Em `type State`, trocar a linha `Events []Event \`json:"events"\`` por:

```go
	Events     []Event              `json:"events,omitempty"` // only read: moved to events.jsonl on start
```

2. Remover `const maxEvents = 200` e a função `event()` inteira de `agent.go` (passou para `events.go`).

3. Na struct `Agent`, junto a `statePath string`, acrescentar:

```go
	eventsPath string
	evMu       sync.Mutex // guards events, apart from mu so the page can read them mid-tick
	events     []Event    // oldest first
	trimmedOn  string     // the day trimEvents last ran, as 2006-01-02
```

4. Em `NewAgent`, logo a seguir ao bloco `if a.st.Services == nil { … }`, acrescentar:

```go
	a.eventsPath = eventsPath(statePath)
	evs, err := loadEvents(a.eventsPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		evs = a.st.Events // from before events.jsonl
	case err != nil:
		return nil, err
	}
	a.events, a.st.Events = evs, nil
	a.trimEvents()
```

5. Em `Tick`, antes de `a.save()`:

```go
	if a.now.Format(time.DateOnly) != a.trimmedOn {
		a.trimEvents()
	}
```

6. Em `publish()`, antes do `json.Marshal`:

```go
	a.evMu.Lock()
	recent := slices.Clone(a.events[max(0, len(a.events)-statusEvents):])
	a.evMu.Unlock()
```

e no fim da lista de valores trocar `a.st.Events,` por `recent,`.

- [ ] **Step 6: Correr os testes**

Run: `go vet ./... && go test ./...`
Expected: PASS (todos, incluindo os que usavam `a.st.Events`).

- [ ] **Step 7: Lint e commit**

```bash
golangci-lint run ./...
git add internal/agent/events.go internal/agent/events_test.go internal/agent/agent.go internal/agent/agent_test.go
git commit -m "Keep 30 days of events in events.jsonl

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: `GET /api/events` e `POST /api/config` parcial

**Files:**
- Modify: `internal/agent/web.go` (`Handler`, `postConfig`)
- Test: `internal/agent/web_test.go` (`TestUI` e um teste novo `TestConfigPartial`)

**Interfaces:**
- Consumes: `func (a *Agent) eventsSnapshot() []Event` (Task 1)
- Produces:
  - `GET /api/events` → `200`, `application/json`, `[]Event` do mais recente para o mais antigo; `401` sem sessão
  - `POST /api/config` com todos os campos opcionais: `{"mode"?: string, "check_interval_s"?: int, "services"?: [{"name": string, "wait_min"?: int, "stability_min"?: int}]}` → `204`; `400` inválido

- [ ] **Step 1: Testes**

Em `TestUI`, na linha do `anon`, acrescentar a verificação do endpoint novo:

```go
	if do(anon, "", "/", "") != http.StatusSeeOther || do(anon, "", "/api/status", "") != 401 || do(anon, "", "/api/events", "") != 401 || do(anon, "", "/login", "") != 200 {
```

e na tabela, a seguir a `{"", "/api/status", "", 200},` (a primeira):

```go
		{"", "/api/events", "", 200},
```

Novo teste no fim de `web_test.go`:

```go
// A partial config changes only what it names.
func TestConfigPartial(t *testing.T) {
	a, _ := setup(t)
	post := func(body string) int {
		t.Helper()
		w := httptest.NewRecorder()
		a.postConfig(w, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body)))
		return w.Code
	}
	iv, hp := a.cfg.CheckIntervalS, a.cfg.Services[1]
	if post(`{"mode":"observe"}`) != 204 || a.cfg.Mode != "observe" || a.cfg.CheckIntervalS != iv {
		t.Fatalf("só o modo: %s %d", a.cfg.Mode, a.cfg.CheckIntervalS)
	}
	if post(`{"services":[{"name":"vaultwarden","stability_min":7}]}`) != 204 {
		t.Fatal("só a estabilidade de um serviço foi recusada")
	}
	vw, _ := a.service("vaultwarden")
	if vw.StabilityMin != 7 || vw.WaitMin == 0 || a.cfg.Services[1] != hp || a.cfg.Mode != "observe" {
		t.Fatalf("mexeu no que não devia: %+v %+v", vw, a.cfg.Services[1])
	}
	if post(`{"services":[{"name":"vaultwarden","wait_min":0}]}`) != 400 {
		t.Fatal("aceitou espera 0")
	}
	if post(`{"services":[{"name":"nao-existe","wait_min":3}]}`) != 400 {
		t.Fatal("aceitou um serviço desconhecido")
	}
	if c, _ := LoadConfig(a.cfgPath); c.Mode != "observe" {
		t.Fatal("não gravou")
	}
}

// /api/events has every event, newest first.
func TestEventsAPI(t *testing.T) {
	a, _ := setup(t)
	a.event("", "primeiro")
	a.event("", "segundo")
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, withSession(a, httptest.NewRequest(http.MethodGet, "/api/events", nil)))
	var evs []Event
	if err := json.Unmarshal(w.Body.Bytes(), &evs); err != nil || len(evs) < 2 || evs[0].Msg != "segundo" {
		t.Fatalf("HTTP %d: %s", w.Code, w.Body)
	}
}

// withSession adds a live session cookie to r.
func withSession(a *Agent, r *http.Request) *http.Request {
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: a.sessions.create()})
	return r
}
```

Acrescentar `"encoding/json"` aos imports de `web_test.go`.

- [ ] **Step 2: Correr e ver falhar**

Run: `go test ./internal/agent -run 'TestUI|TestConfigPartial|TestEventsAPI'`
Expected: FAIL — `TestUI` e `TestEventsAPI`: `/api/events` com sessão dá 404; `TestConfigPartial`: "só o modo" dá 400 (o intervalo que falta passa a 0).

- [ ] **Step 3: Implementar**

Em `Handler()`, a seguir ao `GET /api/status`:

```go
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.eventsSnapshot())
	})
```

Substituir `postConfig` por:

```go
// postConfig applies what the request names and leaves the rest as it is.
func (a *Agent) postConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode           *string `json:"mode"`
		CheckIntervalS *int    `json:"check_interval_s"`
		Services       []struct {
			Name         string `json:"name"`
			WaitMin      *int   `json:"wait_min"`
			StabilityMin *int   `json:"stability_min"`
		} `json:"services"`
	}
	if !decode(w, r, &req) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	next := a.cfg
	next.Services = slices.Clone(a.cfg.Services)
	if req.Mode != nil {
		next.Mode = *req.Mode
	}
	if req.CheckIntervalS != nil {
		next.CheckIntervalS = *req.CheckIntervalS
	}
	for _, rs := range req.Services {
		i := slices.IndexFunc(next.Services, func(s Service) bool { return s.Name == rs.Name })
		if i < 0 {
			http.Error(w, "serviço desconhecido: "+rs.Name, http.StatusBadRequest)
			return
		}
		if rs.WaitMin != nil {
			next.Services[i].WaitMin = *rs.WaitMin
		}
		if rs.StabilityMin != nil {
			next.Services[i].StabilityMin = *rs.StabilityMin
		}
	}
	if err := next.validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := saveConfig(a.cfgPath, &next); err != nil {
		http.Error(w, "guardar configuração: "+err.Error(), http.StatusInternalServerError)
		return
	}
	a.cfg = next
	a.now = time.Now()
	a.done(w, "", "configuração alterada")
}
```

- [ ] **Step 4: Correr os testes**

Run: `go vet ./... && go test ./... && golangci-lint run ./...`
Expected: PASS, 0 issues.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/web.go internal/agent/web_test.go
git commit -m "GET /api/events and partial config changes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Esqueleto — separadores, rotas e página de Definições

**Files:**
- Modify: `internal/agent/web/index.html` (HTML do `<header>` e `<main>`; CSS do topo, `.lower`, `.rules`; JS: rotas, `settingsPanel` → `renderSettings`, remoção do formulário Regras)

**Interfaces:**
- Consumes: `POST /api/config` parcial (Task 2)
- Produces (para as Tasks 4–5):
  - `<section class="page" id="page-overview|page-events|page-settings">`
  - `function route()` — mostra a página do `location.hash`
  - `const currentTab = () => 'overview' | 'events' | 'settings'`
  - `#page-events` contém `<div class="card"><div class="ev-filters" id="evFilters"></div><ol class="timeline" id="events"></ol><div id="evMore"></div></div>` (preenchido na Task 5; nesta task a lista antiga `renderEvents` continua a pintar `#events`)
  - `function renderSettings()` pinta `#settingsBody`
  - `const INTERVALS = [10, 15, 30, 60, 120, 300]`

- [ ] **Step 1: Criar a ferramenta de verificação** (`$S/ui.mjs`, código na secção "Ferramenta de verificação visual" acima).

- [ ] **Step 2: Topo** — substituir o `<header class="top">…</header>` por:

```html
  <header class="top">
    <div class="brand"><span class="logo" data-icon="swap"></span><h1>Failover do homelab</h1></div>
    <nav class="tabs" aria-label="Secções">
      <a href="#/" data-tab="overview"><span data-icon="dashboard"></span>Visão geral</a>
      <a href="#/eventos" data-tab="events"><span data-icon="clock"></span>Eventos</a>
      <a href="#/definicoes" data-tab="settings"><span data-icon="cog"></span>Definições</a>
    </nav>
    <div class="top-meta">
      <span class="chip" id="modeChip"></span>
      <span class="live" id="live"><span class="dot"></span><span id="liveText">A ligar ao agente…</span></span>
      <a class="icon-btn" href="#/definicoes" aria-label="Definições" title="Definições" data-icon="cog"></a>
      <button class="icon-btn" data-logout aria-label="Sair" title="Sair (sai sozinho após 30 min sem atividade)" data-icon="logout"></button>
    </div>
  </header>
```

Em `renderTopology`, apagar a linha que escreve em `$('subtitle')` (o subtítulo deixa de existir) e a linha `const away = …` se só servir para isso.

- [ ] **Step 3: Páginas** — dentro de `<main>`, depois do `</header>`:
  - envolver os dois `.banner`, a `<section class="card" aria-label="Onde está cada serviço">`, o `.section-head` e o `#svcs` num `<section class="page" id="page-overview">…</section>`;
  - apagar todo o `<div class="lower">…</div>` (cartão Regras e cartão Eventos);
  - acrescentar:

```html
  <section class="page" id="page-events" hidden>
    <div class="card">
      <div class="card-head"><h2>Eventos</h2><span class="spacer"></span><span class="muted" id="evCount"></span></div>
      <div class="ev-filters" id="evFilters"></div>
      <ol class="timeline" id="events"></ol>
      <div id="evMore"></div>
    </div>
  </section>

  <section class="page" id="page-settings" hidden>
    <div class="settings">
      <nav class="toc" aria-label="Secções das definições">
        <a href="#/definicoes/geral" data-sub="geral">Geral</a>
        <a href="#/definicoes/dns" data-sub="dns">DNS</a>
        <a href="#/definicoes/conta" data-sub="conta">Conta</a>
      </nav>
      <div id="settingsBody"></div>
    </div>
  </section>
```

- [ ] **Step 4: CSS** — substituir o bloco `/* header */` (as regras `.top`, `.brand`, `.top-meta`) por:

```css
/* header: brand, tabs, then status and actions */
.top { display: flex; align-items: center; gap: 12px 20px; flex-wrap: wrap; }
.brand { display: flex; align-items: center; gap: 12px; }
.brand h1 { font-size: 15px; font-weight: 650; letter-spacing: -.01em; }
.tabs { display: flex; gap: 2px; }
.tabs a { display: inline-flex; align-items: center; gap: 6px; padding: 7px 12px; border-radius: 8px; color: var(--muted);
  text-decoration: none; font-weight: 500; transition: background-color .15s, color .15s; }
.tabs a:hover { color: var(--text); background: color-mix(in srgb, var(--text), transparent 92%); }
.tabs a[aria-current] { color: var(--text); background: color-mix(in srgb, var(--text), transparent 88%); }
.tabs .icon { display: none; }
.top-meta { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-left: auto; }
.page { display: grid; gap: 20px; }
/* settings: an index on the left, the sections on the right */
.settings { display: grid; grid-template-columns: 180px minmax(0, 1fr); gap: 20px; align-items: start; }
.toc { position: sticky; top: 16px; display: grid; gap: 2px; }
.toc a { padding: 7px 12px; border-radius: 8px; color: var(--muted); text-decoration: none; }
.toc a:hover { color: var(--text); background: color-mix(in srgb, var(--text), transparent 92%); }
.toc a[aria-current] { color: var(--text); background: color-mix(in srgb, var(--text), transparent 88%); }
#settingsBody { display: grid; gap: 16px; }
.set-sec { scroll-margin-top: 16px; }
.set-sec h2 { font-size: 15px; font-weight: 600; margin-bottom: 12px; }
select { font: inherit; color: inherit; padding: 5px 8px; border-radius: 8px; border: 1px solid var(--divider); background: var(--well);
  font-variant-numeric: tabular-nums; }
```

Manter `.logo` e `.live`. Apagar `.lower`, `table.rules`, `.rules th`, `.rules td`, `.rules td:first-child` e, no `@media (max-width: 420px)`, a regra `.rules input[type=number]`. No `@media (max-width: 860px)` acrescentar:

```css
  .tabs { position: fixed; left: 0; right: 0; bottom: 0; z-index: 5; justify-content: space-around;
    padding: 6px 8px calc(6px + env(safe-area-inset-bottom)); background: var(--paper); border-top: 1px solid var(--divider); }
  .tabs a { flex-direction: column; gap: 2px; padding: 6px 12px; font-size: 11.5px; }
  .tabs .icon { display: block; }
  main { padding-bottom: 88px; }
  .toasts { bottom: 84px; }
  .settings { grid-template-columns: minmax(0, 1fr); }
  .toc { display: none; }
```

- [ ] **Step 5: Rotas** — no JS, antes de `new ResizeObserver(…)`:

```js
// The tab is in the address (#/eventos, #/definicoes/dns), so a reload or a
// link from elsewhere opens it and the browser's back button moves between tabs.
const ROUTES = {'': 'overview', eventos: 'events', definicoes: 'settings'};
const hashParts = () => location.hash.replace(/^#\/?/, '').split('/');
const currentTab = () => ROUTES[hashParts()[0]] || 'overview';
function route() {
  const tab = currentTab(), sub = hashParts()[1] || 'geral';
  document.querySelectorAll('.page').forEach(p => { p.hidden = p.id !== `page-${tab}`; });
  document.querySelectorAll('[data-tab]').forEach(a => a.dataset.tab === tab ? a.setAttribute('aria-current', 'page') : a.removeAttribute('aria-current'));
  document.querySelectorAll('[data-sub]').forEach(a => tab === 'settings' && a.dataset.sub === sub ? a.setAttribute('aria-current', 'true') : a.removeAttribute('aria-current'));
  if (st) renderAll();
  if (tab === 'settings') $(`set-${sub}`)?.scrollIntoView({block: 'start'});
  if (tab === 'overview') requestAnimationFrame(() => st && renderWires()); // it had no size while hidden
}
addEventListener('hashchange', route);
route();
```

Em `renderWires`, logo no início: `if ($('page-overview').hidden) return;`.

Em `refresh()`, no primeiro estado recebido, voltar a chamar `route()` para o `#/definicoes/dns` de um link fazer scroll já com a página pintada (ver Step 8, onde a linha `if (first)` é reescrita).

- [ ] **Step 6: Definições como página** — substituir `settingsPanel()` por `renderSettings()` e chamá-la em `renderAll`:

```js
// Settings, as a page. Only values that rarely change go in the markup, so
// the 5 s refresh does not repaint it while the token is being typed.
const INTERVALS = [10, 15, 30, 60, 120, 300];
const secs = v => v % 60 ? `${v} s` : `${v / 60} min`;
function renderSettings() {
  const mode = pending.has('mode') ? pending.get('mode') : st.mode;
  const iv = pending.has('interval') ? pending.get('interval') : st.check_interval_s;
  const ivs = [...new Set([...INTERVALS, iv])].sort((a, b) => a - b);
  paint('settingsBody', `
    <section class="card set-sec" id="set-geral"><h2>Geral</h2>
      <fieldset class="seg" ${pending.has('mode') ? 'disabled' : ''}><legend>Modo</legend>
        <label><input type="radio" name="mode" value="observe" ${mode === 'observe' ? 'checked' : ''}><strong>Observação</strong><small>Só regista o que faria</small></label>
        <label><input type="radio" name="mode" value="auto" ${mode === 'auto' ? 'checked' : ''}><strong>Automático</strong><small>Faz failover e regresso sozinho</small></label>
      </fieldset>
      <label class="field">Verificar o servidor a cada <select id="interval" ${pending.has('interval') ? 'disabled' : ''}>
        ${ivs.map(v => `<option value="${v}" ${v === iv ? 'selected' : ''}>${secs(v)}</option>`).join('')}</select></label>
    </section>
    <section class="card set-sec" id="set-dns"><h2>DNS (Technitium)</h2>
      <p class="note-text">O agente aponta o nome de um serviço para o TNAS quando a cópia fica pronta, e repõe-no no regresso.</p>
      ${kv([['Zona', mono(st.dns_zone)], ['API', mono(st.dns_api_url)],
        ['Token', st.dns_token ? chip('Definido', 'var(--success)', 'xs') : chip('Em falta', 'var(--warning)', 'xs')]])}
      <form id="tokenForm"><label class="pw-field">${st.dns_token ? 'Substituir o token' : 'Token da API do Technitium'}
          <input type="password" id="dnsToken" autocomplete="off" required></label>
        <p class="bad-text" id="tokenErr" role="alert"></p>
        <div class="form-foot"><button type="submit" class="btn contained" id="tokenSave">Testar e gravar</button></div></form>
    </section>
    <section class="card set-sec" id="set-conta"><h2>Conta</h2>
      ${kv([['Utilizador', mono(st.user)]])}
      <div class="sec-btns"><button class="btn" data-pw>${icon('key')}Mudar password</button></div>
    </section>`);
}
```

Em `renderPanel`, tirar o ramo `panel.kind === 'settings' ? settingsPanel() :`. Apagar `$('settingsOpen').addEventListener(…)`. `renderAll` passa a:

```js
function renderAll() { renderGlobal(); renderTopology(); renderServices(); renderEvents(); renderSettings(); renderPanel(); }
```

- [ ] **Step 7: Modo e intervalo gravam ao mudar** — no `document.addEventListener('change', …)`, antes de `const sw = …`:

```js
  if (e.target.name === 'mode') {
    const mode = e.target.value;
    if (mode === 'auto' && !await confirmAction('Passar para automático?',
      'O agente passa a fazer failover e regresso sozinho, sem pedir confirmação.', 'Passar para automático')) {
      painted.settingsBody = null; // put the radio back on what the agent has
      renderSettings();
      return;
    }
    post('api/config', {mode}, mode === 'auto' ? 'Modo automático ligado' : 'Modo observação ligado', 'mode', mode);
    return;
  }
  if (e.target.id === 'interval') {
    const v = Number(e.target.value);
    post('api/config', {check_interval_s: v}, `Verificação a cada ${secs(v)}`, 'interval', v);
    return;
  }
```

- [ ] **Step 8: Tirar o formulário Regras do JS** — apagar `fillForm`, `formValue`, `markDirty`, `$('cfg').addEventListener('input', …)`, `$('cfg').addEventListener('submit', …)`, `$('reset').addEventListener(…)`, e em `refresh()` trocar `if (first) { $('maintMin').value = st.default_expiry_min || 60; fillForm(); }` por `if (first) { $('maintMin').value = st.default_expiry_min || 60; route(); }`.

- [ ] **Step 9: Verificar**

```bash
go test ./... && make start
cd $S
node ui.mjs / 1280 dark a1.png "document.querySelectorAll('#wires path').length"          # espera >= 4
node ui.mjs /definicoes 1280 dark a2.png "document.querySelector('[data-tab=settings]').getAttribute('aria-current')"   # "page"
node ui.mjs /xpto 1280 dark a3.png "[document.getElementById('page-overview').hidden, document.getElementById('page-events').hidden]"   # [false,true]
node ui.mjs /definicoes/xpto 420 light a4.png "document.getElementById('page-settings').hidden"   # false
node ui.mjs /eventos 1280 dark a5.png "(location.hash='#/', new Promise(r=>setTimeout(r,300))).then(()=>document.querySelectorAll('#wires path').length)"   # >= 4 (Review Focus 5)
```

Cancelar a confirmação (Review Focus 3):

```bash
node ui.mjs /definicoes 1280 dark a6.png "(async()=>{const r=document.querySelector('input[name=mode][value=auto]');r.click();await new Promise(x=>setTimeout(x,200));document.getElementById('confirm').close('cancel');await new Promise(x=>setTimeout(x,200));return document.querySelector('input[name=mode]:checked').value})()"   # "observe"
```

Ver `a1.png`…`a4.png` com a ferramenta Read: topo com três separadores, sem cartão Regras; Definições com índice (1280) e sem índice + barra de separadores em baixo (420).

- [ ] **Step 10: Commit**

```bash
make stop
git add internal/agent/web/index.html
git commit -m "Tabs, routes and a settings page; the rules card goes

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Cartões compactos, procura e listas de minutos

**Files:**
- Modify: `internal/agent/web/index.html` (`renderServices`, CSS `.grid`/`.svc`, `.section-head`, `svcPanel`, handler de `change`)

**Interfaces:**
- Consumes: `POST /api/config` parcial (Task 2); `route()`/`currentTab()` (Task 3)
- Produces: `const MINUTES = [0, 1, 2, 3, 5, 10, 15, 20, 30, 45, 60]`; `function minuteSelect(s, field, min)`; `<input id="svcSearch">`

- [ ] **Step 1: Cabeçalho da secção** — trocar `<div class="section-head"><h2>Serviços</h2><span class="muted" id="svcCount"></span></div>` por:

```html
  <div class="section-head"><h2>Serviços</h2><span class="muted" id="svcCount"></span>
    <input type="search" id="svcSearch" placeholder="Procurar serviço…" aria-label="Procurar serviço" hidden>
    <button class="btn contained" disabled title="Em breve">+ Adicionar serviço</button></div>
```

CSS: substituir `.section-head` e `.section-head h2`, trocar o `minmax(300px, 1fr)` de `.grid` por `minmax(210px, 1fr)` e `gap: 16px` por `12px`, e acrescentar o resto **a seguir a `.svc.alert`** (tem de vir depois da regra `.svc` original para a sobrepor):

```css
.section-head { display: flex; align-items: center; gap: 12px; margin: 4px 4px -4px; }
.section-head h2 { font-size: 15px; font-weight: 600; }
.section-head #svcSearch { margin-left: auto; width: min(240px, 40vw); padding: 6px 10px; border-radius: 8px; border: 1px solid var(--divider);
  background: var(--well); }
.section-head #svcSearch[hidden] + .btn { margin-left: auto; }
.svc { cursor: pointer; gap: 10px; padding: 14px; border-radius: 12px; transition: border-color .5s, background-color .15s; }
.svc:hover { background-color: color-mix(in srgb, var(--text), transparent 96%); }
.svc-top { display: flex; align-items: center; gap: 10px; min-width: 0; }
.svc-top h3 { font-size: 13px; font-weight: 600; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.svc-top .svc-icon { width: 30px; height: 30px; border-radius: 8px; }
.svc-top .svc-icon .icon { width: 18px; height: 18px; }
.svc-top .sdot { margin-left: auto; width: 8px; height: 8px; border-radius: 50%; flex: none; background: var(--accent); }
.svc-line { color: var(--muted); font-size: 12.5px; font-variant-numeric: tabular-nums; }
```

Apagar as regras que só os cartões grandes usavam: `.svc-head`, `.svc-title`, `.svc-id`, `.svc-id h3`, `.svc-id .mono`, `.meta`, `.meta div/dt/dd`, `.meta dd.warn`, `.svc-foot`. (Manter `.svc`, `.svc.alert`, `.svc-icon`, `.status`, `.bar`, `.snap` — o painel e o estado usam-nos.)

- [ ] **Step 2: `renderServices`** — substituir a função por:

```js
// Compact cards: what it is, where it runs, how the last checks went. Everything else is in its panel.
const lastMs = name => { const b = st.beats?.[name]; const l = b?.[b.length - 1]; return l?.s === 'up' ? ` · ${l.ms} ms` : ''; };
function renderServices() {
  const q = norm($('svcSearch').value.trim());
  $('svcSearch').hidden = st.services.length < 8;
  const shown = st.services.filter(s => !q || norm(s.name).includes(q) || norm(s.host).includes(q));
  paint('svcs', shown.map(s => {
    const S = look(s);
    return `<article class="card svc ${fine(s) ? '' : 'alert'}" id="svc-${esc(s.name)}" data-panel="svc" data-svc="${esc(s.name)}" style="--accent:${S.c}">
      <div class="svc-top"><span class="svc-icon">${icon(SVC_ICON[s.name])}</span>
        <h3><button class="svc-open" data-panel="svc" data-svc="${esc(s.name)}" aria-haspopup="dialog">${esc(s.name)}</button></h3>
        <span class="sdot" title="${esc(S.label)}"></span></div>
      ${fine(s) ? `<p class="svc-line">No servidor${lastMs(s.name)}</p>` : statusBlock(s)}
      <div class="beats" data-beats="${esc(s.name)}" role="img" aria-label="Últimas verificações"></div>
    </article>`;
  }).join('') || '<p class="empty">Nenhum serviço com esse nome.</p>');
  fillBeats();
  const away = st.services.filter(s => s.state !== 'NORMAL').length;
  $('svcCount').textContent = `${st.services.length}${away ? `, ${away} fora do normal` : ''}`;
}
```

Acrescentar junto de `esc`:

```js
// for search: no case, no accents
const norm = s => String(s ?? '').normalize('NFD').replace(/\p{M}/gu, '').toLowerCase();
```

E junto dos outros listeners: `$('svcSearch').addEventListener('input', renderServices);`

- [ ] **Step 3: Listas de minutos no painel** — acrescentar antes de `svcPanel`:

```js
// Wait and stability as lists; a value from the file that is not in the list still shows.
const MINUTES = [0, 1, 2, 3, 5, 10, 15, 20, 30, 45, 60];
function minuteSelect(s, field, min) {
  const key = `${field}:${s.name}`, v = pending.has(key) ? pending.get(key) : s[field];
  const opts = [...new Set([...MINUTES.filter(m => m >= min), v])].sort((a, b) => a - b);
  return `<select data-min="${field}" data-svc="${esc(s.name)}" ${pending.has(key) ? 'disabled' : ''}>
    ${opts.map(m => `<option value="${m}" ${m === v ? 'selected' : ''}>${m} min</option>`).join('')}</select>`;
}
```

Em `svcPanel`, na lista `kv([...])` da secção 'Configuração', trocar as duas linhas de espera e estabilidade por:

```js
        ['Espera antes do failover', minuteSelect(s, 'wait_min', 1)],
        ['Estabilidade antes do regresso', minuteSelect(s, 'stability_min', 0)],
```

No handler de `change`, antes do ramo `mode`:

```js
  const sel = e.target.closest('select[data-min]');
  if (sel) {
    const v = Number(sel.value), field = sel.dataset.min, name = sel.dataset.svc;
    post('api/config', {services: [{name, [field]: v}]}, 'Guardado', `${field}:${name}`, v);
    return;
  }
```

- [ ] **Step 4: Verificar**

Para testar o valor fora da lista (Review Focus 2), pôr no config do demo `wait_min: 7` num serviço: em `e2e/run.sh`, não mexer; em vez disso, com o demo a correr, usar a API:

```bash
make start
cd $S
node ui.mjs / 1280 dark b1.png "fetch('api/config',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({services:[{name:'vaultwarden',wait_min:7}]})}).then(r=>r.status)"   # 204
node ui.mjs / 1280 dark b2.png "(async()=>{document.querySelector('[data-panel=svc]').click();await new Promise(r=>setTimeout(r,500));const s=document.querySelector('select[data-min=wait_min]');return [s.value,[...s.options].map(o=>o.value)]})()"   # ["7",[...,"5","7","10",...]]
node ui.mjs / 1280 light b3.png
node ui.mjs / 420 dark b4.png
```

Ver `b2.png`…`b4.png`: cartões compactos (ícone, nome, ponto, uma linha, barras), sem botões nos cartões; o painel aberto com as duas listas.

- [ ] **Step 5: Commit**

```bash
make stop
git add internal/agent/web/index.html
git commit -m "Compact service cards with search; wait and stability as lists

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Separador Eventos com o histórico

**Files:**
- Modify: `internal/agent/web/index.html` (`renderEvents`, `evItem`, `svcPanel`, `openPanel`, `route`, CSS `.ev`)

**Interfaces:**
- Consumes: `GET /api/events` (Task 2); `#evFilters`, `#events`, `#evMore`, `#evCount`, `currentTab()` (Task 3); `norm` (Task 4)
- Produces: `let evAll` (null até carregar; depois `[]Event`, mais recente primeiro); `async function loadEvents()`

- [ ] **Step 1: Carregar e juntar** — substituir `let seen = null; function renderEvents() {…}` por:

```js
// Every event (30 days) is fetched once, when the tab or a service panel
// first needs it; after that the ones in each status are merged on top.
let evAll = null, evShown = 100, seen = null, evLoading = false;
const evKey = e => e.t + e.svc + e.msg;
async function loadEvents() {
  if (evAll || evLoading) return;
  evLoading = true;
  try {
    const r = toLogin(await fetch('api/events', {cache: 'no-store'}));
    if (r.ok) { evAll = await r.json(); if (st) renderAll(); }
  } finally { evLoading = false; }
}
function mergeEvents() {
  if (!evAll) return;
  const known = new Set(evAll.map(evKey));
  const add = (st.events || []).filter(e => !known.has(evKey(e))).reverse();
  if (add.length) evAll = [...add, ...evAll];
}
// newest first, from whichever list is loaded
const eventsNow = () => evAll || [...(st.events || [])].reverse();

const evWhen = t => {
  const d = new Date(t), today = new Date(), y = new Date(today - 864e5);
  const hm = d.toLocaleTimeString('pt-PT', {hour: '2-digit', minute: '2-digit'});
  if (d.toDateString() === today.toDateString()) return `hoje ${hm}`;
  if (d.toDateString() === y.toDateString()) return `ontem ${hm}`;
  return `${d.toLocaleDateString('pt-PT', {day: '2-digit', month: '2-digit'})} ${hm}`;
};

function renderEvents() {
  mergeEvents();
  if (currentTab() !== 'events') { seen = null; return; } // back on the tab, nothing flashes as new
  const f = $('evSvc')?.value ?? '', rawQ = $('evQuery')?.value ?? '', q = norm(rawQ.trim());
  const names = [...new Set([...st.services.map(s => s.name), ...eventsNow().map(e => e.svc).filter(Boolean)])];
  const opts = `<option value="">Todos os serviços</option><option value="-">Global</option>${names.map(n => `<option value="${esc(n)}">${esc(n)}</option>`).join('')}`;
  if (painted.evOpts !== opts) {
    painted.evOpts = opts;
    paint('evFilters', `<select id="evSvc" aria-label="Serviço">${opts}</select>
      <input type="search" id="evQuery" placeholder="Procurar nos eventos…" aria-label="Procurar nos eventos">`);
    $('evSvc').value = f;
    $('evQuery').value = rawQ;
  }
  const all = eventsNow();
  const match = all.filter(e => (!f || (f === '-' ? !e.svc : e.svc === f)) && (!q || norm(e.msg).includes(q) || norm(e.svc).includes(q)));
  const evs = match.slice(0, evShown);
  $('evCount').textContent = `${match.length} ${match.length === 1 ? 'evento' : 'eventos'} · 30 dias`;
  paint('events', evs.length ? evs.map(e => evItem(e, seen && !seen.has(evKey(e)) && !reduceMotion.matches)).join('')
    : `<li class="empty">${all.length ? 'Nenhum evento com esse filtro.' : 'Ainda sem eventos. As falhas, os failovers e os regressos aparecem aqui.'}</li>`);
  paint('evMore', match.length > evShown ? `<button class="btn" id="evMoreBtn">Carregar mais</button>` : '');
  seen = new Set(all.map(evKey));
}
```

Handlers (junto dos outros):

```js
document.addEventListener('input', e => {
  if (e.target.id === 'evQuery' || e.target.id === 'evSvc') { evShown = 100; renderEvents(); }
});
document.addEventListener('click', e => {
  if (e.target.id === 'evMoreBtn') { evShown += 100; renderEvents(); }
});
```

(Um `<select>` também dispara `input`.)

- [ ] **Step 2: Linha do evento em três colunas** — substituir `evItem` por:

```js
const evItem = (e, fresh = false, withSvc = true) => `<li class="ev ${fresh ? 'fresh' : ''} ${/^ERRO/.test(e.msg) ? 'err' : ''}" style="--c:${evTone(e.msg)}">
    <span class="ev-dot"></span>
    <time datetime="${esc(e.t)}" title="${esc(when(e.t))}">${esc(evWhen(e.t))}</time>
    ${withSvc ? `<span class="ev-svc">${esc(e.svc || 'global')}</span>` : ''}
    <p>${esc(cap(e.msg))}</p></li>`;
```

CSS — substituir a regra `.ev { … grid-template-columns: 14px 1fr … }` por:

```css
.ev { --c: var(--neutral); display: grid; grid-template-columns: 14px 96px 120px minmax(0, 1fr); gap: 12px; padding: 9px 4px; align-items: baseline; }
.ev time { color: var(--muted); font-variant-numeric: tabular-nums; font-size: 12.5px; white-space: nowrap; }
.ev-svc { color: var(--muted); font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ev.err p { color: var(--error); }
.sheet .ev { grid-template-columns: 14px 96px minmax(0, 1fr); }
.ev-filters { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
.ev-filters input { flex: 1; min-width: 180px; max-width: 320px; padding: 6px 10px; border-radius: 8px; border: 1px solid var(--divider); background: var(--well); }
#evMore { display: flex; justify-content: center; padding-top: 12px; }
#evMore:empty { display: none; }
.timeline { max-height: none; }
```

No `@media (max-width: 860px)`: `.ev { grid-template-columns: 14px minmax(0, 1fr); } .ev time, .ev-svc { grid-column: 2; }`.

- [ ] **Step 3: Painel do serviço usa o histórico** — em `svcPanel`, trocar `const events = (st.events || []).filter(e => e.svc === s.name).slice(-25).reverse();` por `const events = eventsNow().filter(e => e.svc === s.name).slice(0, 25);`. Em `openPanel`, acrescentar no início `if (kind === 'svc') loadEvents();`. Em `route()`, antes de `if (st) renderAll();`: `if (tab === 'events') loadEvents();`.

- [ ] **Step 4: Verificar**

```bash
make start
cd $S
node ui.mjs /eventos 1280 dark c1.png "[document.querySelectorAll('#events .ev').length, document.getElementById('evCount').textContent]"
node ui.mjs /eventos 1280 dark c2.png "(async()=>{const q=document.getElementById('evQuery');q.value='IMAGENS';q.dispatchEvent(new Event('input',{bubbles:true}));await new Promise(r=>setTimeout(r,100));return [...document.querySelectorAll('#events .ev p')].every(p=>/imagens/i.test(p.textContent))})()"   # true
node ui.mjs /eventos 420 light c3.png
```

Ver `c1.png` e `c3.png`: lista a toda a largura, colunas quando/serviço/mensagem, filtros no topo.

- [ ] **Step 5: Commit**

```bash
make stop
git add internal/agent/web/index.html
git commit -m "Events tab: 30 days, by service and search

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: Afinação do estilo, README e verificação final

**Files:**
- Modify: `internal/agent/web/index.html` (CSS), `README.md`

- [ ] **Step 1: Escala de texto** — no CSS, trocar: `font-size: 11px` → `11.5px`; `font-size: 12px` → `12.5px`; `font-size: 16px` → `15px`; `font-size: 17px` → `15px`. Confirmar:

```bash
grep -o "font-size: [0-9.]*px" internal/agent/web/index.html | sort -u
```

Expected: só `11.5px`, `12.5px`, `13px`, `15px`, `18px`.

- [ ] **Step 2: Raios e números** — em `:root`: `--radius-card: 14px;`, `--radius-md: 12px;`, `--radius: 8px;`. Acrescentar `font-variant-numeric: tabular-nums;` ao `body`. Confirmar à vista (Step 4) que botões, campos e cartões mantêm proporção.

- [ ] **Step 3: README** — na secção "Comportamento que o plano não fixava", acrescentar:

```markdown
- **Interface:** três separadores (Visão geral, Eventos, Definições), com o separador no endereço (`#/eventos`, `#/definicoes/dns`). A espera e a estabilidade de cada serviço mudam-se no painel do serviço; o modo e o intervalo em Definições → Geral (passar a automático pede confirmação).
- **Eventos:** em `state/events.jsonl`, uma linha por evento; ficam 30 dias, até 5000. Os de um `state.json` antigo passam para lá no arranque.
```

e trocar o bullet "**Interface:** sai sozinha após 30 min sem atividade." por "**Sessão:** sai sozinha após 30 min sem atividade."

- [ ] **Step 4: Verificação completa**

```bash
go vet ./... && go test ./... && golangci-lint run ./...
make start
cd $S
for h in / /eventos /definicoes; do for w in 1280 420; do for t in dark light; do node ui.mjs "$h" $w $t "f-$(echo $h|tr -d /)-$w-$t.png"; done; done; done
```

Ver os 12 PNG com a ferramenta Read: topologia com fios e pacotes, cartões compactos, eventos em colunas, definições com índice (1280) e barra de separadores em baixo (420), claro e escuro.

```bash
make stop && timeout 590 make e2e
```

Expected: `E2E OK`.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/web/index.html README.md
git commit -m "One type scale, radii and tabular numbers; README

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```
