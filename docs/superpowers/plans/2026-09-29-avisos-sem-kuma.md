# Sem Kuma, conta, ecrã único, email, vigia — plano

> Executado inline (superpowers:executing-plans). Sem commits: o utilizador pediu que o branch dev/v1.7.0 fique por gravar.

**Spec:** `docs/superpowers/specs/2026-09-29-avisos-sem-kuma-design.md`

1. Remover o Kuma e as compatibilidades; configuração de fábrica sem serviços.
2. Conta no primeiro acesso (`/login` com `data-setup`, `POST /setup`, janela de 30 min).
3. Email: `email.go` (envio SMTP, fila, alertas), secção Avisos, `POST /api/email/test`; `System.SendMail`.
4. Vigia (`watchdog.go`) e marca de paragem limpa.
5. Ecrã único na página.
6. e2e com Mailpit; README; revisão final.
Cada passo: testes primeiro (a falhar), implementação, `go test -race`, `golangci-lint`.
