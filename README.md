# failover-agent

Agente em Go que corre no TNAS. Quando um serviço do servidor (`.66`) falha, arranca uma cópia no TNAS a partir de um snapshot Btrfs do espelho; quando o servidor volta, devolve-lhe o serviço e descarta a cópia (o que se escreveu nela perde-se). Segue o plano de 27/09/2026.

## Instalar

Uma tag `v*` (`git tag v1.2.0 && git push --tags`) corre o [release.yml](.github/workflows/release.yml): testes, imagem `ghcr.io/mordillosan/failover-agent` e binário na release.

No TNAS, com o [docker-compose.yml](deploy/docker-compose.yml) em `/Volume1/Docker/failover/`:

```bash
docker compose pull && docker compose up -d
```

O primeiro arranque cria `config/failover.yml` (modo observe), `config/user.yml` (`admin` / `admin`, em bcrypt) e os overrides em `overrides/`. Depois:

1. Entra em `https://192.168.1.249:8099` e muda a password. O browser avisa do certificado na primeira visita: é o do próprio agente (ver abaixo), aceita-o uma vez.
2. Em ⚙ **Definições**, revê a Rede, o DNS (URL, zona e o token do Technitium, testado antes de ser gravado em `config/technitium.token`), o Kuma e os Caminhos. Não é preciso editar o `failover.yml`.

- **Password perdida:** `docker exec failover-agent rm /config/user.yml && docker restart failover-agent` volta a `admin` / `admin`.
- **Atualizar:** tag nova e `docker compose pull && docker compose up -d`. A versão instalada: `docker compose run --rm failover-agent version`.

Antes de ligar, confirma nos composes do servidor: o ML do Immich chama-se `immich-machine-learning` e o `immich-server` não depende dele; a macvlan do UniFi chama-se `lan`; todos usam `docker-compose.yml`.

## Fases (§14)

| Fase | Configuração |
|---|---|
| F2 observação | `mode: observe` — as decisões só ficam nos eventos |
| F3/F4 ações e DNS | token do Technitium em ⚙ **Definições** + botões **Forçar failover** / **Forçar regresso** |
| F6 automático | `mode: auto` na interface |

## Desenvolvimento

```bash
make test         # go test -race (~15 s, inclui Docker)
make lint         # gofmt, go vet, shellcheck, shfmt
make e2e          # ~3 min, imagem e Docker reais, btrfs trocado por cp/rm
make start        # demo com 5 serviços em https://localhost:18099 (admin / admin)
make server-down  # simula a falha do servidor (server-up para voltar)
make logs / stop
```

O `e2e` e o `start` partilham containers: não corras os dois ao mesmo tempo. Só se testam no TNAS: T-01, T-03, T-07 a T-10, T-13, T-22, T-23, o `arping` sobre o `ovs_eth0` e se `cap_add` substitui o `privileged`.

## Comportamento que o plano não fixava

- **Configuração:** `tnas_ip` no topo (as cópias também se verificam por ele, R5); campos novos `mode`, `lan_iface`, `start_timeout_min`, `kuma.npm_token`. Guardar na interface reescreve o `failover.yml` sem comentários.
- **DNS sempre ligado:** sem DNS a cópia não serve ninguém, por isso as fases F3 e F4 juntaram-se. Sem token do Technitium, um failover dá ERROR antes do snapshot. Um `dns.enabled` antigo é ignorado e sai do ficheiro na próxima gravação.
- **Internet:** a cada intervalo, cada caixa pede ao seu Technitium (`server.ip` e `tnas_ip`, porta 53) um nome aleatório em `docker.io`, que nenhuma cache tem; só informa, não decide nada. O Docker Hub em HTTPS só se testa no arranque. No e2e não há Technitium, por isso a internet aparece em falta.
- **Sessão:** sai sozinha após 30 min sem atividade.
- **Interface:** três separadores (Visão geral, Eventos, Definições), com o separador no endereço (`#/eventos`, `#/definicoes/dns`). A espera e a estabilidade de cada serviço mudam-se no painel do serviço; o modo e o intervalo em Definições → Geral (passar a automático pede confirmação).
- **Eventos:** em `state/events.jsonl`, uma linha por evento; ficam 30 dias, até 5000. Os de um `state.json` antigo passam para lá no arranque.
- **Definições:** tudo o que está no `failover.yml` menos os serviços muda-se na interface, secção a secção (Guardar/Repor), com o erro junto do campo e verificações depois de gravar (pings, Technitium, Kuma, pastas), que só avisam. Rede e Caminhos só mudam com todos os serviços no servidor. Os tokens do Kuma nunca se mostram. Mudar o endereço da interface pede **Reiniciar agora**: o agente sai limpo e o Docker (`restart: unless-stopped`) arranca-o de novo.
- **Serviços:** adicionam-se, editam-se e removem-se na interface (**+ Adicionar serviço**, e **Editar** no painel de cada um): pasta do espelho escolhida de uma lista, endereço, espera, estabilidade, ícone (um link para uma imagem; vazio usa o do [dashboard-icons](https://github.com/homarr-labs/dashboard-icons) pelo nome ou pela pasta), override em YAML (verificado com `docker compose config` e gravado em `overrides/<nome>.override.yml`), IP que tem de estar livre e token do Kuma. O nome não muda depois de criado. A pasta, o endereço, o override e o IP só mudam, e o serviço só se remove, com ele no servidor. Remover deixa o ficheiro de override.
- **Ícones:** o agente descarrega cada ícone uma vez para `state/icons/` e a página serve-o daí, sem ir à internet. Só imagens (PNG, JPEG, WebP, GIF, SVG, ICO) até 512 KB; um SVG é servido com uma política que o impede de correr código. Um ícone do dashboard-icons que não existia volta a ser procurado ao fim de um dia.
- **HTTPS:** a interface é sempre HTTPS, com um certificado autoassinado feito pelo agente como no LinuxIO (`state/certificates/`, ECDSA, 395 dias, renovado sozinho a 30 dias do fim ou quando o `tnas_ip` muda). `http://` na mesma porta redireciona para `https://`. O healthcheck confia só nesse certificado. `ui.tls_cert` e `ui.tls_key` deixaram de existir; um ficheiro antigo com eles continua a carregar.
- **§16:** porta 8099; intervalo de 60 s; a manutenção bloqueia só failovers e o tempo de espera de um serviço só começa quando ela acaba; o TTL do wildcard fica no Technitium.
- **ERROR:** a cópia é removida logo, sem nova tentativa; sai quando o serviço volta no servidor ou com **Forçar failover**. Um IP `.92` ocupado (R3) dá ERROR antes do snapshot.
- **Caso 2.3:** um failover em curso continua.
- **`down`:** sempre `docker compose -p failover-<svc> down -v`, funciona sem snapshot e descarta volumes com nome (O3).
- **Imagens (O4):** verifica se cada stack tem as imagens no TNAS ao arrancar, de hora a hora, após a descarga noturna (seguida de `docker image prune -f`) e a pedido; o que faltar vai para os eventos.
