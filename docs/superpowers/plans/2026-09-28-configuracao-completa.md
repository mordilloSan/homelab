# Parte B (configuração completa) — plano de implementação

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Todas as definições do `failover.yml` (menos os serviços) mudam-se em Definições, secção a secção, e a interface passa a ser sempre HTTPS com um certificado gerado e renovado pelo agente.

**Architecture:** Uma tabela `settings` em Go liga cada chave do `failover.yml` à sua secção, ao campo do `Config` e às marcas "bloqueada"/"segredo"; dela saem o `POST /api/config/section`, o `settings` do estado e as verificações. `validate()` passa a dizer qual o campo errado (`FieldError`). O certificado e o redirecionamento HTTP→HTTPS na mesma porta seguem o LinuxIO (stdlib). A página desenha um formulário por secção a partir de uma tabela JS com os rótulos.

**Tech Stack:** Go 1.27 stdlib (crypto/ecdsa, crypto/x509, net), yaml.v3 já existente; HTML/JS sem dependências.

**Spec:** `docs/superpowers/specs/2026-09-28-configuracao-completa-design.md`

## Global Constraints

- Sem dependências novas; sem `InsecureSkipVerify` novo (o de `sys.go` é o verificador de serviços, pré-existente e intencional).
- Chaves = as do `failover.yml` (`server.ip`, `dns.ttl`, …); ids de secção: `rede`, `verificacao`, `dns`, `kuma`, `caminhos`, `noturna`, `interface`.
- Segredos (`kuma.heartbeat_token`, `kuma.npm_token`) nunca saem do agente; vazio mantém.
- Bloqueadas (`rede`, `caminhos`): só mudam com todos os serviços em NORMAL e `tnas_npm.snapshot` vazio.
- Certificado: `<dir do estado>/certificates/0-self-signed.cert|key`, ECDSA P-256, 395 dias, renovado a ≤30 dias ou quando `tnas_ip` não está nele.
- Textos em PT-PT; comentários em inglês.
- Validação por tarefa: `go vet ./... && go test ./... && golangci-lint run ./...`; `shellcheck e2e/run.sh && shfmt -d e2e/run.sh` quando tocar no script; `make e2e` no fim (nunca com `make start` a correr).

## Review Focus

1. **Ficheiro do certificado estragado ou com dono errado** (chave sem certificado, lixo no PEM): o agente gera outro e arranca; nunca fica sem interface. Teste na Task 2.
2. **Mudar `tnas_ip` pela interface**: o certificado seguinte passa a incluir o IP novo sem reiniciar. Teste na Task 2.
3. **Um pedido HTTP lento ou que nunca manda nada** na porta da interface não bloqueia os outros (classificação por ligação, com prazo). Teste na Task 2.
4. **Guardar uma secção com um campo de outra secção ou desconhecido**: 400 com o campo, nada gravado. Teste na Task 3.
5. **Secção com alterações por gravar durante o refresh de 5 s**: o que foi escrito não desaparece. Verificação na Task 5.

---

### Task 1: `FieldError` e as regras novas de validação

**Files:** Modify `internal/agent/agent.go` (`Config.UI`, `validate`, `LoadConfig`). Test: `internal/agent/config_test.go` (novo).

**Interfaces — Produces:** `type FieldError struct{ Field, Msg string }` (`Error() string` = `Field + ": " + Msg`); `validate()` devolve `*FieldError` nos erros de campo; `LoadConfig` lê `maintenance.default_expiry_min: 0` como 60 e apaga `ui.tls_cert`/`ui.tls_key`.

- [ ] **Step 1: Teste** — `config_test.go`: tabela que parte da config de `config/failover.yml`, aplica uma mutação e espera o campo:

```go
func TestValidateFields(t *testing.T) {
	base, err := LoadConfig("config/failover.yml")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		field string
		set   func(*Config)
	}{
		{"server.ip", func(c *Config) { c.Server.IP = "192.168.1" }},
		{"tnas_ip", func(c *Config) { c.TNASIP = "tnas" }},
		{"router_ip", func(c *Config) { c.RouterIP = "" }},
		{"server.npm_check_host", func(c *Config) { c.Server.NPMCheckHost = "https://nginx.engmariz.com" }},
		{"lan_iface", func(c *Config) { c.LANIface = "ovs eth0" }},
		{"dns.api_url", func(c *Config) { c.DNS.APIURL = "127.0.0.1:5380" }},
		{"dns.zone", func(c *Config) { c.DNS.Zone = "eng mariz" }},
		{"dns.ttl", func(c *Config) { c.DNS.TTL = 0 }},
		{"kuma.base_url", func(c *Config) { c.Kuma.BaseURL = "kuma:3001" }},
		{"paths.mirror_subvol", func(c *Config) { c.Paths.MirrorSubvol = "Volume1/ServerBackup" }},
		{"paths.mirror_root", func(c *Config) { c.Paths.MirrorRoot = "../x" }},
		{"paths.snapshots_dir", func(c *Config) { c.Paths.SnapshotsDir = "snaps" }},
		{"paths.overrides_dir", func(c *Config) { c.Paths.OverridesDir = "o" }},
		{"npm.dir", func(c *Config) { c.NPM.Dir = "/npm" }},
		{"maintenance.default_expiry_min", func(c *Config) { c.Maintenance.DefaultExpiryMin = 20000 }},
		{"ui.listen", func(c *Config) { c.UI.Listen = "8099" }},
		{"nightly.prepull_at", func(c *Config) { c.Nightly.PrepullAt = "25:00" }},
		{"start_timeout_min", func(c *Config) { c.StartTimeoutMin = 0 }},
	} {
		next := base
		c.set(&next)
		var fe *FieldError
		if err := next.validate(); !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: %v", c.field, err)
		}
	}
	if err := base.validate(); err != nil {
		t.Fatalf("a config por defeito não valida: %v", err)
	}
}

// An old file with ui.tls_cert/tls_key and a zero default expiry still loads.
func TestLoadOldUIAndExpiry(t *testing.T) {
	b, _ := os.ReadFile("config/failover.yml")
	s := strings.Replace(string(b), "default_expiry_min: 60", "default_expiry_min: 0", 1)
	s = strings.Replace(s, "ui:\n", "ui:\n  tls_cert: /x.pem\n  tls_key: /x.key\n", 1)
	p := filepath.Join(t.TempDir(), "f.yml")
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(p)
	if err != nil || c.Maintenance.DefaultExpiryMin != 60 {
		t.Fatalf("%v %d", err, c.Maintenance.DefaultExpiryMin)
	}
	if err := saveConfig(p, &c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); strings.Contains(string(b), "tls_") {
		t.Fatalf("tls_ ficou no ficheiro:\n%s", b)
	}
}
```

(Se o `failover.yml` de fábrica não tiver uma secção `ui:`, o `Replace` não faz nada; nesse caso acrescentar `ui:\n  listen: 0.0.0.0:8099\n` ao ficheiro de fábrica, que é o valor que o `main` já usa por defeito — confirmar em `config/failover.yml`.)

- [ ] **Step 2:** `go test ./internal/agent -run 'TestValidateFields|TestLoadOldUIAndExpiry'` → FAIL (`FieldError` não existe).
- [ ] **Step 3: Implementar** em `agent.go`:
  - `UI` passa a `Listen string`, `TLSCert string \`yaml:"tls_cert,omitempty"\``, `TLSKey string \`yaml:"tls_key,omitempty"\`` com o comentário "ignored: the UI makes its own certificate; read only so older files still load".
  - `FieldError` e `func fe(field, msg string) error { return &FieldError{field, msg} }`.
  - `validate()` reescrito como uma lista ordenada de verificações `if cond { return fe(campo, msg) }`, com as regras da spec, mantendo as atuais (mensagens em PT, ex. `"tem de ser um endereço IP"`). Helpers: `isIP(s)`, `isName(s)` (não vazio, sem espaço, `/`, `:`), `isURL(s)` (`url.Parse`, esquema http/https, `Host` não vazio), `isAbs(s)`, `isRel(s)` (não vazio, não absoluto, sem `..` depois de `filepath.Clean`), `isListen(s)` (`net.SplitHostPort` + porta 1–65535).
  - Serviços continuam com `validateService` (erros sem campo, a parte C trata disso).
  - `LoadConfig`, antes de `validate`: `c.Maintenance.DefaultExpiryMin = cmp.Or(c.Maintenance.DefaultExpiryMin, 60)`; depois de validar: `c.UI.TLSCert, c.UI.TLSKey = "", ""`.
- [ ] **Step 4:** `go vet ./... && go test ./...` → PASS (os testes existentes que usam `UI.TLSCert` passam para a Task 2; se algum falhar a compilar aqui, é porque ainda referencia `UITLS` — só o `main.go`, tratado na Task 2).
- [ ] **Step 5:** commit "Validation names the field; old TLS keys and a zero expiry still load".

### Task 2: Certificado do agente, HTTPS com redirecionamento, healthcheck

**Files:** Create `internal/agent/certificate.go`, `internal/agent/tlsredirect.go`, tests `certificate_test.go`, `tlsredirect_test.go`. Modify `internal/agent/web.go` (apagar `UITLS`; `UIURL` sempre https), `main.go` (`serve`, `healthcheck`), `web_test.go` (`TestHealthcheck`, `TestUIURL`).

**Interfaces — Produces:**
- `func CertFile(statePath string) string` → `<dir>/certificates/0-self-signed.cert`
- `type certStore struct{ dir string; ips func() []net.IP; mu sync.Mutex; cur *tls.Certificate; leaf *x509.Certificate }`
- `func (a *Agent) TLSConfig() (*tls.Config, error)` — carrega ou gera já (erro só se nem gerar conseguir), `GetCertificate` renova quando preciso
- `func (a *Agent) CertInfo() (names []string, notAfter time.Time)`
- `func NewRedirectListener(inner net.Listener, cfg *tls.Config) net.Listener`
- `Healthcheck(listen, certFile string)` (já existe, sem mudar)

- [ ] **Step 1: Testes** (`certificate_test.go`): 
  - `loadOrCreate(dir, now, ips)` sem ficheiros → gera; segunda chamada → mesmo número de série;
  - com `now` a 29 dias do fim → série nova;
  - chave escrita sem certificado, ou PEM com lixo → gera novo sem erro;
  - `ips` com um IP que o certificado não tem → série nova (Review Focus 2);
  - nomes incluem `localhost` e o IP `127.0.0.1`; `Healthcheck` contra um `httptest` servido com esse certificado e `CertFile` → nil.
  
  (`tlsredirect_test.go`):
  - um `net.Listen` em `127.0.0.1:0` embrulhado em `NewRedirectListener` e servido por `http.Server`: `http.Get("http://…/x?y=1")` sem seguir redirecionamentos → 301 `Location: https://127.0.0.1:<porta>/x?y=1`; um cliente TLS com o certificado como raiz → 200;
  - uma ligação TCP aberta que não manda nada não impede um segundo pedido de responder em menos de 1 s (Review Focus 3).
- [ ] **Step 2:** correr → FAIL (funções não existem).
- [ ] **Step 3: Implementar**
  - `certificate.go`: como `LinuxIO/backend/webserver/web/certificate.go`, reduzido: `loadOrCreate(dir string, now time.Time, ips []net.IP) (tls.Certificate, *x509.Certificate, error)` com `needsNew(leaf, now, ips)` (≤30 dias ou falta um IP de `ips`), geração ECDSA P-256 com `DNSNames: localhost, host, host.local` e `IPAddresses: 127.0.0.1, ::1, ips…, IPs globais das interfaces`, escrita com `writeAtomic` (chave 0600, certificado 0644 via `os.Chmod` depois), `slog.Warn` quando o par existente é ilegível. `certStore.get(hello)` sob `mu`: se `needsNew(leaf, time.Now(), ips())` → `loadOrCreate` de novo. `Agent.TLSConfig()` cria o `certStore` com `ips = func() []net.IP { a.mu.Lock(); defer a.mu.Unlock(); return []net.IP{net.ParseIP(a.cfg.TNASIP)} }` — atenção: `GetCertificate` corre durante handshakes, nunca com `a.mu` já tomado pelo mesmo goroutine; um tick longo atrasa handshakes — para evitar, guardar `tnasIP` num `atomic.Value` atualizado em `postSection`/`NewAgent` e ler daí em vez de `a.mu`.
  - `tlsredirect.go`: o `tlsRedirectListener` do LinuxIO (`LinuxIO/backend/webserver/web/tls_redirect.go`), com `Location` a partir de `req.Host` (sem porta configurada: o `Host` do pedido já a traz) e 301.
  - `web.go`: apagar `UITLS`; `UIURL(listen, host string)` sempre `https://`.
  - `main.go`: `serve` faz `srv.TLSConfig, err = a.TLSConfig()`; `ln = agent.NewRedirectListener(ln, srv.TLSConfig)`; `srv.Serve(ln)`. `healthcheck(cfgPath, statePath)` → `agent.Healthcheck(cfg.UI.Listen, agent.CertFile(statePath))`; `run` passa `*statePath`.
  - `TestUIURL` e `TestHealthcheck` ajustados (o caso HTTP simples desaparece do `Healthcheck`: a interface é sempre HTTPS; `Healthcheck(listen, "")` continua a fazer HTTP para quem o use, mas o `main` passa sempre o ficheiro).
- [ ] **Step 4:** `go vet ./... && go test ./... && golangci-lint run ./...` → PASS.
- [ ] **Step 5:** commit "The UI is always HTTPS, with a certificate the agent makes and renews".

### Task 3: API das secções, verificações e reinício

**Files:** Create `internal/agent/settings.go`, `settings_test.go`. Modify `web.go` (rotas), `agent.go` (`publish`: `settings`, `ui_listen_running`, `cert`; campo `restart func()`, `listening string`).

**Interfaces — Produces:**
- `type setting struct{ key, section string; locked, secret, num bool; str func(*Config) *string; int func(*Config) *int }`, `var settings []setting`
- `func (a *Agent) postSection(w, r)` — `POST /api/config/section`
- `func (a *Agent) postCheck(w, r)` — `POST /api/config/check` → `[]checkResult{Field, OK, Msg}`
- `func (a *Agent) postRestart(w, r)` — `POST /api/restart` → 202, chama `a.restart()` numa goroutine
- `func (a *Agent) SetRestart(f func())`, `func (a *Agent) SetListening(addr string)` (chamadas pelo `main`)
- estado: `settings map[string]any` (segredos → bool), `ui_listen_running string`, `cert {names []string, not_after time}`

- [ ] **Step 1: Testes** (`settings_test.go`, com `postSection` via `httptest`):
  - `rede` com `tnas_ip` válido → 204, `a.cfg.TNASIP` mudou e está no ficheiro;
  - `rede` com `tnas_ip: "x"` → 400 `{"field":"tnas_ip"}` e nada mudou;
  - `rede` com `dns.ttl` → 400 `{"field":"dns.ttl"}` (Review Focus 4); chave desconhecida → 400;
  - `rede` com um serviço em `Active` e `tnas_ip` diferente → 409; o mesmo valor → 204;
  - `kuma` com `kuma.npm_token: ""` mantém, `"novo"` substitui; `*a.view.Load()` não contém `"novo"` e tem `"kuma.npm_token":true`;
  - `interface` com `ui.listen` para uma porta ocupada (um `net.Listen` aberto no teste) → 400 `ui.listen`;
  - `postCheck` `caminhos` com `paths.snapshots_dir` inexistente → resultado `ok:false` para esse campo;
  - `postRestart` chama a função dada a `SetRestart`.
- [ ] **Step 2:** FAIL.
- [ ] **Step 3: Implementar** `settings.go` com a tabela (uma linha por chave da spec), `postSection` (decode `{section, values map[string]json.RawMessage}` com `DisallowUnknownFields`; para cada chave: existe e é da secção, senão 400 com o campo; `num` → `int`, senão `string` com `TrimSpace`; segredo vazio salta; campo bloqueado que muda com `!a.allHome()` → 409; `next.validate()` → `FieldError` → 400 JSON; `ui.listen` que muda → `net.Listen` de teste e `Close`; `saveConfig`; `a.cfg = next`; atualizar o `tnasIP` atómico; `a.done(w, "", "definições: "+secção+" alteradas")`), `allHome()` (todos NORMAL e `TNASNPM.Snapshot == ""`), `postCheck` (fora do `mu`: copia a config sob o lock, corre os pings/`sys.Check`/`testToken`/`sys.Get`/`os.Stat` da spec), `postRestart`. Erros JSON com `writeJSON(w, code, v)`.
  - `publish`: `settings` = mapa a partir da tabela; `ui_listen_running = a.listening`; `cert` a partir do `certStore` se existir.
  - rotas em `Handler()`.
- [ ] **Step 4:** `go vet ./... && go test ./... && golangci-lint run ./...` → PASS.
- [ ] **Step 5:** `main.go`: `a.SetListening(cfg.UI.Listen)`; `a.SetRestart(cancel)` (o `cancel` do `ctx` do `serve`); ao sair por reinício, log "reinício pedido na interface". Commit "Every setting through the API, section by section".

### Task 4: e2e em HTTPS

**Files:** Modify `e2e/run.sh`.

- [ ] `login`/`status` com `curl -sfk https://127.0.0.1:18099/…`; mensagem final `Interface: https://localhost:18099`; `docker run` do agente com `--restart unless-stopped` (para o reinício da interface funcionar no demo). Um passo novo depois do arranque: `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:18099/` → `301`.
- [ ] `shellcheck e2e/run.sh && shfmt -d e2e/run.sh`; `make e2e` → `E2E OK`. Commit "e2e over HTTPS".

### Task 5: Secções de Definições na página

**Files:** Modify `internal/agent/web/index.html`.

**Interfaces — Consumes:** `st.settings`, `st.ui_listen_running`, `st.cert`, `POST api/config/section`, `api/config/check`, `api/restart`.

- [ ] **Step 1: Tabela de secções** (JS), com rótulo, chave, tipo e ajuda:

```js
const SECTIONS = [
  {id: 'rede', title: 'Rede', locked: true, fields: [
    {k: 'server.ip', l: 'IP do servidor'}, {k: 'server.npm_check_host', l: 'Endereço do NPM do servidor', h: 'O nome que o agente pede ao NPM do servidor em cada verificação.'},
    {k: 'tnas_ip', l: 'IP do TNAS'}, {k: 'router_ip', l: 'IP do router'}, {k: 'lan_iface', l: 'Interface da LAN', h: 'Para o arping que confirma que um IP está livre (R3).'}]},
  {id: 'verificacao', title: 'Verificação', fields: [
    {k: 'start_timeout_min', l: 'Tempo para a cópia ficar pronta', u: 'min', n: 1},
    {k: 'npm.alert_after_min', l: 'Avisar do NPM em falha após', u: 'min', n: 0},
    {k: 'maintenance.default_expiry_min', l: 'Manutenção por defeito', u: 'min', n: 1}]},
  {id: 'dns', title: 'DNS (Technitium)', fields: [
    {k: 'dns.api_url', l: 'URL da API'}, {k: 'dns.zone', l: 'Zona'}, {k: 'dns.ttl', l: 'TTL do registo', u: 's', n: 1}]},
  {id: 'kuma', title: 'Uptime Kuma', fields: [
    {k: 'kuma.base_url', l: 'URL', h: 'Vazio desliga os avisos para o Kuma.'},
    {k: 'kuma.heartbeat_token', l: 'Token do heartbeat', s: true}, {k: 'kuma.npm_token', l: 'Token do NPM', s: true}]},
  {id: 'caminhos', title: 'Caminhos', locked: true, fields: [
    {k: 'paths.mirror_subvol', l: 'Subvolume do espelho'}, {k: 'paths.mirror_root', l: 'Pasta dentro do espelho'},
    {k: 'paths.snapshots_dir', l: 'Pasta dos snapshots'}, {k: 'paths.overrides_dir', l: 'Pasta dos overrides'}, {k: 'npm.dir', l: 'Pasta do NPM (no espelho)'}]},
  {id: 'noturna', title: 'Descarga noturna', fields: [{k: 'nightly.prepull_at', l: 'Hora', t: 'time', h: 'Vazio desliga a descarga das imagens.'}]},
  {id: 'interface', title: 'Interface', fields: [{k: 'ui.listen', l: 'Endereço e porta', h: 'Ex.: 0.0.0.0:8099. Muda depois de reiniciar o agente.'}]},
];
```

- [ ] **Step 2: Desenho** — `renderSettings()` cria uma vez o esqueleto (`<section class="card set-sec" id="set-<id>">` para geral, cada secção da tabela, conta) e depois pinta cada uma com `paint('set-'+id, html)`, **exceto** se o `<form>` da secção tiver `data-dirty` ou contiver `document.activeElement` (Review Focus 5). O índice ganha as secções novas. Secção bloqueada com `!allHome()` → `<fieldset disabled>` e nota. Segredos: `chip('Definido'|'Em falta')` + `<input type="password" placeholder="Deixa vazio para manter">`. DNS: o formulário dos campos e, por baixo, o formulário do token de hoje. Interface: campo, certificado (`st.cert.names.join(', ')`, "válido até …") e, se `st.settings['ui.listen'] !== st.ui_listen_running`, a nota "Reiniciar agora para usar https://…" com `<button data-restart>`.
- [ ] **Step 3: Comportamento** — `input` num formulário de secção marca/desmarca `data-dirty` comparando com `st.settings` e ativa Guardar/Repor; Repor apaga `data-dirty` e repinta; submeter envia `{section, values}` (números com `Number`, segredos vazios de fora); 400 → mensagem em `.field-err` do campo `data-k`; 409 → `.sec-err`; 204 → toast "Guardado", limpa o estado e chama `api/config/check`, mostrando `✓`/`!` por campo numa `<ul class="checks">`. `beforeunload` com alguma secção suja → `e.preventDefault()`. `data-restart` → `confirmAction` → `POST api/restart` → espera (`fetch(novo + '/healthz', {mode: 'no-cors'})` a cada 2 s, até 90 s) → `location.href = novo + location.pathname + location.hash`.
- [ ] **Step 4: Verificar** com `$S/ui.mjs` (agora `https://localhost:18099`, com `--ignore-certificate-errors` no Chrome e `NODE_TLS_REJECT_UNAUTHORIZED=0` para o login): secções à vista a 1280/420 claro/escuro; escrever num campo de Rede, esperar 6 s, o valor continua; guardar `tnas_ip: x` → a mensagem de erro aparece junto do campo; Repor devolve o valor; guardar `dns.ttl: 120` → toast e lista de verificações.
- [ ] **Step 5:** commit "Every setting in the UI, a form per section".

### Task 6: README e verificação final

- [ ] README: secção de arranque diz `https://192.168.1.249:8099` e o aviso do browser na primeira visita; "Comportamento que o plano não fixava" ganha HTTPS/certificado, Definições por secção, reinício pela interface; tira `ui.tls_cert`/`ui.tls_key`.
- [ ] `go vet ./... && go test -race ./... && golangci-lint run ./...`; `make e2e`; screenshots finais. Commit "README for part B".
