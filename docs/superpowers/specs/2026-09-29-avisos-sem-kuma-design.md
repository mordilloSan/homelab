# Sem Kuma: conta no primeiro acesso, ecrã único, avisos por email, vigia

Data: 2026-09-29 · Estado: aprovado em conversa (secções 1 e 2).

O utilizador vai limpar tudo e começar de novo: **nada de compatibilidade com
instalações antigas**.

## Remoções
- O Kuma por completo: `kuma:` na configuração, os envios em cada verificação,
  o token por serviço, a secção nas Definições. Uma chave `kuma:` passa a ser
  um erro, como qualquer chave desconhecida.
- As compatibilidades: `dns.enabled`, `ui.tls_cert`/`ui.tls_key`, a migração
  dos eventos do `state.json`.
- A configuração de fábrica deixa de trazer serviços (a descoberta propõe-nos).

## Primeiro acesso: criar a conta
- Sem `config/user.yml`, a página de login é "Criar a conta de administrador"
  (utilizador, password ≥ 8, repetir). Ao criar, entra.
- Só nos 30 minutos a seguir ao arranque do agente (depois: "reinicia o agente
  para criar a conta"). Com conta, recusado.
- Não há mais `admin`/`admin` nem o aviso de password por defeito. Password
  perdida: apagar `user.yml` e reiniciar.

## Ecrã único "Configurar em 1 minuto" (instalação nova)
Substitui o assistente de passos. Blocos: **Rede e servidor** (descoberto,
para confirmar; "Avançado" leva às Definições) · **Technitium** (utilizador e
password, o agente cria o token) · **Avisos por email** (atalhos Gmail /
Outlook / Outro; Gmail explica a password de aplicação) · **Serviços**
(descobertos, um visto em cada). **Começar** grava por ordem (rede e zona,
Technitium, email e email de teste, serviços) e mostra o resultado de cada
bloco; fica concluído quando nada falhou. O email é opcional; se preenchido,
o email de teste tem de ir.

## Avisos por email (SMTP, stdlib)
- `email:` `host`, `port`, `security` (`starttls` | `tls` | `none`), `user`,
  `password` (segredo: nunca sai do agente; vazio mantém), `from`, `to`
  (vazios = `user`). Secção "Avisos" nas Definições, com "Enviar email de
  teste" (`POST /api/email/test`).
- Avisa: serviço ativo no TNAS, erro de failover, regresso concluído, NPM em
  falha com o servidor vivo, router inacessível e de volta, agente encravado,
  agente reiniciado depois de uma falha.
- Os avisos de uma verificação seguem num só email, enviado fora do lock; um
  envio que falha fica no log e nos eventos e tenta de novo na verificação
  seguinte, durante um dia (uma falha de rede pode ser o que o email avisa).

## Vigia
- Uma goroutine, a cada minuto: sem uma verificação concluída há mais de
  3 × intervalo + 15 min → email e saída (o Docker reinicia). O `/healthz`
  próprio a falhar 3 vezes seguidas → email e saída.
- O estado guarda uma marca de paragem limpa; um arranque sem ela (e com
  estado) avisa "o agente reiniciou depois de uma falha", com o último evento.
- O email do vigia não depende do lock (a configuração do email é lida de uma
  cópia atómica).

## Testes
Servidor SMTP falso em Go (STARTTLS, TLS, sem TLS, AUTH PLAIN); agrupamento e
tentativas; password fora do estado; vigia com relógio e saída injetados;
marca de paragem; conta no primeiro acesso (janela, recusa com conta); Kuma e
compatibilidades sem sobras; e2e com um Mailpit a receber o email de um
failover real; ecrã único no demo a 1280/420.
