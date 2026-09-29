# Descoberta no TNAS — plano

> Executado inline (superpowers:executing-plans), sem paragens: o utilizador pediu tudo até ao fim, sem PR nem release.

**Spec:** `docs/superpowers/specs/2026-09-29-descoberta-design.md`

**Global constraints:** sem dependências novas; a descoberta só propõe, nunca grava; nada de passwords do Technitium em disco; textos PT-PT; `go vet`, `go test -race`, `golangci-lint`, `shellcheck`/`shfmt`, `make e2e`.

**Review focus:** (1) proxy hosts do NPM com formatos inesperados (sem `set $server`, vários domínios, comentários) não rebentam nem inventam endereços; (2) `docker compose config` a falhar numa pasta não estraga as outras; (3) um token do Technitium criado mas não gravável não deixa o utilizador sem saber; (4) a descoberta sem rede (sem rota por defeito) devolve vazios, não erros; (5) o assistente novo numa instalação sem espelho encontrado.

### Task 1 — `discover.go`: rede, NPM, compose, correspondência (TDD)
- `defaultRoute(procRoute []byte) string`, `lanOf(ifaces []ifaceAddr, gw net.IP) (ip, iface string)`
- `readProxyHosts(dir string) []proxyHost{Domains []string; Server string; Port int}`; `findProxyHostDir(npmRoot string) string` (procura `nginx/proxy_host` até 4 níveis)
- `analyzeCompose(js []byte, lanIface string) composeInfo{Names map[string]bool; Ports map[int]string; FixedIPs map[string]string; Override string; FreeIP string; Notes []string}` sobre o JSON do `docker compose config --format json`
- `matchHost(hosts []proxyHost, info composeInfo, serverIP string) (host string, all []string)`
- `slugName(dir string) string`
- testes com dados de exemplo para cada um.

### Task 2 — `GET /api/discover` e `POST /api/technitium/login` (TDD)
- `(a *Agent) discover() discovery` fora do lock; `getDiscover`; `postTechnitiumLogin` (createToken → testToken → gravar como o postDNS).
- `technitiumJSON` que devolve o corpo (a `technitium` atual só dá o erro).
- testes com o `fake` (`outs` para o compose, `bodies` para o Technitium).

### Task 3 — e2e
- o espelho do e2e ganha `npm/data/nginx/proxy_host/N.conf` por serviço (formato do NPM); o teste verifica no `/api/discover` o endereço de cada serviço e o override do `web` (ml com GPU → desligado) e do `unifi` (macvlan).

### Task 4 — Interface
- assistente: Conta · Technitium · Rede · Serviços · Concluir; "Descobrir serviços" na Visão geral; formulário do serviço preenchido pela descoberta; "Detetar" em Rede; login do Technitium em DNS.
- verificação no demo com `ui.mjs`.

### Task 5 — README, revisão final, commit e push do branch (sem PR, sem release).
