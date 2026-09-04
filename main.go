package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"
	"rsc.io/qr"

	"github.com/llmbeam/llmbeam/internal/backend"
	"github.com/llmbeam/llmbeam/internal/connect"
	"github.com/llmbeam/llmbeam/internal/lan"
	"github.com/llmbeam/llmbeam/internal/netutil"
	"github.com/llmbeam/llmbeam/internal/pair"
	"github.com/llmbeam/llmbeam/internal/remote"
	"github.com/llmbeam/llmbeam/internal/security"
	"github.com/llmbeam/llmbeam/internal/server"
	"github.com/llmbeam/llmbeam/internal/ui"
)

const (
	AppName             = "llmbeam"
	defaultRemoteWebURL = "https://llmbeam.github.io/llmbeam/"
)

var version = "dev"

type stringSlice []string

func (values *stringSlice) String() string {
	return fmt.Sprint(*values)
}

func (values *stringSlice) Set(value string) error {
	*values = append(*values, value)
	return nil
}

type config struct {
	port      int
	noQR      bool
	codeTTL   time.Duration
	showVer   bool
	remote    bool
	remoteURL string
	backends  stringSlice
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "connect" {
		return runConnect(args[1:], os.Stdin, stdout, stderr)
	}
	configuration, err := parseConfig(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	if configuration.showVer {
		fmt.Fprintln(stdout, AppName, version)
		return 0
	}

	terminal := &terminalOutput{writer: stdout}
	terminal.block(func(w io.Writer) {
		fmt.Fprintf(w, "\n  %s %s\n\n", AppName, version)
	})

	results, candidates, err := backend.Discover(configuration.backends, 800*time.Millisecond)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	terminal.printDiscovery(results)

	lanIP, err := netutil.LANIP()
	if err != nil {
		fmt.Fprintln(stderr, "error: cannot determine private LAN IP:", err)
		return 1
	}
	baseURL := "http://" + net.JoinHostPort(lanIP, strconv.Itoa(configuration.port))

	pairs := pair.NewManager(configuration.codeTTL)
	registry := backend.NewRegistry(candidates, 800*time.Millisecond)
	limiter := pair.NewRateLimiter(5, time.Minute, 5*time.Minute)
	gateway := server.New(pairs, registry, limiter, ui.FS())
	handler := gateway.Handler()
	access := pairingAccess{localURL: baseURL}
	terminal.printConnectorCode(gateway.ConnectorCode(), gateway.ConnectorCodeExpiry())

	var remoteServer *remote.Server
	if configuration.remote {
		terminal.block(func(w io.Writer) {
			fmt.Fprintln(w, "\n  Starting encrypted Tailcat remote access...")
		})
		remoteServer, err = remote.Start(handler)
		if err != nil {
			fmt.Fprintln(stderr, "error: cannot start Tailcat remote access:", err)
			return 1
		}
		defer remoteServer.Close()
		access.remoteWebURL = configuration.remoteURL
		access.tailcatToken = remoteServer.Token()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initialCode := <-pairs.CodeUpdates()
	terminal.printPairing(initialCode, access, configuration.noQR)
	go watchPairingCodes(ctx, pairs, initialCode, terminal, access, configuration.noQR)

	slog.SetDefault(slog.New(slog.NewTextHandler(terminal, nil)))
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(configuration.port))
	httpServer := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Fprintln(stderr, "server error:", err)
		return 1
	}
	defer listener.Close()

	var tlsServer *http.Server
	var tlsServeError chan error
	tlsPort := 0
	identity, identityErr := security.NewTLSIdentity()
	if identityErr != nil {
		fmt.Fprintln(stderr, "warning: secure LAN connector unavailable:", identityErr)
	} else if configuration.port == 65535 {
		fmt.Fprintln(stderr, "warning: secure LAN connector unavailable: no TLS port available")
	} else {
		tlsPort = configuration.port + 1
		tlsAddress := net.JoinHostPort("0.0.0.0", strconv.Itoa(tlsPort))
		rawTLSListener, listenErr := net.Listen("tcp", tlsAddress)
		if listenErr != nil {
			fmt.Fprintln(stderr, "warning: secure LAN connector unavailable:", listenErr)
			tlsPort = 0
		} else {
			gateway.SetServerFingerprint(identity.Fingerprint)
			tlsServer = &http.Server{
				Addr:              tlsAddress,
				Handler:           handler,
				ReadHeaderTimeout: 5 * time.Second,
				ReadTimeout:       30 * time.Second,
				IdleTimeout:       2 * time.Minute,
				MaxHeaderBytes:    1 << 20,
			}
			tlsServeError = make(chan error, 1)
			go func() {
				tlsServeError <- tlsServer.Serve(tls.NewListener(rawTLSListener, identity.ServerConfig()))
			}()
			defer tlsServer.Close()
		}
	}

	advertiser := lan.NewAdvertiser()
	metadata := map[string]string{"version": version}
	if tlsPort > 0 {
		metadata["tls_port"] = strconv.Itoa(tlsPort)
		metadata["fingerprint"] = gateway.ServerFingerprint()
	}
	if err := advertiser.Start(lan.DefaultDeviceName(), configuration.port, metadata); err != nil {
		fmt.Fprintln(stderr, "warning: LAN discovery unavailable:", err)
	} else {
		defer advertiser.Close()
	}

	serveError := make(chan error, 1)
	go func() {
		serveError <- httpServer.Serve(listener)
	}()

	select {
	case err := <-serveError:
		if tlsServer != nil {
			_ = tlsServer.Close()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "server error:", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			fmt.Fprintln(stderr, "server shutdown:", err)
		}
		if tlsServer != nil {
			if err := tlsServer.Shutdown(shutdownCtx); err != nil {
				_ = tlsServer.Close()
				fmt.Fprintln(stderr, "TLS server shutdown:", err)
			}
			if tlsServeError != nil {
				if err := <-tlsServeError; err != nil && !errors.Is(err, http.ErrServerClosed) {
					fmt.Fprintln(stderr, "TLS server error:", err)
				}
			}
		}
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "server error:", err)
			return 1
		}
		return 0
	}
}

func runConnect(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("llmbeam connect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var host, listen, fingerprint string
	var timeout time.Duration
	flags.StringVar(&host, "host", "", "LLMBeam host (host:port or URL); skip mDNS discovery")
	flags.StringVar(&listen, "listen", "127.0.0.1:0", "local loopback listen address")
	flags.StringVar(&fingerprint, "fingerprint", "", "expected SHA-256 TLS certificate fingerprint for --host https://...")
	flags.DurationVar(&timeout, "timeout", 3*time.Second, "mDNS discovery timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "error: unexpected arguments:", flags.Args())
		return 2
	}
	if timeout <= 0 {
		fmt.Fprintln(stderr, "error: timeout must be positive")
		return 2
	}

	baseURL := strings.TrimSpace(host)
	serverFingerprint := strings.TrimSpace(fingerprint)
	if baseURL == "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		peers, err := lan.NewDiscoverer().Discover(ctx)
		cancel()
		if err != nil {
			fmt.Fprintln(stderr, "error: LAN discovery failed:", err)
			fmt.Fprintln(stderr, "hint:", connectHostHint())
			return 1
		}
		if len(peers) == 0 {
			fmt.Fprintln(stderr, "No LLMBeam hosts found on the local network.")
			fmt.Fprintln(stderr, "hint:", connectHostHint())
			return 1
		}
		fmt.Fprintln(stdout, "Found LLMBeam hosts:")
		for index, peer := range peers {
			address := ""
			if len(peer.Addresses) > 0 {
				address = net.JoinHostPort(peer.Addresses[0].String(), strconv.Itoa(peer.Port))
			}
			fmt.Fprintf(stdout, "  %d. %-24s %s\n", index+1, peer.Name, address)
		}
		selection, err := readConnectLine(stdin, stdout, "Select host: ")
		if err != nil {
			fmt.Fprintln(stderr, "error: cannot read host selection:", err)
			return 1
		}
		index, err := strconv.Atoi(selection)
		if err != nil || index < 1 || index > len(peers) {
			fmt.Fprintln(stderr, "error: invalid host selection")
			return 1
		}
		peer := peers[index-1]
		if len(peer.Addresses) == 0 {
			fmt.Fprintln(stderr, "error: selected host has no usable LAN address")
			return 1
		}
		if peer.TLSPort > 0 && peer.Fingerprint != "" {
			baseURL = "https://" + net.JoinHostPort(peer.Addresses[0].String(), strconv.Itoa(peer.TLSPort))
			serverFingerprint = peer.Fingerprint
		} else {
			baseURL = "http://" + net.JoinHostPort(peer.Addresses[0].String(), strconv.Itoa(peer.Port))
			fmt.Fprintln(stderr, "warning: selected host does not advertise a pinned TLS connector; using HTTP")
		}
	}
	baseURL, err := connect.NormalizeHost(baseURL)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	code, err := readConnectLine(stdin, stdout, "Enter 6-character Connect Code: ")
	if err != nil {
		fmt.Fprintln(stderr, "error: cannot read Connect Code:", err)
		return 1
	}
	var client *connect.Client
	if strings.HasPrefix(baseURL, "https://") {
		client, err = connect.NewPinned(baseURL, serverFingerprint, nil)
	} else {
		client, err = connect.New(baseURL, nil)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 2
	}
	pairCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	session, err := client.Pair(pairCtx, code)
	cancel()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if serverFingerprint == "" {
		serverFingerprint = session.Fingerprint
	}
	if store, storeErr := connect.NewSessionStore(""); storeErr == nil {
		if storeErr = store.Save(connect.StoredSession{Host: baseURL, Fingerprint: serverFingerprint, Session: session}); storeErr != nil {
			fmt.Fprintln(stderr, "warning: could not save connector session:", storeErr)
		}
	} else {
		fmt.Fprintln(stderr, "warning: could not initialize connector session store:", storeErr)
	}
	modelsCtx, modelsCancel := context.WithTimeout(context.Background(), 10*time.Second)
	models, modelsErr := client.Models(modelsCtx)
	modelsCancel()
	if modelsErr != nil {
		fmt.Fprintln(stderr, "warning: could not retrieve models:", modelsErr)
	} else {
		printConnectModels(stdout, models)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, localAPIKey, err := client.ListenWithAPIKey(ctx, listen)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	defer listener.Close()
	fmt.Fprintln(stdout, "\nConnected.")
	fmt.Fprintf(stdout, "Local OpenAI endpoint:\n  http://%s/v1\n", listener.Addr().String())
	fmt.Fprintf(stdout, "Local API key:\n  %s\n", localAPIKey)
	fmt.Fprintf(stdout, "Connector session expires: %s\n", session.Expires.Local().Format(time.RFC3339))
	<-ctx.Done()
	return 0
}

func printConnectModels(stdout io.Writer, models []connect.Model) {
	fmt.Fprintln(stdout, "Available models:")
	if len(models) == 0 {
		fmt.Fprintln(stdout, "  (none currently available)")
		return
	}
	for _, model := range models {
		fmt.Fprintf(stdout, "  - %s\n", model.ID)
	}
}

func readConnectLine(stdin io.Reader, stdout io.Writer, prompt string) (string, error) {
	fmt.Fprint(stdout, prompt)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line != "" && errors.Is(err, io.EOF) {
		return line, nil
	}
	return line, err
}

// connectHostHint returns the actionable manual fallback shown when mDNS is
// unavailable. Keeping it centralized ensures all discovery failures provide
// the same documented command.
func connectHostHint() string {
	return "llmbeam connect --host <ip>:8442"
}

func parseConfig(args []string, stderr io.Writer) (config, error) {
	var configuration config
	flags := flag.NewFlagSet(AppName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&configuration.port, "port", 8442, "listen port")
	flags.BoolVar(&configuration.noQR, "no-qr", false, "suppress QR code output")
	flags.DurationVar(&configuration.codeTTL, "code-ttl", 10*time.Minute, "pairing code validity")
	flags.BoolVar(&configuration.showVer, "version", false, "print version and exit")
	flags.BoolVar(&configuration.remote, "remote", false, "enable experimental Tailcat remote access")
	flags.StringVar(&configuration.remoteURL, "remote-url", defaultRemoteWebURL, "public LLMBeam Connect page")
	flags.Var(&configuration.backends, "backend", "extra OpenAI-compatible base URL (repeatable)")
	if err := flags.Parse(args); err != nil {
		return config{}, err
	}
	if flags.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if configuration.port < 1 || configuration.port > 65535 {
		return config{}, fmt.Errorf("port must be between 1 and 65535")
	}
	if configuration.codeTTL <= 0 {
		return config{}, fmt.Errorf("code-ttl must be positive")
	}
	remoteURL, err := url.Parse(configuration.remoteURL)
	if err != nil || remoteURL.Scheme != "https" || remoteURL.Host == "" || remoteURL.RawQuery != "" || remoteURL.Fragment != "" {
		return config{}, fmt.Errorf("remote-url must be an HTTPS URL without query or fragment")
	}
	return configuration, nil
}

type terminalOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

func (output *terminalOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.writer.Write(data)
}

func (output *terminalOutput) block(write func(io.Writer)) {
	output.mu.Lock()
	defer output.mu.Unlock()
	write(output.writer)
}

func (output *terminalOutput) printDiscovery(results []backend.ProbeResult) {
	output.block(func(w io.Writer) {
		fmt.Fprintln(w, "  Discovered backends:")
		available := 0
		for _, result := range results {
			mark, status := "✗", "(not running)"
			if result.Up {
				available++
				mark = "✓"
				status = fmt.Sprintf("(%d models)", result.ModelCount)
			}
			warning := ""
			if !result.Candidate.Loopback {
				warning = "  warning: non-loopback upstream"
			}
			fmt.Fprintf(w, "    %s %-11s %-30s %s%s\n",
				mark, result.Candidate.ID, result.Candidate.BaseURL, status, warning)
		}
		if available == 0 {
			fmt.Fprintln(w, "\n  No backend is running yet; model discovery will retry every 30s.")
		}
	})
}

func (output *terminalOutput) printConnectorCode(code string, expires time.Time) {
	if len(code) != 6 {
		return
	}
	output.block(func(w io.Writer) {
		fmt.Fprintf(w, "\n  LAN connector code: %s-%s\n", code[:3], code[3:])
		fmt.Fprintln(w, "  Run `llmbeam connect` on another computer, then enter this code.")
		remaining := time.Until(expires).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(w, "  Connector code expires in %s.\n", remaining)
	})
}

type pairingAccess struct {
	localURL     string
	remoteWebURL string
	tailcatToken string
}

func (access pairingAccess) pairURL(code string) string {
	if access.tailcatToken != "" {
		return fmt.Sprintf("%s/#/connect/%s/%s", strings.TrimRight(access.remoteWebURL, "/"), access.tailcatToken, code)
	}
	return fmt.Sprintf("%s/#/pair/%s", access.localURL, code)
}

func (output *terminalOutput) printPairing(update pair.CodeUpdate, access pairingAccess, noQR bool) {
	pairURL := access.pairURL(update.Code)
	columns, rows := terminalDimensions(output.writer)
	layout := chooseQRLayout(pairURL, columns, rows)
	output.block(func(w io.Writer) {
		if !noQR && layout.show {
			fmt.Fprintln(w)
			if access.tailcatToken == "" {
				fmt.Fprintln(w, "  Scan with your phone (same Wi-Fi):")
			} else {
				fmt.Fprintln(w, "  Scan with your phone (works from any network):")
			}
			fmt.Fprintln(w)
			qrterminal.GenerateWithConfig(pairURL, qrterminal.Config{
				Level:      qrterminal.L,
				Writer:     w,
				HalfBlocks: true,
				QuietZone:  layout.quietZone,
			})
		} else if !noQR {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  Terminal too small for a scannable QR code; use the address below.")
		}
		fmt.Fprintf(w, "\n  Local:   %s  code  %s-%s\n",
			access.localURL, update.Code[:4], update.Code[4:])
		if access.tailcatToken != "" {
			fmt.Fprintf(w, "  Remote:  %s\n", pairURL)
			fmt.Fprintln(w, "  Remote access is experimental and relayed through Tailcat DERP.")
		}
		remaining := time.Until(update.Expires).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(w, "  Code expires in %s. Ctrl-C to quit.\n\n", remaining)
	})
}

const pairingOutputRows = 7

type qrLayout struct {
	show      bool
	quietZone int
}

func chooseQRLayout(payload string, columns, rows int) qrLayout {
	standard := qrLayout{show: true, quietZone: 2}
	if columns <= 0 || rows <= 0 {
		return standard
	}
	code, err := qr.Encode(payload, qr.L)
	if err != nil {
		return standard
	}
	for _, layout := range []qrLayout{standard, {show: true, quietZone: 1}} {
		width, height := layout.dimensions(code.Size)
		if width <= columns && height+pairingOutputRows <= rows {
			return layout
		}
	}
	if columns >= code.Size+2 {
		return qrLayout{show: true, quietZone: 1}
	}
	return qrLayout{}
}

func (layout qrLayout) dimensions(moduleSize int) (int, int) {
	width := moduleSize + 2*layout.quietZone
	height := (moduleSize + 2*layout.quietZone + 1) / 2
	return width, height
}

func terminalDimensions(writer io.Writer) (int, int) {
	file, ok := writer.(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return 0, 0
	}
	columns, rows, err := term.GetSize(int(file.Fd()))
	if err != nil {
		return 0, 0
	}
	return columns, rows
}

func watchPairingCodes(
	ctx context.Context,
	manager *pair.Manager,
	current pair.CodeUpdate,
	terminal *terminalOutput,
	access pairingAccess,
	noQR bool,
) {
	updates := manager.CodeUpdates()
	for {
		delay := time.Until(current.Expires)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case next := <-updates:
			timer.Stop()
			current = next
			terminal.printPairing(current, access, noQR)
		case <-timer.C:
			manager.Code()
			select {
			case <-ctx.Done():
				return
			case current = <-updates:
				terminal.printPairing(current, access, noQR)
			}
		}
	}
}
