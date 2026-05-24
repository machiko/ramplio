package dashboard

import (
	"fmt"
	"net"
)

func newListener(port int) (net.Listener, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("dashboard: cannot bind :%d: %w", port, err)
	}
	return ln, nil
}
