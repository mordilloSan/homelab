# Plano de melhorias: velocidade, eficiência e experiência

Data: 2026-09-30 · Estado: decidido ponto a ponto com o utilizador, a partir da
análise de 30/09 (12 áreas, 96 propostas, cada uma julgada por dois revisores).
Sem código alterado; este é o plano. Referências de padrões: LinuxIO
(`~/LinuxIO/backend`).

Convenções: cada release num branch `dev/vX.Y.Z` e PR; quem faz commit é o
utilizador. Esforços em horas de trabalho.

## v1.9.1: velocidade e eficiência (~9 h)

1. **Cópia verificada dentro do tick.** Em FAILING_OVER, depois do `compose up`,
   verificar a cópia pelo NPM do TNAS a cada 5 s até responder, dentro do mesmo
   tick (cada check com o seu timeout de 10 s; o loop respeita
   `start_timeout_min`). Efeito: o DNS aponta ≤5 s depois de a cópia responder
   (−30 s em média, até −55 s por failover). 1,5 h.
2. **Publicar e gravar a cada transição.** `set()` publica a view; o estado é
   gravado logo que a cópia existe. Todos os chamadores têm `a.mu`
   (agent.go:747,775,782,811,857,892; web.go:313,317). Efeito: cada passo na
   página dentro de um poll; nenhuma cópia órfã num restart a meio do tick. 1 h.
3. **state.json só quando muda, com fsync.** Comparar os bytes; escrever com
   fsync (padrão `utils.WriteFileAtomic` do LinuxIO). Efeito: de 1440 escritas
   por dia para as reais; estado durável num corte de energia. 1 h.
4. **Pull explícito das imagens em falta antes do `up`.** Se o scan diz que
   faltam imagens, um passo `compose pull` com a mensagem "a descarregar N
   imagens…" na pílula, evento, e o `start_timeout_min` só começa depois.
   Efeito: a descarga deixa de ser invisível e não faz o failover dar ERROR.
   1,5 h. Depende do scan (ponto 5) saber o que falta.
5. **Scan de imagens uma vez por dia, quando a stack muda, e em paralelo.**
   Sai o scan de hora a hora; fica o do arranque, o da noturna e o disparado por
   mudar a pasta ou o override de um serviço (não o ícone nem os tempos). O
   `compose config` das stacks corre em paralelo. Efeito: ~400 processos/dia a
   menos; scan ~3× mais rápido. 1,5 h.
6. **Um só helper Technitium** para dns add/delete, testToken, zonas e login.
   −18 linhas. 15 min.
7. **`stop_grace_period: 12m`** no compose (o timeout máximo de um comando é
   10 min) e um log "a acabar a verificação em curso antes de sair". Efeito: um
   update ou reboot nunca mata um failover a meio. 15 min.
8. **Polling pára com o separador escondido** (`document.hidden`); ao voltar
   pede logo. As animações pausam nesse tempo. 0,5 h.
9. **Gzip da página, da fonte e do histórico de eventos; ETag na página; a
   página recarrega-se quando vê uma versão nova do agente.** O ETag no
   `/api/status` não compensa (as barras mudam a cada tick). Efeito: a página
   passa de 144 KB para ~35 KB; sem páginas antigas depois de um update. 1,5 h.

## v1.9.2: avisos (~10,5 h)

10. **Emails que contam a história.** Um email por incidente, com o assunto a
    dizer tudo: "6 serviços em failover no TNAS às 03:12" e "6 serviços de
    volta ao servidor após 6 h 2 min no TNAS"; o corpo lista os serviços e as
    horas de cada passo. Nota: `Since` de ACTIVE não muda durante o estado, por
    isso a duração é `now − Since`. 2 h.
11. **Servidor caiu.** No tick em que o servidor deixa de responder: evento, o
    ponto vermelho de hoje e **um** email com a hora; o contrário quando volta.
    Sem banner. 1 h.
12. **A noturna diz como correu.** No fim, um evento com o resumo (N imagens,
    X MB, falhas); uma noite com falhas vai por email. 2 h.
13. **Emailar o que hoje só fica nos eventos:** as linhas do modo observação
    (agent.go:743,771), a falha do pull (agent.go:1193) e o primeiro "regresso
    por concluir". E **guardar no estado os certificados inválidos já
    avisados** (agent.go:333,370,464-475), para não repetir emails a cada
    restart. 1,5 h.
14. **ERROR sem nada a correr conta como "em casa".** Sem snapshot nem DNS,
    desbloqueia Rede, Caminhos, os campos do serviço e Remover. 2 h.
15. **Repetir o arranque da cópia depois de um ERROR.** Só em `auto`, fora de
    manutenção e com o servidor ainda em baixo: nova tentativa a cada
    `start_timeout_min`; cada tentativa fica nos eventos. 2 h.

## v1.10: topologia e prontidão (~19 h)

16. **O failover em passos na topologia.** Snapshot → cópia a arrancar → cópia
    saudável → DNS, cada um com a hora e a duração (o passo em curso a pulsar),
    "próxima verificação em N s", e o título do separador com o estado. Precisa
    do ponto 2. Falta uma hora para o `compose up` (hoje só snapshot, DNS e fim
    têm eventos: agent.go:848,825,811). Padrão: `common/durabletask` do
    LinuxIO. 4 h.
17. **Onde ficam os dados da cópia.** A descoberta e o formulário analisam os
    volumes do compose: um bind absoluto fora de `<mirror_subvol>` arranca
    vazio; um volume nomeado perde-se no regresso. Aviso em PT-PT em cada um.
    3 h.
18. **Espelho que deixou de mudar.** Guardar o mtime mais recente das pastas
    do espelho; avisar (evento + email) ao fim de N dias sem mudar (N em
    Definições, 2 por defeito); a idade do espelho na caixa do TNAS. 2,5 h.
19. **Veredicto noturno "Pronto para failover".** Junto com a noturna: token
    válido, imagens no TNAS, espelho recente, snapshot de teste (feito e
    apagado), compose do NPM, IP da macvlan livre, mais os pré-requisitos do
    ponto 20. Um veredicto por serviço; a caixa do TNAS diz "Pronto para
    failover · verificado às 05:03" ou "2 problemas"; um problema novo vai por
    email. 4 h.
20. **Pré-requisitos do TNAS no scan e na descoberta.** Redes externas existem
    (`docker network inspect`), portas publicadas livres (`net.Listen`),
    macvlan externa presente; nota no serviço com a dica em PT-PT ("cria a rede
    x no TNAS: docker network create x"). 4 h.
21. **Instalar no telemóvel e o estado no separador.** `manifest.json` (nome,
    logótipo, cor, ecrã inteiro), título "● 2 em failover · Failover do
    homelab" e favicon a mudar de cor. Sem service worker. 1,5 h.

## v1.11: ops e telemóvel (~3,5 h)

22. **Imagem mais pequena.** `alpine` + os dois binários estáticos do
    `docker:cli` (docker, docker-compose) + btrfs-progs, arping, ping, tzdata,
    ca-certificates. 256 MB → ~100 MB. O e2e confirma. 1 h.
23. **Release ao merge.** Merge de `dev/vX.Y.Z` para `main` cria a tag
    `vX.Y.Z` no commit do merge e publica; outro nome de branch não publica.
    Notas da release = texto do PR. `actionlint` no fim. 1 h.
24. **No telemóvel, arrastar só depois de segurar** meio segundo (com
    vibração curta); antes disso o dedo faz scroll. No computador fica igual.
    1,5 h.

## Decidido não fazer

- Tirar o HEALTHCHECK da imagem (fica; no demo aparece "unhealthy" porque o
  `-config` não está em `/config`).
- Descoberta só refeita quando algo a pode mudar; NPM/Technitium no lote
  paralelo.
- Botão "Descarregar agora" e pull automático das imagens em falta.
- Sinal "precisa de internet" nas pílulas e aviso na confirmação do forçar.
- Topologia esbatida sem agente e esqueleto ao abrir.
- Guardar o último snapshot 7 dias.
- Vigiar o espelho em segundo plano (pastas novas, drift).
- Sessões persistentes e "manter a sessão neste dispositivo".
- SSE em vez de polling.
- CI/testes mais rápidos (golangci-lint pré-compilado, e2e −1 min).
- Botão "Ficar no TNAS" durante a contagem do regresso.

## Rejeitado pelos revisores (não perguntado)

Ensaiar o failover sem tocar no DNS · Engine API em vez do CLI · cache dos
composes por mtime · detetar failover.yml editado à mão · descobrir os
caminhos do espelho · modo por serviço · beats compactos · update com um
toque a partir da página · baixar o TTL do wildcard com um clique · retomar a
última cópia guardada · `compose down` com timeout curto · prioridade por
ordem · backoff nos retries SMTP · `container_name` repetido, binds em falta e
`.env` via stderr.
