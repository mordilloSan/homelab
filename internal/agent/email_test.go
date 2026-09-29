package agent

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"mime"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// smtpServer is a small SMTP server for the tests: plain, STARTTLS or TLS
// from the start. It keeps the login and the message it got.
type smtpServer struct {
	ln          net.Listener
	cfg         *tls.Config
	mode        string
	mu          sync.Mutex
	auth, data  string
	from, rcpts string
}

func newSMTPServer(t *testing.T, mode string) (*smtpServer, *x509.CertPool) {
	t.Helper()
	cert, certFile := testCert(t, t.TempDir(), "smtp", []string{"localhost"})
	_ = certFile
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if mode == "tls" {
		ln = tls.NewListener(ln, cfg)
	}
	s := &smtpServer{ln: ln, cfg: cfg, mode: mode}
	go s.serve()
	t.Cleanup(func() { _ = ln.Close() })
	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return s, pool
}

func (s *smtpServer) port() int {
	a, _ := s.ln.Addr().(*net.TCPAddr)
	return a.Port
}

func (s *smtpServer) serve() {
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.session(c)
	}
}

func (s *smtpServer) set(f *string, v string) {
	s.mu.Lock()
	*f += v
	s.mu.Unlock()
}

// readData reads the message after DATA, up to the lone dot.
func readData(r *bufio.Reader) string {
	var b strings.Builder
	for {
		l, err := r.ReadString('\n')
		if err != nil || l == ".\r\n" {
			return b.String()
		}
		b.WriteString(l)
	}
}

func (s *smtpServer) session(c net.Conn) {
	defer func() { _ = c.Close() }()
	r := bufio.NewReader(c)
	say := func(l string) { _, _ = c.Write([]byte(l + "\r\n")) }
	say("220 localhost ESMTP test")
	secure := s.mode == "tls"
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0]); cmd {
		case "EHLO", "HELO":
			say("250-localhost")
			say(map[bool]string{true: "250 STARTTLS", false: "250 AUTH PLAIN"}[s.mode == "starttls" && !secure])
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, s.cfg)
			if tc.Handshake() != nil {
				return
			}
			c, r, secure = tc, bufio.NewReader(tc), true
		case "AUTH":
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			s.set(&s.auth, strings.ReplaceAll(string(b), "\x00", "|"))
			say("235 ok")
		case "MAIL":
			s.set(&s.from, line)
			say("250 ok")
		case "RCPT":
			s.set(&s.rcpts, line)
			say("250 ok")
		case "DATA":
			say("354 go")
			s.set(&s.data, readData(r))
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (s *smtpServer) got() (auth, from, rcpts, data string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.auth, s.from, s.rcpts, s.data
}

// A Gmail-like STARTTLS on 587, TLS on 465 and a plain LAN relay all deliver;
// the password only goes over TLS, and the subject is encoded for accents.
func TestSMTPSend(t *testing.T) {
	for _, mode := range []string{"starttls", "tls", "none"} {
		srv, pool := newSMTPServer(t, mode)
		m := Mail{Host: "localhost", Port: srv.port(), Security: mode, User: "agente@gmail.com", Password: "app-pass",
			From: "agente@gmail.com", To: "miguel@engmariz.com", Subject: "vaultwarden em failover no TNAS", Body: "às 14:02\n.linha com ponto"}
		if err := smtpSend(m, pool); err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		auth, from, rcpts, data := srv.got()
		if (mode == "none") != (auth == "") || (auth != "" && auth != "|agente@gmail.com|app-pass") {
			t.Errorf("%s: auth %q", mode, auth)
		}
		if !strings.Contains(from, "agente@gmail.com") || !strings.Contains(rcpts, "miguel@engmariz.com") {
			t.Errorf("%s: %q %q", mode, from, rcpts)
		}
		var subj string
		for l := range strings.SplitSeq(data, "\r\n") {
			if after, ok := strings.CutPrefix(l, "Subject: "); ok {
				subj, _ = new(mime.WordDecoder).DecodeHeader(after)
			}
		}
		if subj != "vaultwarden em failover no TNAS" || !strings.Contains(data, "às 14:02") || !strings.Contains(data, "\r\n..linha com ponto") {
			t.Errorf("%s: assunto %q, mensagem %q", mode, subj, data)
		}
	}
}

// The alerts of one check go in one email, sent outside the lock.
func TestAlertsMailed(t *testing.T) {
	a, f := setup(t)
	f.down["bitwarden.engmariz.com@"+srv] = true
	at := t0
	tickTo(a, &at, 6)
	a.mailJobs.Wait()
	if !mailed(f, "vaultwarden") || !mailed(f, "em failover no TNAS") {
		t.Fatalf("sem email do failover: %+v", f.mails)
	}
	for _, m := range f.mails {
		if m.To != "miguel@engmariz.com" || !strings.HasPrefix(m.Subject, "[Failover]") || !strings.Contains(m.Body, "https://") {
			t.Fatalf("email: %+v", m)
		}
	}
	n := len(f.mails)
	a.mu.Lock()
	a.alert("", "um")
	a.alert("", "dois")
	a.flushAlerts()
	a.mu.Unlock()
	a.mailJobs.Wait()
	if len(f.mails) != n+1 || !strings.Contains(f.mails[n].Body, "um") || !strings.Contains(f.mails[n].Body, "dois") {
		t.Fatalf("dois avisos da mesma verificação não seguiram num só email: %+v", f.mails[n:])
	}
}

// A failed send is tried again on the next checks, 3 times, then given up (in the events).
func TestAlertsRetry(t *testing.T) {
	a, f := setup(t)
	f.failMail = errors.New("535 password errada")
	a.mu.Lock()
	a.alert("", "router inacessível")
	a.mu.Unlock()
	for i := range 5 { // an outage can last many checks: the alert waits
		a.mu.Lock()
		a.flushAlerts()
		a.mu.Unlock()
		a.mailJobs.Wait()
		if len(a.alerts) != 1 {
			t.Fatalf("tentativa %d: o aviso não ficou para a seguinte", i)
		}
	}
	a.mu.Lock()
	a.now = a.now.Add(mailKeep + time.Minute)
	a.flushAlerts()
	a.mu.Unlock()
	a.mailJobs.Wait()
	if len(a.alerts) != 0 || !hasEvent(a, "desisto") {
		t.Fatalf("passado um dia: %d na fila, eventos %v", len(a.alerts), a.events)
	}
	f.failMail = nil
}

// Definições → Avisos: the password never leaves the agent; the test email.
func TestEmailSettings(t *testing.T) {
	a, f := setup(t)
	if code, _ := postTo(t, a.postSection, `{"section":"avisos","values":{"email.password":""}}`); code != 204 || a.cfg.Email.Password != "segredo" {
		t.Fatal("vazio não manteve a password")
	}
	if code, _ := postTo(t, a.postSection, `{"section":"avisos","values":{"email.password":" nova-app-pass "}}`); code != 204 || a.cfg.Email.Password != "nova-app-pass" {
		t.Fatal("não substituiu")
	}
	if v := string(*a.view.Load()); strings.Contains(v, "nova-app-pass") || !strings.Contains(v, `"email.password":true`) {
		t.Fatalf("o estado mostra a password: %s", v)
	}
	for body, field := range map[string]string{
		`{"section":"avisos","values":{"email.security":"ssl"}}`:   "email.security",
		`{"section":"avisos","values":{"email.to":"não é email"}}`: "email.to",
		`{"section":"avisos","values":{"email.port":0}}`:           "email.port",
	} {
		if code, got := postTo(t, a.postSection, body); code != 400 || !strings.Contains(got, `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s", field, code, got)
		}
	}
	if code, body := postTo(t, a.postEmailTest, `{}`); code != 204 || !mailed(f, "teste") {
		t.Fatalf("email de teste: %d %s", code, body)
	}
	f.failMail = errors.New("535 5.7.8 Username and Password not accepted")
	if code, body := postTo(t, a.postEmailTest, `{}`); code != 400 || !strings.Contains(body, "not accepted") {
		t.Fatalf("falha do teste: %d %s", code, body)
	}
	_ = time.Second
}
