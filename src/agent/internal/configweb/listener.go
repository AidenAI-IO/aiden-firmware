package configweb

import (
	"context"
	"net"
	"net/http"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type usbIngressContextKey struct{}

func configHTTPServer(address string, handler http.Handler, usb bool) *http.Server {
	return &http.Server{
		Addr: address, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 65 * time.Second,
		WriteTimeout: 65 * time.Second, IdleTimeout: 60 * time.Second,
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context {
			return context.WithValue(ctx, usbIngressContextKey{}, usb)
		},
	}
}

func listenConfigWeb(address, device string) (net.Listener, error) {
	config := net.ListenConfig{Control: func(_, _ string, connection syscall.RawConn) error {
		var socketErr error
		if err := connection.Control(func(fd uintptr) {
			socketErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
			if socketErr == nil && device != "" {
				socketErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, device)
			}
		}); err != nil {
			return err
		}
		return socketErr
	}}
	return config.Listen(context.Background(), "tcp4", address)
}
