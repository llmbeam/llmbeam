package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/llmbeam/llmbeam/internal/pair"
	"rsc.io/qr"
)

func TestParseConfig(t *testing.T) {
	var stderr bytes.Buffer
	configuration, err := parseConfig([]string{
		"--port", "9000",
		"--no-qr",
		"--code-ttl", "2m",
		"--remote",
		"--backend", "http://127.0.0.1:8000/v1",
		"--backend", "http://127.0.0.1:9001/v1",
	}, &stderr)
	if err != nil {
		t.Fatalf("parseConfig() error: %v", err)
	}
	if configuration.port != 9000 || !configuration.noQR || configuration.codeTTL != 2*time.Minute || !configuration.remote {
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
		{"--remote-url", "http://example.com"},
		{"--remote-url", "https://example.com/?query=bad"},
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
	}, pairingAccess{localURL: "http://192.168.1.42:8442"}, true)

	output := buffer.String()
	if !strings.Contains(output, "http://192.168.1.42:8442") || !strings.Contains(output, "2345-ABCD") {
		t.Fatalf("pairing output = %q", output)
	}
	if strings.Contains(output, "Scan with your phone") {
		t.Fatalf("--no-qr output included QR prompt: %q", output)
	}
}

func TestPrintPairingUsesCompactQRCode(t *testing.T) {
	var buffer bytes.Buffer
	terminal := &terminalOutput{writer: &buffer}
	terminal.printPairing(pair.CodeUpdate{
		Code:    "2345ABCD",
		Expires: time.Now().Add(10 * time.Minute),
	}, pairingAccess{localURL: "http://192.168.1.42:8442"}, false)

	output := buffer.String()
	if !strings.ContainsAny(output, "▀▄█") {
		t.Fatalf("pairing output does not use compact block characters: %q", output)
	}
	if lines := strings.Count(output, "\n"); lines > 24 {
		t.Fatalf("compact pairing output uses %d lines, want at most 24", lines)
	}
}

func TestRemotePairURLKeepsSecretsInFragment(t *testing.T) {
	access := pairingAccess{
		localURL:     "http://192.168.1.42:8442",
		remoteWebURL: "https://llmbeam.github.io/llmbeam/",
		tailcatToken: "tcToken",
	}
	if got := access.pairURL("2345ABCD"); got != "https://llmbeam.github.io/llmbeam/#/connect/tcToken/2345ABCD" {
		t.Fatalf("remote pair URL = %q", got)
	}
}

func TestChooseQRLayoutAdaptsToTerminalSize(t *testing.T) {
	payload := "http://192.168.1.42:8442/#/pair/2345ABCD"
	code, err := qr.Encode(payload, qr.L)
	if err != nil {
		t.Fatal(err)
	}

	standard := chooseQRLayout(payload, 0, 0)
	if !standard.show || standard.quietZone != 2 {
		t.Fatalf("fallback terminal layout = %+v", standard)
	}

	narrow := chooseQRLayout(payload, code.Size+2, 100)
	if !narrow.show || narrow.quietZone != 1 {
		t.Fatalf("narrow terminal layout = %+v", narrow)
	}

	tooSmall := chooseQRLayout(payload, code.Size+1, 100)
	if tooSmall.show {
		t.Fatalf("too-small terminal layout = %+v", tooSmall)
	}

	_, standardHeight := standard.dimensions(code.Size)
	tooShort := chooseQRLayout(payload, 100, standardHeight+pairingOutputRows-2)
	if !tooShort.show || tooShort.quietZone != 1 {
		t.Fatalf("short terminal layout = %+v, want scrollable QR", tooShort)
	}
}

func TestRemoteQRCodeDoesNotOverflowShortTerminal(t *testing.T) {
	payload := "https://llmbeam.github.io/llmbeam/#/connect/" + strings.Repeat("t", 225) + "/2345ABCD"
	layout := chooseQRLayout(payload, 80, 24)
	if !layout.show || layout.quietZone != 1 {
		t.Fatalf("remote QR layout = %+v, want scrollable QR", layout)
	}
}
