package remote

import (
	"log"
	"net"
	"net/http"
	"os"

	"github.com/tailscale/tailcat"
	_ "tailscale.com/feature/condregister/useproxy"
)

const tunnelPort = 443

type Server struct {
	tailcat *tailcat.Server
}

func Start(handler http.Handler) (*Server, error) {
	verbose := os.Getenv("LLMBEAM_TAILCAT_VERBOSE") != ""
	tailcatServer := &tailcat.Server{
		Logf: func(format string, args ...any) {
			if verbose {
				log.Printf("tailcat: "+format, args...)
			}
		},
		OnTCP: func(port uint16) func(net.Conn) {
			if port != tunnelPort {
				return nil
			}
			return func(connection net.Conn) {
				serveConnection(handler, connection)
			}
		},
	}
	if err := tailcatServer.Start(); err != nil {
		return nil, err
	}
	return &Server{tailcat: tailcatServer}, nil
}

func (server *Server) Token() string {
	return string(server.tailcat.ConnBlob())
}

func (server *Server) Close() error {
	return server.tailcat.Close()
}
