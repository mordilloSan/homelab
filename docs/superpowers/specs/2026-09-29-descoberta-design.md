# Descoberta no TNAS: configurar sem saber os detalhes

Data: 2026-09-29 · Estado: decidido pelo implementador sozinho, a pedido do
utilizador ("faz tudo do início ao fim"); direção escolhida por ele:
descoberta só no TNAS, sem segundo agente.

## Objetivo

Quem instala não devia ter de saber IPs, caminhos, tokens nem overrides. O
agente, no TNAS, descobre o que já tem à mão e a interface pede só decisões:
**que serviços proteger**. As definições técnicas continuam em Definições,
como "Avançado", para corrigir o que a descoberta errar.

Inspirado no clustering do Technitium: juntar-se com o mínimo (um login),
o resto vem sozinho.

## O que se descobre, e de onde

| O quê | De onde |
|---|---|
| IP do router | a rota por defeito do TNAS (`/proc/net/route`; o agente corre com `network_mode: host`) |
| IP do TNAS e interface da LAN | a interface do TNAS na mesma rede do router |
| Proxy hosts do servidor | os ficheiros do NPM no espelho: `<espelho>/<raiz>/<npm>/…/nginx/proxy_host/*.conf` (o NPM escreve um por proxy host, com `server_name`, `set $server` e `set $port`) |
| IP do servidor | o destino mais comum dos proxy hosts que é um IP; confirmado, se houver token, pelo registo `*.<zona>` no Technitium |
| Endereço do NPM do servidor (verificação) | o proxy host que aponta para a porta 81 ou para o próprio contentor do NPM; senão fica o que está |
| Zona DNS | as zonas primárias do Technitium; escolhe a que contém os domínios dos proxy hosts |
| Token do Technitium | o utilizador e a password do Technitium, pedidos uma vez: o agente cria um token com nome `failover-agent` pela API (`/api/user/createToken`) e grava-o; a password não fica guardada |
| Serviços | cada pasta do espelho com `docker-compose.yml` (menos a do NPM), lida com `docker compose config --format json` (o compose resolvido, com `.env`) |
| Endereço de cada serviço | o proxy host cujo destino é um serviço ou `container_name` do compose, ou o IP do servidor com uma porta publicada pelo compose, ou o IP fixo de uma rede macvlan do compose |
| Override e IP livre | uma rede `macvlan` → override com `parent` = interface da LAN do TNAS, e `require_free_ip` = o `ipv4_address` do serviço nela; um serviço com GPU (`deploy…devices`, `gpus`, `runtime: nvidia`) ou `devices` que não é o que o NPM serve → desligado no override (`profiles: ["disabled"]`) |
| Nome | o nome da pasta, em minúsculas, com o que não for `[a-z0-9_-]` trocado por `-` |

Uma descoberta nunca muda nada sozinha: propõe. Quem grava é a interface,
pelos mesmos pedidos de sempre (`/api/config/section`, `/api/service`).

## API

- `GET /api/discover` → o que se descobriu (corre fora do lock; demora o
  `docker compose config` de cada pasta):
  - `network`: `router_ip`, `tnas_ip`, `lan_iface`, `server_ip`,
    `npm_check_host` (cada um vazio se não se soube);
  - `dns`: `zone`, `zones` (se houver token);
  - `npm`: `found` (bool), `proxy_hosts` (quantos);
  - `services`: `[{dir, name, host, hosts[], override_yaml, require_free_ip,
    notes[], configured, error}]`.
- `POST /api/technitium/login` `{user, pass}` → cria o token e grava-o
  (como o `POST /api/dns`); 400 com a mensagem do Technitium.

## Interface

- **Assistente** (instalação nova) com menos passos: Conta · **Technitium**
  (utilizador e password, "Ligar") · **Rede** (o que foi detetado, "Usar
  estes valores", ou editar) · **Serviços** (a lista descoberta, com um
  visto em cada um a proteger, endereço, avisos; "Proteger os escolhidos"
  adiciona-os) · Concluir. Kuma e Caminhos saem do assistente (ficam em
  Definições); o passo Serviços mostra os Caminhos só se o espelho não
  foi encontrado.
- **Visão geral**: "Descobrir serviços" ao lado de "+ Adicionar serviço"
  abre a lista dos descobertos que ainda não estão configurados, cada um
  com "Adicionar" (abre o formulário já preenchido).
- **Formulário do serviço**: escolher a pasta preenche o endereço, o
  override e o IP descobertos (se os campos estiverem vazios), com uma nota
  do que veio da descoberta.
- **Definições → Rede**: botão "Detetar" que preenche os campos com o que
  foi descoberto (sem gravar).
- **Definições → DNS**: além do token, "Entrar com o utilizador do
  Technitium".

## Fora

Agente no servidor; Kuma automático (o Kuma não tem API REST estável);
detetar o subvolume do espelho (continua o configurado; a descoberta diz se
existe).

## Testes

Go, com pastas temporárias e o `fake`: rota por defeito (`/proc/net/route`
de exemplo); escolha da interface da LAN; leitura de proxy hosts do NPM
(vários `server_name`, `set $server` com e sem aspas, ficheiros que não são
proxy hosts); análise do compose resolvido (portas curtas e longas,
macvlan, GPU, `container_name`); correspondência domínio → serviço pelos três
caminhos; nome a partir da pasta; `GET /api/discover` completo; login no
Technitium (token criado e gravado, password errada → 400, password nunca
gravada). e2e: o espelho do e2e ganha proxy hosts do NPM e o `/api/discover`
tem de dar o endereço certo a cada serviço. Interface: o assistente novo e a
lista de descobertos no demo, em largo e estreito.
