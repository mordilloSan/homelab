# failover-agent

Agente em Go que corre no TNAS e passa um serviço do servidor (`.66`) para uma cópia no TNAS quando ele falha, e o devolve quando volta. A cópia arranca de um snapshot Btrfs do espelho, e o que se escreve nela perde-se no regresso. Segue o plano de 27/09/2026.

| Ficheiro | O que é |
|---|---|
| `agent.go` | Configuração, estado persistente e máquina de estados (um `Tick` por intervalo) |
| `sys.go` | Efeitos reais: `docker compose`, `btrfs`, `arping`, `ping`, verificações HTTPS com `--resolve` e chamadas HTTP |
| `web.go`, `index.html`, `inter.woff2` | Interface (HTTP Basic com bcrypt), no estilo da LinuxIO: mesmos tokens, ícones mdi e a fonte Inter (SIL OFL) embutida, sem internet |
| `config/failover.example.yml` | Configuração comentada |
| `overrides/` | `immich.override.yml` (sem ML) e `unifi.override.yml` (`parent: ovs_eth0`) |
| `e2e/run.sh` | Teste ponta a ponta com o Docker local |

## Instalar no TNAS

O TNAS não compila nada. Uma tag `v*` no GitHub (`git tag v0.1.0 && git push --tags`) faz o [release.yml](.github/workflows/release.yml) correr os testes. Depois publica a imagem `ghcr.io/mordillosan/failover-agent` e anexa o binário à release.

No TNAS só são precisos quatro ficheiros em `/Volume1/Docker/failover/`: `docker-compose.yml`, `config/failover.yml` (a partir de [failover.example.yml](config/failover.example.yml)) e os dois overrides em `overrides/`.

```bash
cd /Volume1/Docker/failover
# o repositório é privado: uma vez, com um token do GitHub com read:packages
docker login ghcr.io -u mordilloSan
docker compose pull
read -rs P && echo "$P" | docker compose run --rm -T failover-agent hash   # → ui.password_hash
printf '%s\n' '<token do Technitium>' > config/technitium.token && chmod 600 config/technitium.token
# tokens dos monitores Push do Kuma em kuma.* no failover.yml
docker compose up -d
```

Para atualizar, cria uma tag nova e faz `docker compose pull && docker compose up -d` no TNAS. `docker compose run --rm failover-agent version` mostra a versão instalada.

A interface fica em `http://192.168.1.249:8099`. O utilizador pode ser qualquer um; só conta a password.

Antes de ligar, confirma nos composes reais (estão no servidor e não os vi):
- o serviço de ML do Immich chama-se `immich-machine-learning` e o `immich-server` não depende dele;
- a rede macvlan do UniFi chama-se `lan`;
- todos os serviços usam `docker-compose.yml` (não `compose.yaml`).

## Fases (§14)

| Fase | Como |
|---|---|
| F2 observação | `mode: observe`. As decisões automáticas só ficam nos eventos (`[observação] …`). |
| F3 ações sem DNS | `dns.enabled: false` e os botões **Forçar failover** e **Forçar regresso**. Os botões funcionam também em observação. |
| F4 DNS | `dns.enabled: true` no ficheiro, depois `docker compose restart` |
| F6 automático | `mode: auto` na interface |

## Testes

```bash
make test         # go test -race, ~15 s. Inclui um teste com Docker
make lint         # gofmt, go vet, shellcheck, shfmt
make e2e          # ~3 min. Imagem real + Docker real; btrfs trocado por cp/rm
make start        # demonstração com os 5 serviços (vaultwarden, homepage, speedtest, immich, unifi): http://localhost:18099 (password e2e-password-longa)
make server-down  # simula a falha do servidor (server-up para o trazer de volta)
make logs         # registos do agente
make stop         # remove tudo o que o start criou
```

- Os testes unitários (`agent_test.go`, com um TNAS falso) cobrem T-11, T-12, T-14 a T-17 e T-19 a T-21. Também confirmam a ordem do R1, a proteção do `btrfs subvolume delete` e o ficheiro de exemplo.
- O e2e (dois Caddy a fazer de servidor e de NPM do TNAS) cobre T-04 e T-05 sem btrfs, e ainda failover, override, reinício do agente, regresso e limpeza. Cobre também o T-11 com o `arping` real, numa bridge Docker, com um container a ocupar o IP do unifi.
- O `make e2e` e o `make start` usam os mesmos containers. Não mexas no demo enquanto o e2e corre, porque um apaga o outro.
- Só se testam no TNAS: T-01, T-03, T-07 a T-10, T-13, T-22 e T-23, o `arping` sobre o `ovs_eth0` e se `cap_add` substitui o `privileged`.

## Decisões tomadas na implementação

Questões em aberto (§16), com a tua proposta 💡:
1. A manutenção bloqueia só os failovers. Os regressos continuam.
2. Porta 8099, HTTP simples na LAN.
3. Há ações manuais (forçar failover e regresso).
4. O TTL do wildcard fica por tua conta no Technitium. O agente não lhe mexe.
5. Intervalo de 60 s.
6. Modo de observação: `mode: observe`.

Diferenças em relação ao rascunho da configuração (§5.3):
- `tnas_ip` passou para o nível de topo (antes era `dns.failover_ip`), porque as cópias também são verificadas por ele (R5).
- Novos campos:
  - `mode`
  - `lan_iface` (interface do arping)
  - `start_timeout_min` (quanto tempo a cópia tem para ficar saudável)
  - `dns.enabled`
  - `kuma.npm_token` (monitor `failover-npm-servidor`)

Comportamento que o plano não fixava:
- **ERROR:**
  - a cópia é removida logo (DNS, `down`, snapshot) e não há nova tentativa automática;
  - sai de ERROR quando o serviço volta a estar saudável no servidor, ou com **Forçar failover**.
- **IP `.92` ocupado (R3):** vai para ERROR. O `arping` corre antes do snapshot e do NPM, por isso nada fica a correr.
- **Manutenção:** o tempo de espera de um serviço que falhou durante a manutenção só começa a contar quando ela acaba.
- **Caso 2.3:** um failover que já estava em curso continua.
- **`down`:** faz-se sempre `docker compose -p failover-<svc> down -v`, sem ficheiros. Funciona mesmo que o snapshot já tenha desaparecido, e os volumes com nome também se descartam (O3).
- **Imagens:** depois da descarga noturna faz-se `docker image prune -f`, só às imagens sem tag. Se o agente arrancar depois da hora marcada, a primeira descarga é logo.
- **Imagens (O4):** o agente verifica se cada stack tem no TNAS as imagens de que precisa. Usa `docker compose config --images` sobre o espelho, com os overrides, e depois `docker image inspect`. Verifica ao arrancar, de hora a hora, depois da descarga noturna e a pedido na interface. Quando falta alguma, fica registado nos eventos.
- **Interface:** ao guardar, reescreve o `failover.yml` e os comentários perdem-se. O ficheiro de exemplo fica como referência.
