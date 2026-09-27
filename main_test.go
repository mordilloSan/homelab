package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		code     int
		out, err string
	}{
		{[]string{"version"}, 0, "dev\n", ""},
		{[]string{"help"}, 0, "Uso:", ""},
		{[]string{"-h"}, 0, "", "Uso:"},
		{[]string{"versoin"}, 2, "", `comando desconhecido "versoin"`},
		{[]string{"version", "extra"}, 2, "", `argumento a mais: "extra"`},
		{[]string{"healthcheck", "-bogus"}, 2, "", "flag provided but not defined"},
		// the flag after the command is read, not the default /config/failover.yml
		{[]string{"healthcheck", "-config", "/nope.yml"}, 1, "", "healthcheck: open /nope.yml"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), tc.args, &stdout, &stderr)
		if code != tc.code || !strings.Contains(stdout.String(), tc.out) || !strings.Contains(stderr.String(), tc.err) {
			t.Errorf("%v: código %d, stdout %q, stderr %q", tc.args, code, stdout.String(), stderr.String())
		}
	}
}
