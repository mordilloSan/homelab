# Parte C: serviços pela interface

Data: 2026-09-28 · Estado: decidido pelo implementador, por pedido do
utilizador ("segue em frente sem interacção, faz tudo até ao fim"); os campos
foram vistos com ele na divisão em partes.

Última de três partes (A. estrutura · B. configuração · **C. serviços**).

## Objetivo

Adicionar, editar e remover serviços na interface, sem limite de número, sem
editar o `failover.yml` nem os ficheiros de override.

## Formulário do serviço

Abre no painel do serviço ("Editar") ou vazio em **+ Adicionar serviço**.

| Campo | Chave | Regras | Muda com o serviço fora do servidor? |
|---|---|---|---|
| Nome | `name` | `[a-z0-9][a-z0-9_-]*`, único, não `npm`; **fixo depois de criado** | — |
| Pasta no espelho | `dir` | escolhida de uma lista das pastas de `<espelho>/<raiz>` com `docker-compose.yml`; tem de existir | não |
| Endereço | `host` | nome sem esquema nem `/` | não |
| Espera antes do failover | `wait_min` | lista da parte A, ≥ 1 | sim |
| Estabilidade antes do regresso | `stability_min` | lista, ≥ 0 | sim |
| Ícone | `icon` | link http(s) para uma imagem, descarregada pelo agente para `state/icons/` e servida daí; vazio usa o do dashboard-icons pelo nome e depois pela pasta (SVG, depois PNG), e na falta o ícone da página | sim |
| Override | `override_yaml` | YAML; validado com `docker compose -f <compose> -f <override> config -q`; gravado em `<overrides>/<nome>.override.yml`; vazio tira o override | não |
| IP que tem de estar livre | `require_free_ip` | IP; precisa de `lan_iface` | não |
| Token do Kuma | `kuma_token` | segredo (vazio mantém) | sim |

- "Não" = só com o serviço em NORMAL (mudar o endereço durante um failover
  deixava o registo DNS antigo; a pasta e o override estão a correr).
- O nome não muda porque está nos projetos compose, nos snapshots, no estado e
  nos tokens; para mudar, remove-se e adiciona-se.
- Depois de adicionar ou editar, o agente volta a verificar as imagens.

## Remover

Só com o serviço em NORMAL; pede confirmação. Sai da configuração, do estado,
das barras de verificação e dos tokens do Kuma. O ficheiro de override fica
(pode ter sido escrito à mão). Os eventos antigos ficam no histórico.

## API

- `GET /api/mirror` → `[{"dir", "compose": true}]`, as pastas de
  `<espelho>/<raiz>` com `docker-compose.yml`, por ordem.
- `GET /api/service/override?name=<n>` → o texto do override (vazio se não há).
- `POST /api/service` `{"new": bool, "name", "dir", "host", "wait_min",
  "stability_min", "icon", "override_yaml", "require_free_ip", "kuma_token"}`
  → 204; 400 `{"field", "error"}`; 409 (campo bloqueado com o serviço fora
  do servidor); 404 (editar um que não existe).
- `POST /api/service/remove` `{"name"}` → 204; 409; 404.
- Os serviços no estado ganham `icon` e `kuma_token` (só `true`/`false`).

`validateService` passa a devolver `FieldError` com estas chaves.

## Testes

- Go: adicionar grava o serviço, o override e o token (fora do estado);
  nome repetido, inválido ou `npm` → 400 `name`; pasta sem compose → 400
  `dir`; YAML inválido e `compose config` a falhar → 400 `override_yaml`;
  editar `host` com o serviço em ACTIVE → 409, `wait_min` → 204; override
  vazio tira-o; remover em NORMAL limpa config, estado, barras e token,
  em ACTIVE → 409, desconhecido → 404; `GET /api/mirror` só lista pastas com
  compose.
- Interface: screenshots do formulário (novo e editar) a 1280/420; erro junto
  do campo; o refresh não apaga o que se escreve; adicionar um serviço no
  demo e vê-lo nos cartões e na topologia; remover.
- `make e2e`.
