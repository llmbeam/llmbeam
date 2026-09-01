package remote

import (
	"net"
	"net/http"

	"github.com/tailscale/tailcat"
	_ "tailscale.com/feature/condregister/useproxy"
)

const tunnelPort = 443

type Server struct {
	tailcat *tailcat.Server
}

func Start(handler http.Handler) (*Server, error) {
	tailcatServer := &tailcat.Server{
		Logf: func(string, ...any) {},
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
