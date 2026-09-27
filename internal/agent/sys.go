package agent

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
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

var apiClient = &http.Client{Timeout: 10 * time.Second}

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

// unwrapURL drops the URL from errors: Kuma push URLs carry the tokens.
func unwrapURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		return ue.Err
	}
	return err
}
