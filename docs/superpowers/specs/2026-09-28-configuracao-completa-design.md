# Parte B: toda a configuração pela interface

Data: 2026-09-28 · Estado: aprovado em conversa, secção a secção

Segunda de três partes (A. estrutura e aspecto · **B. configuração completa** ·
C. serviços pela interface). Parte do esqueleto da parte A
(`2026-09-28-ui-estrutura-design.md`).

## Objetivo

Nunca mais abrir o `failover.yml`: tudo o que lá está, menos os serviços,
muda-se em Definições. A interface passa a ser sempre HTTPS, com um
certificado gerado e renovado pelo agente, como no LinuxIO.

**Decidido com o utilizador:** Guardar e Repor por secção · HTTPS sempre com
certificado próprio (como o LinuxIO), sem certificado do utilizador por
agora · o healthcheck confia só no certificado da interface (sem
`InsecureSkipVerify`) · tokens nunca mostrados · campos perigosos só com
todos os serviços no servidor.

## Secções das Definições

Pela ordem do índice. As chaves são as do `failover.yml`.

| Secção (`id`) | Campos | Grava |
|---|---|---|
| Geral (`geral`) | `mode`, `check_interval_s` | ao mudar (parte A) |
| Rede (`rede`) 🔒 | `server.ip`, `server.npm_check_host`, `tnas_ip`, `router_ip`, `lan_iface` | Guardar/Repor |
| Verificação (`verificacao`) | `start_timeout_min`, `npm.alert_after_min`, `maintenance.default_expiry_min` | Guardar/Repor |
| DNS (`dns`) | `dns.api_url`, `dns.zone`, `dns.ttl`; token como hoje ("Testar e gravar") | Guardar/Repor |
| Kuma (`kuma`) | `kuma.base_url`, `kuma.heartbeat_token` 🔑, `kuma.npm_token` 🔑 | Guardar/Repor |
| Caminhos (`caminhos`) 🔒 | `paths.mirror_subvol`, `paths.mirror_root`, `paths.snapshots_dir`, `paths.overrides_dir`, `npm.dir` | Guardar/Repor |
| Descarga noturna (`noturna`) | `nightly.prepull_at` (HH:MM, vazio = desligada) | Guardar/Repor |
| Interface (`interface`) | `ui.listen`; certificado atual (só leitura) | Guardar/Repor + reiniciar |
| Conta (`conta`) | utilizador, mudar password (parte A) | como hoje |

🔒 bloqueada: só muda com todos os serviços em NORMAL e o NPM do TNAS parado.
🔑 segredo: nunca sai do agente; a página mostra "Definido"/"Em falta" e um
campo vazio que, deixado vazio, mantém o valor.

Fora: serviços e `kuma.service_tokens` (parte C); `dns.token_file` fica
interno; assistente de primeiro arranque (a configuração por defeito já traz
os valores do utilizador).

### Comportamento de uma secção

- Guardar e Repor só ativos com alterações. O refresh de 5 s não repinta
  uma secção com alterações ou com o foco lá dentro. `beforeunload` avisa se
  houver alterações por gravar.
- Guardar envia só essa secção. Recusa (400) mostra o erro junto do campo
  que falhou; bloqueio (409) mostra o motivo por cima dos botões.
- Depois de gravar, a página pede as verificações da secção e mostra-as por
  baixo, como avisos (não bloqueiam: um ping falha com o servidor em baixo
  sem o IP estar errado):
  - Rede: ping ao router, ao servidor e ao TNAS; NPM do servidor.
  - DNS: a zona responde ao token atual.
  - Kuma: o URL responde (qualquer resposta HTTP).
  - Caminhos: cada pasta existe (espelho, espelho/raiz, espelho/raiz/NPM,
    snapshots, overrides).
- Secção bloqueada com algum serviço fora do servidor: campos desativados e
  a nota "Só com todos os serviços no servidor".

## Validação (agente)

`validate()` passa a devolver `FieldError{Field, Msg}` com a chave do campo
que falhou. Regras novas, além das atuais:

- `server.ip`, `tnas_ip`, `router_ip`: endereço IP.
- `server.npm_check_host`: nome sem esquema, espaços nem `/`.
- `lan_iface`: sem espaços nem `/` (pode ficar vazio sem `require_free_ip`).
- `dns.api_url` e `kuma.base_url` (se preenchido): `http://` ou `https://`
  com anfitrião.
- `dns.zone`: nome sem espaços; `dns.ttl`: 1–86400.
- `paths.mirror_subvol`, `paths.snapshots_dir`, `paths.overrides_dir`:
  absolutos. `paths.mirror_root`, `npm.dir`: relativos, sem `..`.
- `maintenance.default_expiry_min`: 1–10080 (um 0 no ficheiro lê-se como 60).
- `ui.listen`: `anfitrião:porta` com porta 1–65535.

## HTTPS e certificado

- Certificado em `<pasta do estado>/certificates/0-self-signed.cert` e
  `.key` (chave 0600): ECDSA P-256, 395 dias, nomes `localhost`, nome da
  máquina, `nome.local`, IPs `127.0.0.1`, `::1`, `tnas_ip` e os da máquina.
- Gerado quando falta, está ilegível ou incompleto (com log), quando faltam
  30 dias ou menos para o fim, ou quando `tnas_ip` não está nos IPs dele. A
  verificação corre em cada handshake (barata: compara datas e IPs em
  memória), por isso a renovação entra sem reiniciar.
- A porta da interface serve HTTPS; um pedido HTTP simples na mesma porta
  recebe `301` para `https://` com o mesmo anfitrião e caminho (como o
  LinuxIO: espreita o primeiro byte, `0x16` é TLS).
- Saem `ui.tls_cert` e `ui.tls_key`: um ficheiro que ainda os tenha carrega
  e perde-os na próxima gravação.
- `healthcheck` confia só neste certificado (raiz única, nome `localhost`,
  ligação a `127.0.0.1`).

## Reiniciar

- Guardar em Interface valida `ui.listen` e, se mudou, tenta abrir a porta
  antes de gravar (recusa se estiver ocupada).
- Com o endereço gravado diferente do que está a ser usado, a secção mostra
  "Reiniciar agora para usar https://…" e um botão. Confirmar →
  `POST /api/restart` → o agente acaba o tick em curso, grava e sai (0); o
  Docker (`restart: unless-stopped`) arranca-o de novo. A página espera pelo
  endereço novo e abre-o.

## API

- `POST /api/config/section` `{"section": "<id>", "values": {"<chave>": valor}}`
  → 204; 400 `{"field", "error"}` (inválido, ou chave que não é da secção);
  409 `{"error"}` (secção bloqueada e um campo bloqueado mudou).
- `POST /api/config/check` `{"section": "<id>"}` → `[{"field", "ok", "msg"}]`.
- `POST /api/restart` → 202.
- `/api/status` ganha `settings` (valores de todas as chaves acima; segredos
  como `true`/`false`), `ui_listen_running` e `cert` (`names`, `not_after`).

## Testes

- Go: cada secção grava um valor válido; cada regra nova recusa com o campo
  certo; chave de outra secção → 400; secção bloqueada com um serviço em
  failover → 409, e sem mudar o campo bloqueado → 204; segredo vazio mantém,
  novo substitui, `/api/status` nunca o mostra; certificado gerado, reusado,
  renovado a 30 dias, trocado quando estragado e quando `tnas_ip` muda;
  redirecionamento HTTP→HTTPS na mesma porta; porta ocupada recusada;
  `/api/restart` termina o `Run`; `healthcheck` com o certificado gerado.
- Interface: screenshots de todas as secções (1280/420, claro/escuro);
  guardar com erro mostra-o no campo; repor.
- `make e2e` em HTTPS.
