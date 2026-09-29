package agent

import (
	"bytes"
	"cmp"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Alerts by email: what the agent did or found that someone should know,
// sent over SMTP (Gmail with an app password, or any server). They only
// inform; nothing waits on them.

// EmailConfig is the email: section of failover.yml.
type EmailConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Security string `yaml:"security"` // starttls (587) · tls (465) · none (a LAN relay, no login)
	User     string `yaml:"user"`
	Password string `yaml:"password"` // never leaves the agent
	From     string `yaml:"from"`     // empty: the user
	To       string `yaml:"to"`       // empty: the user
}

func (e EmailConfig) recipient() string { return cmp.Or(e.To, e.User) }

// on: there is a server and someone to write to.
func (e EmailConfig) on() bool { return e.Host != "" && e.recipient() != "" }

func (e EmailConfig) mail(subject, body string) Mail {
	return Mail{Host: e.Host, Port: e.Port, Security: e.Security, User: e.User, Password: e.Password,
		From: cmp.Or(e.From, e.User, e.recipient()), To: e.recipient(), Subject: subject, Body: body}
}

func (e EmailConfig) validate() error {
	if e.Host == "" {
		return nil // no alerts
	}
	for _, r := range []struct {
		bad        bool
		field, msg string
	}{
		{!isName(e.Host), "email.host", "tem de ser o nome do servidor, por exemplo smtp.gmail.com"},
		{e.Port < 1 || e.Port > 65535, "email.port", "tem de estar entre 1 e 65535"},
		{e.Security != "starttls" && e.Security != "tls" && e.Security != "none", "email.security", "tem de ser starttls, tls ou none"},
		{e.From != "" && !isAddress(e.From), "email.from", "tem de ser um endereço de email"},
		{e.To != "" && !isAddress(e.To), "email.to", "tem de ser um endereço de email"},
		{e.recipient() == "", "email.to", "falta a quem enviar"},
	} {
		if r.bad {
			return fe(r.field, r.msg)
		}
	}
	return nil
}

func isAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// Mail is one email to send.
type Mail struct {
	Host     string
	Port     int
	Security string
	User     string
	Password string
	From     string
	To       string
	Subject  string
	Body     string
}

func (RealSys) SendMail(m Mail) error { return smtpSend(m, nil) }

// smtpSend sends m; roots are the trusted CAs (nil: the system's). The
// password only goes over TLS: with security none there is no login.
func smtpSend(m Mail, roots *x509.CertPool) error {
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tcfg := &tls.Config{ServerName: m.Host, RootCAs: roots, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	var err error
	if m.Security == "tls" {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tcfg)
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if m.Security == "starttls" {
		if err = c.StartTLS(tcfg); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if m.User != "" && m.Security != "none" {
		if err = c.Auth(smtp.PlainAuth("", m.User, m.Password, m.Host)); err != nil {
			return err
		}
	}
	if err = c.Mail(m.From); err != nil {
		return err
	}
	if err = c.Rcpt(m.To); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write(message(m)); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// oneLine keeps a header to one line: an error text may carry several.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func message(m Mail) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n",
		oneLine(m.From), oneLine(m.To), mime.QEncoding.Encode("utf-8", oneLine(m.Subject)), time.Now().Format(time.RFC1123Z))
	b.WriteString(strings.ReplaceAll(strings.ReplaceAll(m.Body, "\r\n", "\n"), "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.Bytes()
}

type alertItem struct {
	T        time.Time
	Svc, Msg string
}

// alert is an event someone should know about: it also goes by email, with
// the others of the same check.
func (a *Agent) alert(svc, msg string) {
	a.event(svc, msg)
	if a.cfg.Email.on() {
		a.alerts = append(a.alerts, alertItem{a.now, svc, msg})
	}
}

const mailKeep = 24 * time.Hour

// flushAlerts sends what the check queued in one email, outside mu. A failed
// send goes back in the queue for the next check (an outage may be what it
// reports); an alert older than mailKeep is given up.
func (a *Agent) flushAlerts() {
	if len(a.alerts) == 0 || !a.cfg.Email.on() {
		return
	}
	batch := a.alerts
	a.alerts = nil
	m := a.cfg.Email.mail(alertSubject(batch), a.alertBody(batch))
	a.mailJobs.Go(func() {
		err := a.sys.SendMail(m)
		a.mu.Lock()
		defer a.mu.Unlock()
		if err == nil {
			return
		}
		slog.Warn("enviar o email dos avisos", "error", err)
		keep := slices.DeleteFunc(batch, func(x alertItem) bool { return a.now.Sub(x.T) > mailKeep })
		if n := len(batch) - len(keep); n > 0 {
			a.event("", fmt.Sprintf("o email dos avisos falha há mais de %s, desisto de %d: %v", mailKeep, n, err))
		}
		a.alerts = append(keep, a.alerts...)
	})
}

func alertSubject(batch []alertItem) string {
	first := batch[0].Msg
	if batch[0].Svc != "" {
		first = batch[0].Svc + ": " + first
	}
	if len(batch) == 1 {
		return "[Failover] " + first
	}
	return fmt.Sprintf("[Failover] %d avisos: %s…", len(batch), first)
}

func (a *Agent) alertBody(batch []alertItem) string {
	var b strings.Builder
	for _, x := range batch {
		who := cmp.Or(x.Svc, "geral")
		fmt.Fprintf(&b, "%s  %s: %s\n", x.T.Format("02/01 15:04"), who, x.Msg)
	}
	if w := a.watchCfg.Load(); w != nil && w.ui != "" {
		fmt.Fprintf(&b, "\nInterface: %s\n", w.ui)
	}
	return b.String()
}

// postEmailTest sends a test email with the saved settings, outside mu, and
// says what the server answered.
func (a *Agent) postEmailTest(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	e, body := a.cfg.Email, a.alertBody([]alertItem{{time.Now(), "", "email de teste: os avisos do failover chegam aqui"}})
	a.mu.Unlock()
	if !e.on() {
		http.Error(w, "falta o servidor ou a quem enviar", http.StatusBadRequest)
		return
	}
	if err := a.sys.SendMail(e.mail("[Failover] Email de teste", body)); err != nil {
		http.Error(w, "o servidor de email recusou: "+err.Error(), http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = time.Now()
	a.done(w, "", "email de teste enviado para "+e.recipient())
}
