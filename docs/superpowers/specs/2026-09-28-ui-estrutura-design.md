# Parte A: estrutura e aspecto da interface

Data: 2026-09-28 · Estado: para revisão

Primeira de três partes para configurar tudo pela interface:
**A. estrutura e aspecto** (esta), B. configuração completa, C. serviços pela interface.

## Objetivo

A interface passa de uma página comprida para uma aplicação com separadores,
com aspecto mais profissional mas a mesma identidade e as mesmas animações.
O cartão **Regras** desaparece; os eventos ganham um separador a toda a
largura e 30 dias de histórico. Prepara o esqueleto onde as partes B e C
põem os seus formulários.

**Decidido com o utilizador:** separadores no topo · serviços em cartões
compactos · estilo "como hoje, afinado" · três separadores (Visão geral,
Eventos, Definições) · eventos com filtro por serviço, procura e mais
histórico · manter todas as animações.

## Estrutura

### Topo (uma linha)

- Esquerda: logótipo com gradiente e "Failover do homelab".
- Separadores: **Visão geral · Eventos · Definições**.
- Direita: chip do modo, "Atualizado há X s", ⚙ (vai para Definições) e sair.
- O separador ativo fica no endereço: `#/` , `#/eventos`, `#/definicoes`,
  `#/definicoes/dns`. Recarregar ou abrir um link leva ao sítio certo; o botão
  "voltar" do browser anda entre separadores. Um `#` desconhecido cai na
  Visão geral.
- ≤ 860 px: os separadores passam para uma barra fixa em baixo (ícone +
  texto); o topo fica com logótipo, chip, ⚙ e sair.

### Visão geral

1. Avisos (router, token em falta, password por defeito), como hoje.
2. Topologia, como hoje: clientes, servidor ⇄ TNAS, internet, rodapé com o
   router e a manutenção global.
3. **Serviços · N**: cartões compactos em grelha
   (`repeat(auto-fill, minmax(210px, 1fr))`). Cada cartão: ícone, nome, ponto
   de estado, uma linha de texto (onde corre + tempo de resposta, ou o motivo
   quando não está bem, por exemplo "em falha há 2 min · failover em 3 min") e
   as barras de heartbeat. Cor do estado na borda de baixo e no ícone.
   Clicar abre o painel do serviço.
   - Procura por nome ou endereço, visível a partir de 8 serviços.
   - Botão **+ Adicionar serviço** visível mas desativado, com a dica
     "Em breve" (fica ativo na parte C).
4. O cartão **Regras** desaparece.

### Eventos

- Lista a toda a largura: quando (hoje 14:02 · ontem 22:41 · 27/09 07:29),
  serviço (ou "global"), mensagem. Erros (mensagem começa por `ERRO`) a
  vermelho.
- Filtros: seletor de serviço (Todos · Global · cada serviço) e caixa de
  procura (sem distinguir maiúsculas nem acentos). Contagem "N eventos · 30 dias".
- Mostra 100 de cada vez, com **Carregar mais**.
- Eventos novos entram no topo com a animação atual (`.ev.fresh`), só quando
  passam no filtro.

### Definições

- Página (deixa de ser painel lateral). ≥ 860 px: índice das secções à
  esquerda; abaixo disso, secções empilhadas.
- Secções nesta parte:
  - **Geral**: modo (Observação / Automático, os dois blocos atuais) e
    "Verificar o servidor a cada" (10, 15, 30, 60, 120, 300 s; um valor fora
    da lista aparece como opção extra).
  - **DNS**: o que está hoje no painel (zona, API, estado do token, formulário
    do token).
  - **Conta**: utilizador e **Mudar password**.
- Grava ao mudar, com o aviso "Guardado". Passar para **Automático** pede
  confirmação ("O agente passa a fazer failover e regresso sozinho");
  voltar a Observação não.
- As secções das partes B e C não aparecem até existirem.

### Painel do serviço

Continua a abrir à direita. Muda só:

- **Espera antes do failover** e **Estabilidade antes do regresso** passam a
  listas: 0, 1, 2, 3, 5, 10, 15, 20, 30, 45, 60 min (a espera começa em 1,
  como a validação atual exige). Um valor fora da lista aparece como opção
  extra ("7 min"). Grava ao escolher, com "Guardado"; um erro repõe o valor.

## Estilo (A: como hoje, afinado)

Mantém-se o logótipo com gradiente, as sombras e a cor nas bordas. O que muda:

- Escala de texto fixa: 11.5 / 12.5 / 13 / 15 / 18 px, e só estes.
- Espaçamentos em múltiplos de 4 px; raios de canto 8 (controlos), 12
  (cartões internos) e 14 (cartões de secção).
- Números em `tabular-nums` em todo o lado (tempos, contagens, horas).
- Cor só onde é estado (pontos, ícones, bordas, barras); texto e controlos
  neutros.
- Temas claro e escuro como hoje (seguem o sistema).
- **Animações todas mantidas**: fios com pacotes, link a fluir, ícones de
  estado a respirar, barras a entrar, cartões a deslizar (FLIP) quando um
  serviço muda de caixa, entrada dos eventos, painéis e avisos.
  `prefers-reduced-motion` continua a desligá-las.

## Agente (Go)

### Eventos num ficheiro próprio

Hoje os eventos (máx. 200) vivem em `state.json`, reescrito a cada tick.
Com 30 dias isso deixa de servir.

- Ficheiro `events.jsonl` ao lado de `state.json` (mesmo diretório): uma
  linha JSON por evento (`{"t":…,"svc":…,"msg":…}`), acrescentada em
  `event()`.
- Em memória fica a lista inteira (`a.events`), fora de `State`.
- Limites: 30 dias **e** 5000 eventos. Aparar = reescrever o ficheiro
  (`writeAtomic`) só com o que fica; acontece no arranque e no primeiro tick
  de cada dia.
- Migração: no arranque, se `state.json` tiver `events` e `events.jsonl` não
  existir, passam para o ficheiro novo; `State.Events` deixa de ser gravado.
- Um erro ao escrever o ficheiro vai para o log (`slog.Error`), não pára o
  agente; o evento fica em memória.
- Linhas ilegíveis no ficheiro são ignoradas ao ler (um corte a meio de uma
  escrita não estraga o resto).

### API

- `GET /api/status`: `events` passa a ter só os 50 mais recentes.
- `GET /api/events` (novo, com sessão): todos os eventos em memória, do mais
  recente para o mais antigo.
- `POST /api/config` passa a aceitar alterações parciais: cada campo é
  opcional (`mode`, `check_interval_s`, `services[].wait_min`,
  `services[].stability_min`); o que não vem fica como está. A validação e a
  gravação são as de hoje.

## Fora desta parte

- Campos de Rede, Kuma, Caminhos, porta e HTTPS (parte B).
- Adicionar, editar e remover serviços; ícones por serviço (parte C).
- Filtrar eventos por tipo e exportar.

## Testes

- Go:
  - `event()` acrescenta a `events.jsonl`; um agente novo lê-os de volta.
  - Aparar: eventos com mais de 30 dias e acima de 5000 saem; o ficheiro é
    reescrito.
  - Migração de `state.json` para `events.jsonl`, uma só vez.
  - Linha corrompida no ficheiro é ignorada.
  - `/api/status` com 50; `/api/events` com todos e só com sessão.
  - `POST /api/config` parcial: só o modo muda o modo; só um serviço não mexe
    nos outros nem no intervalo.
- Interface: screenshots do demo (`make start`) a 1280 px e 420 px, claro e
  escuro, nos três separadores e no painel de um serviço; navegar pelo
  endereço e pelo "voltar".
- `make e2e`, `golangci-lint`, `go test ./...`.
