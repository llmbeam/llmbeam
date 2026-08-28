package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/shao-hua-li/scanchat/internal/pair"
)

func TestParseConfig(t *testing.T) {
	var stderr bytes.Buffer
	configuration, err := parseConfig([]string{
		"--port", "9000",
		"--no-qr",
		"--code-ttl", "2m",
		"--backend", "http://127.0.0.1:8000/v1",
		"--backend", "http://127.0.0.1:9001/v1",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseConfig() error: %v", err)
	}
	if configuration.port != 9000 || !configuration.noQR || configuration.codeTTL != 2*time.Minute {
		t.Fatalf("parseConfig() = %+v", configuration)
	}
	if len(configuration.backends) != 2 {
		t.Fatalf("backends = %v", configuration.backends)
	}
}

func TestParseConfigRejectsInvalidValues(t *testing.T) {
	for _, args := range [][]string{
		{"--port", "0"},
		{"--port", "65536"},
		{"--code-ttl", "0s"},
		{"unexpected"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := parseConfig(args, &bytes.Buffer{}); err == nil {
				t.Fatalf("parseConfig(%v) should fail", args)
			}
		})
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"--version"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run --version status = %d, stderr = %q", status, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != AppName+" "+version {
		t.Fatalf("version output = %q", got)
	}
}

func TestPrintPairingWithoutQR(t *testing.T) {
	var buffer bytes.Buffer
	terminal := &terminalOutput{writer: &buffer}
	terminal.printPairing(pair.CodeUpdate{
		Code:    "2345ABCD",
		Expires: time.Now().Add(10 * time.Minute),
	}, "http://192.168.1.42:8442", true)

	output := buffer.String()
	if !strings.Contains(output, "http://192.168.1.42:8442") || !strings.Contains(output, "2345-ABCD") {
		t.Fatalf("pairing output = %q", output)
	}
	if strings.Contains(output, "Scan with your phone") {
		t.Fatalf("--no-qr output included QR prompt: %q", output)
	}
}
