package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

type RealSys struct{}

func (s RealSys) Run(name string, args ...string) error {
	_, err := s.Output(name, args...)
	return err
}

// Output returns stdout; stderr (compose warnings, errors) only goes into the error.
func (RealSys) Output(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = "/" // compose must never pick up a compose file from the working directory
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(cmp.Or(stderr.String(), string(out)))
		if len(msg) > 400 {
			msg = "…" + msg[len(msg)-400:]
		}
		return string(out), fmt.Errorf("%s: %w: %s", name, err, msg)
	}
	return string(out), nil
}

func (RealSys) Check(host, ip string) error {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	c := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			// Like curl --resolve: connect to ip, keep host for SNI and the Host header.
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, _ := net.SplitHostPort(addr)
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
			},
			// Like curl -k: this tests liveness, a certificate problem is not a failover.
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get("https://" + host + "/")
	if err != nil {
		return unwrapURL(err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// Resolve asks the DNS resolver at ip for a name no cache holds, so the answer
// has to come from the internet; NXDOMAIN counts as reached.
func (RealSys) Resolve(ip string) error {
	d := &net.Dialer{Timeout: 5 * time.Second}
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return d.DialContext(ctx, network, net.JoinHostPort(ip, "53"))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err := r.LookupHost(ctx, rand.Text()+".docker.io.") // rooted: no search domains
	if de, ok := errors.AsType[*net.DNSError](err); ok && de.IsNotFound {
		return nil
	}
	return err
}

var apiClient = &http.Client{Timeout: 10 * time.Second}

// refuseLocal is the dialer's check for icon downloads: never loopback
// (where the TNAS's Technitium API and this UI answer on the host network)
// nor a link-local address. The TNAS's LAN address is not refused: the
// LAN is where self-hosted icons live. It runs on every connection, so a redirect or a name
// that resolves there is refused too; the LAN stays reachable.
func refuseLocal(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if err != nil || ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return fmt.Errorf("endereço local recusado para ícones: %s", host)
	}
	return nil
}

var iconClient = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second, Control: refuseLocal}).DialContext,
	TLSHandshakeTimeout: 5 * time.Second,
}}

// GetIcon downloads an icon's link, through iconClient.
func (RealSys) GetIcon(u string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New("URL inválido")
	}
	resp, err := iconClient.Do(req)
	if err != nil {
		return nil, unwrapURL(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, err
}

func (RealSys) Get(u, bearer string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, errors.New("URL inválido")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := apiClient.Do(req)
	if err != nil {
		return nil, unwrapURL(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return body, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, err
}

// unwrapURL drops the URL from errors: a URL may carry a secret (the
// Technitium login's password).
func unwrapURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}
