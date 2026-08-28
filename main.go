package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/shao-hua-li/scanchat/internal/backend"
	"github.com/shao-hua-li/scanchat/internal/netutil"
	"github.com/shao-hua-li/scanchat/internal/pair"
	"github.com/shao-hua-li/scanchat/internal/server"
	"github.com/shao-hua-li/scanchat/internal/ui"
)

const AppName = "scanchat"

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
	port     int
	noQR     bool
	codeTTL  time.Duration
	showVer  bool
	backends stringSlice
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	initialCode := <-pairs.CodeUpdates()
	terminal.printPairing(initialCode, baseURL, configuration.noQR)
	go watchPairingCodes(ctx, pairs, initialCode, terminal, baseURL, configuration.noQR)

	slog.SetDefault(slog.New(slog.NewTextHandler(terminal, nil)))
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(configuration.port))
	httpServer := &http.Server{
		Addr:              address,
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}

	serveError := make(chan error, 1)
	go func() {
		serveError <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-serveError:
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
		if err := <-serveError; err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintln(stderr, "server error:", err)
			return 1
		}
		return 0
	}
}

func parseConfig(args []string, stderr io.Writer) (config, error) {
	var configuration config
	flags := flag.NewFlagSet(AppName, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.IntVar(&configuration.port, "port", 8442, "listen port")
	flags.BoolVar(&configuration.noQR, "no-qr", false, "suppress QR code output")
	flags.DurationVar(&configuration.codeTTL, "code-ttl", 10*time.Minute, "pairing code validity")
	flags.BoolVar(&configuration.showVer, "version", false, "print version and exit")
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

func (output *terminalOutput) printPairing(update pair.CodeUpdate, baseURL string, noQR bool) {
	pairURL := fmt.Sprintf("%s/#/pair/%s", baseURL, update.Code)
	output.block(func(w io.Writer) {
		if !noQR {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "  Scan with your phone (same Wi-Fi):")
			fmt.Fprintln(w)
			qrterminal.GenerateWithConfig(pairURL, qrterminal.Config{
				Level:     qrterminal.L,
				Writer:    w,
				BlackChar: qrterminal.BLACK,
				WhiteChar: qrterminal.WHITE,
				QuietZone: 2,
			})
		}
		fmt.Fprintf(w, "\n  Or open  %s  and enter code  %s-%s\n",
			baseURL, update.Code[:4], update.Code[4:])
		remaining := time.Until(update.Expires).Round(time.Second)
		if remaining < 0 {
			remaining = 0
		}
		fmt.Fprintf(w, "  Code expires in %s. Ctrl-C to quit.\n\n", remaining)
	})
}

func watchPairingCodes(
	ctx context.Context,
	manager *pair.Manager,
	current pair.CodeUpdate,
	terminal *terminalOutput,
	baseURL string,
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
			terminal.printPairing(current, baseURL, noQR)
		case <-timer.C:
			manager.Code()
			select {
			case <-ctx.Done():
				return
			case current = <-updates:
				terminal.printPairing(current, baseURL, noQR)
			}
		}
	}
}
