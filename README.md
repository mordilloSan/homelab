# failover-agent

Corre no TNAS e vigia os serviços do servidor (`.66`). Quando um falha, arranca uma cópia no TNAS a partir de um snapshot Btrfs do espelho e aponta o nome do serviço para o TNAS no Technitium. Quando o servidor volta, repõe o DNS e apaga a cópia. **O que se escreveu na cópia perde-se.**

## Como funciona

A cada verificação (60 s por defeito), o agente pede `https://<serviço>` ao NPM do servidor.

1. Um serviço que falha durante a **espera antes do failover** (2 min por defeito) passa para o TNAS: snapshot do espelho, `docker compose up` da cópia, verificação da cópia pelo NPM do TNAS e registo DNS.
2. Quando volta a responder no servidor durante a **estabilidade antes do regresso** (2 min), o agente repõe o DNS e remove a cópia e o snapshot.
3. Com o router em baixo, o agente não decide nada. Com o NPM do servidor em baixo mas o servidor vivo, só avisa.

Em **observação** só regista nos eventos o que faria. Em **automático** faz tudo sozinho.

## Instalar

No TNAS, em `/Volume1/Docker/failover/`, com o [docker-compose.yml](deploy/docker-compose.yml):

```bash
docker compose pull && docker compose up -d
```

1. Abre `https://192.168.1.249:8099` nos **30 minutos** a seguir ao arranque e cria a conta de administrador. O browser avisa do certificado: é o do próprio agente, aceita-o uma vez.
2. **Configurar em 1 minuto:** confirma a rede e os caminhos que o agente encontrou, entra com o utilizador do Technitium, escolhe o email dos avisos e marca os serviços a proteger. **Começar** grava tudo, faz um snapshot de teste e envia um email de teste. Cada bloco diz se correu bem.
3. Começa em observação. Quando os eventos mostrarem o que esperas, passa a automático em ⚙ Definições → Geral.

**Atualizar:** `docker compose pull && docker compose up -d`. A versão instalada: `docker compose run --rm failover-agent version`.

**Password perdida:** `docker exec failover-agent rm /config/user.yml && docker restart failover-agent`, e cria a conta outra vez nos 30 minutos seguintes.

## Usar

- **Página principal:** a topologia (clientes, servidor, TNAS, internet), os serviços e os eventos.
- **Forçar failover ou regresso:** arrasta um serviço do servidor para o TNAS, ou ao contrário, ou usa os botões no painel do serviço. Um failover forçado **fica no TNAS** até um regresso forçado; um automático volta sozinho.
- **Manutenção** (global ou por serviço): bloqueia os failovers durante o tempo escolhido; os regressos continuam.
- **Serviços:** **+** adiciona um à mão; Definições → Descobrir serviços lista as pastas do espelho ainda por proteger. A pasta, o endereço, o override e o IP só mudam com o serviço no servidor.
- **Ícones:** o nome de um ícone do [dashboardicons.com](https://dashboardicons.com), ou o link da sua página. Vazio: o do nome ou da pasta.
- **Avisos por email:** failover, regresso, erro, NPM do servidor em falha, router, certificado inválido e o agente a reiniciar. Os de uma mesma verificação seguem num só email. Um envio falhado tenta-se de novo durante um dia.
- **Eventos:** 30 dias, com filtro, procura e exportação para CSV.

## Quando algo corre mal

| O que se vê | O que fazer |
|---|---|
| A página diz que já não dá para criar a conta | `docker restart failover-agent` e cria-a nos 30 minutos seguintes |
| Gmail: `535 Username and Password not accepted` | Usa uma [password de aplicação](https://myaccount.google.com/apppasswords) (precisa da verificação em 2 passos), não a password da conta |
| Um serviço "sem endereço" na descoberta | O proxy host do NPM tem de apontar para o nome ou hostname do contentor, para o IP fixo de uma macvlan, ou para o IP do servidor numa porta que o compose publica |
| "O docker compose config não deu nenhum serviço" | O compose no espelho só tem `profiles`, só tem `include`, ou falta-lhe o `.env` |
| O snapshot de teste falha | O espelho tem de ser um subvolume Btrfs no mesmo volume da pasta dos snapshots |
| "Certificado de X inválido" | O serviço conta como a responder, sem failover (o TNAS serve o mesmo certificado). Renova o certificado no NPM |
| Um serviço em ERROR | A cópia já foi removida. Sai sozinho quando o serviço voltar no servidor, ou com Forçar failover. A mensagem diz porquê (por exemplo, o IP da macvlan ocupado) |
| "O agente reiniciou depois de parar sem ser pedido" | O watchdog viu a verificação parada ou a página sem responder, ou houve um crash. O email traz o último evento; `docker logs failover-agent` tem o resto |

## Ficheiros

| Onde | O quê |
|---|---|
| `config/failover.yml` | a configuração; a interface reescreve-o ao gravar (sem os comentários) |
| `config/user.yml` | a conta, com a password em bcrypt |
| `config/technitium.token` | o token que o agente criou no Technitium |
| `state/state.json` | onde está cada serviço; sobrevive a reinícios |
| `state/events.jsonl` | os eventos (30 dias, até 5000) |
| `state/certificates/`, `state/icons/` | o certificado da interface e os ícones descarregados |
| `overrides/<serviço>.override.yml` | o que muda num compose ao correr no TNAS (macvlan, GPU desligada) |

## Desenvolvimento

```bash
make test         # go test -race (~15 s)
make lint         # gofmt, go vet, golangci-lint, shellcheck, shfmt
make e2e          # ~3 min: a imagem com Docker real, btrfs trocado por cp/rm
make start        # demo em https://localhost:18099, como uma instalação nova; emails em http://localhost:18025
make server-down  # simula a falha do servidor (server-up para voltar)
make logs / stop
```

O `e2e` e o `start` usam os mesmos containers: não corras os dois ao mesmo tempo. Só se testa no TNAS: o Btrfs a sério, o `arping` sobre o `ovs_eth0` e as macvlans.

Uma tag `v*` corre o [release.yml](.github/workflows/release.yml): testes, imagem `ghcr.io/mordillosan/failover-agent` e binário na release.
