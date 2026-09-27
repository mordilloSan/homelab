# failover-agent

Agente em Go que corre no TNAS. Quando um serviço do servidor (`.66`) falha, arranca uma cópia no TNAS a partir de um snapshot Btrfs do espelho; quando o servidor volta, devolve-lhe o serviço e descarta a cópia (o que se escreveu nela perde-se). Segue o plano de 27/09/2026.

## Instalar

Uma tag `v*` (`git tag v1.2.0 && git push --tags`) corre o [release.yml](.github/workflows/release.yml): testes, imagem `ghcr.io/mordillosan/failover-agent` e binário na release.

No TNAS, com o [docker-compose.yml](deploy/docker-compose.yml) em `/Volume1/Docker/failover/`:

```bash
docker compose pull && docker compose up -d
```

O primeiro arranque cria `config/failover.yml` (modo observe, DNS desligado), `config/user.yml` (`admin` / `admin`, em bcrypt) e os overrides em `overrides/`. Depois:

1. Entra em `http://192.168.1.249:8099` e muda a password.
2. Revê o `config/failover.yml` (tokens do Kuma, DNS) e põe o token do Technitium em `config/technitium.token`.

- **Password perdida:** `docker exec failover-agent rm /config/user.yml && docker restart failover-agent` volta a `admin` / `admin`.
- **Atualizar:** tag nova e `docker compose pull && docker compose up -d`. A versão instalada: `docker compose run --rm failover-agent version`.

Antes de ligar, confirma nos composes do servidor: o ML do Immich chama-se `immich-machine-learning` e o `immich-server` não depende dele; a macvlan do UniFi chama-se `lan`; todos usam `docker-compose.yml`.

## Fases (§14)

| Fase | Configuração |
|---|---|
| F2 observação | `mode: observe` — as decisões só ficam nos eventos |
| F3 ações sem DNS | `dns.enabled: false` + botões **Forçar failover** / **Forçar regresso** |
| F4 DNS | `dns.enabled: true` no ficheiro e `docker compose restart` |
| F6 automático | `mode: auto` na interface |

## Desenvolvimento

```bash
make test         # go test -race (~15 s, inclui Docker)
make lint         # gofmt, go vet, shellcheck, shfmt
make e2e          # ~3 min, imagem e Docker reais, btrfs trocado por cp/rm
make start        # demo com 5 serviços em http://localhost:18099 (admin / admin)
make server-down  # simula a falha do servidor (server-up para voltar)
make logs / stop
```

O `e2e` e o `start` partilham containers: não corras os dois ao mesmo tempo. Só se testam no TNAS: T-01, T-03, T-07 a T-10, T-13, T-22, T-23, o `arping` sobre o `ovs_eth0` e se `cap_add` substitui o `privileged`.

## Comportamento que o plano não fixava

- **Configuração:** `tnas_ip` no topo (as cópias também se verificam por ele, R5); campos novos `mode`, `lan_iface`, `start_timeout_min`, `dns.enabled`, `kuma.npm_token`. Guardar na interface reescreve o `failover.yml` sem comentários.
- **§16:** porta 8099 em HTTP; intervalo de 60 s; a manutenção bloqueia só failovers e o tempo de espera de um serviço só começa quando ela acaba; o TTL do wildcard fica no Technitium.
- **ERROR:** a cópia é removida logo, sem nova tentativa; sai quando o serviço volta no servidor ou com **Forçar failover**. Um IP `.92` ocupado (R3) dá ERROR antes do snapshot.
- **Caso 2.3:** um failover em curso continua.
- **`down`:** sempre `docker compose -p failover-<svc> down -v`, funciona sem snapshot e descarta volumes com nome (O3).
- **Imagens (O4):** verifica se cada stack tem as imagens no TNAS ao arrancar, de hora a hora, após a descarga noturna (seguida de `docker image prune -f`) e a pedido; o que faltar vai para os eventos.
