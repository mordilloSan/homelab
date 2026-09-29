# failover-agent

Agente em Go que corre no TNAS. Quando um serviço do servidor (`.66`) falha, arranca uma cópia no TNAS a partir de um snapshot Btrfs do espelho; quando o servidor volta, devolve-lhe o serviço e descarta a cópia (o que se escreveu nela perde-se). Segue o plano de 27/09/2026.

## Instalar

Uma tag `v*` (`git tag v1.2.0 && git push --tags`) corre o [release.yml](.github/workflows/release.yml): testes, imagem `ghcr.io/mordillosan/failover-agent` e binário na release.

No TNAS, com o [docker-compose.yml](deploy/docker-compose.yml) em `/Volume1/Docker/failover/`:

```bash
docker compose pull && docker compose up -d
```

O primeiro arranque cria `config/failover.yml` (modo observe, sem serviços). Depois:

1. Entra em `https://192.168.1.249:8099` nos **30 minutos** a seguir ao arranque e cria a conta de administrador (utilizador e password, em bcrypt em `config/user.yml`). Passado esse tempo sem conta, a página pede para reiniciar o container, para ninguém na rede a criar antes de ti. O browser avisa do certificado na primeira visita: é o do próprio agente (ver abaixo), aceita-o uma vez.
2. Abre **Configurar em 1 minuto**, um só ecrã: a rede e o servidor que o agente descobriu (confirma ou corrige), os caminhos do espelho e dos snapshots (confirmados com um snapshot de teste do espelho, feito e apagado logo), o utilizador e a password do Technitium (o agente cria o seu token; a password não fica), os avisos por email (Gmail com uma [password de aplicação](https://myaccount.google.com/apppasswords), Outlook ou outro servidor) e os serviços que descobriu no espelho, com um visto em cada um a proteger. **Começar** grava tudo, envia um email de teste e diz como correu cada bloco; o que falhou fica escrito para corrigir. Pode-se deixar e retomar pelo aviso na Visão geral. Depois, tudo continua em ⚙ **Definições**. Não é preciso editar o `failover.yml`.

- **Password perdida:** `docker exec failover-agent rm /config/user.yml && docker restart failover-agent`, e cria a conta outra vez nos 30 minutos seguintes.
- **Atualizar:** tag nova e `docker compose pull && docker compose up -d`. A versão instalada: `docker compose run --rm failover-agent version`.

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
make start        # demo em https://localhost:18099, como uma instalação nova; emails em http://localhost:18025
make server-down  # simula a falha do servidor (server-up para voltar)
make logs / stop
```

O `e2e` e o `start` partilham containers: não corras os dois ao mesmo tempo. Só se testam no TNAS: T-01, T-03, T-07 a T-10, T-13, T-22, T-23, o `arping` sobre o `ovs_eth0` e se `cap_add` substitui o `privileged`.

## Comportamento que o plano não fixava

- **Configuração:** `tnas_ip` no topo (as cópias também se verificam por ele, R5); campos novos `mode`, `lan_iface`, `start_timeout_min`, `email`. Guardar na interface reescreve o `failover.yml` sem comentários.
- **DNS sempre ligado:** sem DNS a cópia não serve ninguém, por isso as fases F3 e F4 juntaram-se. Sem token do Technitium, um failover dá ERROR antes do snapshot.
- **Internet:** a cada intervalo, cada caixa pede ao seu Technitium (`server.ip` e `tnas_ip`, porta 53) um nome aleatório em `docker.io`, que nenhuma cache tem; só informa, não decide nada. O Docker Hub em HTTPS só se testa no arranque. No e2e não há Technitium, por isso a internet aparece em falta.
- **Sessão:** sai sozinha após 30 min sem atividade.
- **Interface:** três separadores (Visão geral, Eventos, Definições), com o separador no endereço (`#/eventos`, `#/definicoes/dns`). A espera e a estabilidade de cada serviço mudam-se no painel do serviço; o modo e o intervalo em Definições → Geral (passar a automático pede confirmação).
- **Eventos:** em `state/events.jsonl`, uma linha por evento; ficam 30 dias, até 5000. **Exportar CSV** no separador Eventos descarrega o que o filtro mostra (`;` e BOM, para o Excel em português).
- **Descoberta:** o agente descobre sozinho, sem nada no servidor: o router e a rede do TNAS (`/proc/net/route`), os proxy hosts do NPM nos ficheiros que o NPM escreve no espelho (`…/nginx/proxy_host/*.conf`), o IP do servidor (o destino desses proxy hosts, e o registo `*.<zona>` do Technitium), a zona, e cada compose do espelho resolvido com `docker compose config`: o endereço de cada serviço (pelo nome ou hostname do contentor, pelo IP fixo de uma macvlan ou pela porta publicada), o override (macvlan para a interface do TNAS, contentores com GPU desligados) e o IP que tem de estar livre. Só propõe: quem grava é a interface. Em Definições, "Detetar" na Rede e o login do Technitium no DNS; na Visão geral, "Descobrir serviços".
- **Configurar em 1 minuto:** abre sozinho só numa instalação nova (sem `state.json`); depois fica em `#/assistente`. O demo (`make start`) abre-o sempre, porque começa sem estado nem serviços.
- **Avisos por email:** SMTP com a biblioteca do Go (STARTTLS na 587, TLS na 465, ou sem cifra nem login num relay da LAN). Vão por email: um serviço em failover no TNAS, de volta ao servidor, em ERROR; o NPM do servidor em falha e de volta; o router inacessível e de volta; o agente a reiniciar. Os de uma mesma verificação seguem num só email, com o link da interface. Um envio falhado tenta-se nas 3 verificações seguintes e depois fica só o evento. A password nunca sai do agente. **Enviar email de teste** em Definições → Avisos.
- **Watchdog:** uma goroutine à parte das verificações, que não espera pelo lock. Se a verificação parar mais de 3 intervalos e 15 min, ou o próprio `/healthz` falhar 3 minutos seguidos, envia um email e termina o processo, e o Docker arranca-o de novo. Um arranque depois de uma saída que não foi pedida (um crash, o watchdog, um `kill`) envia "o agente reiniciou depois de parar sem ser pedido", com o último evento.
- **Definições:** tudo o que está no `failover.yml` menos os serviços muda-se na interface, secção a secção (Guardar/Repor), com o erro junto do campo e verificações depois de gravar (pings, Technitium, pastas), que só avisam. Rede e Caminhos só mudam com todos os serviços no servidor. A password do email nunca se mostra. Mudar o endereço da interface pede **Reiniciar agora**: o agente sai limpo e o Docker (`restart: unless-stopped`) arranca-o de novo.
- **Serviços:** adicionam-se, editam-se e removem-se na interface (**+ Adicionar serviço**, e **Editar** no painel de cada um): pasta do espelho escolhida de uma lista, endereço, espera, estabilidade, ícone (o nome de um do [dashboardicons.com](https://dashboardicons.com), ou o link da sua página; vazio usa o do nome ou da pasta), override em YAML (verificado com `docker compose config` e gravado em `overrides/<nome>.override.yml`) e IP que tem de estar livre. O nome não muda depois de criado. A pasta, o endereço, o override e o IP só mudam, e o serviço só se remove, com ele no servidor. Remover deixa o ficheiro de override.
- **Ícones:** só do CDN do dashboard-icons (`cdn.jsdelivr.net/gh/homarr-labs/dashboard-icons`), nunca de um link qualquer: o agente não pede endereços escolhidos por quem usa a interface. O agente descarrega cada ícone uma vez para `state/icons/` e a página serve-o daí, sem ir à internet. Só imagens (PNG, JPEG, WebP, GIF, SVG, ICO) até 512 KB; um SVG é servido com uma política que o impede de correr código. Um ícone do dashboard-icons que não existia volta a ser procurado ao fim de um dia.
- **HTTPS:** a interface é sempre HTTPS, com um certificado autoassinado feito pelo agente como no LinuxIO (`state/certificates/`, ECDSA, 395 dias, renovado sozinho a 30 dias do fim ou quando o `tnas_ip` muda). `http://` na mesma porta redireciona para `https://`. O healthcheck confia só nesse certificado.
- **§16:** porta 8099; intervalo de 60 s; a manutenção bloqueia só failovers e o tempo de espera de um serviço só começa quando ela acaba; o TTL do wildcard fica no Technitium.
- **ERROR:** a cópia é removida logo, sem nova tentativa; sai quando o serviço volta no servidor ou com **Forçar failover**. Um IP `.92` ocupado (R3) dá ERROR antes do snapshot.
- **Caso 2.3:** um failover em curso continua.
- **`down`:** sempre `docker compose -p failover-<svc> down -v`, funciona sem snapshot e descarta volumes com nome (O3).
- **Imagens (O4):** verifica se cada stack tem as imagens no TNAS ao arrancar, de hora a hora, após a descarga noturna (seguida de `docker image prune -f`) e a pedido; o que faltar vai para os eventos.
