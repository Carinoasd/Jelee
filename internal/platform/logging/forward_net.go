package logging

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
)

// classifyNetError turns a transport error into a deliverError with a fixed
// class word; the error text itself may hold addresses and is not kept.
func classifyNetError(err error) error {
	class := "network"
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var host x509.HostnameError
	var ne net.Error
	switch {
	case errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &host):
		class = "tls"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		class = "timeout"
	}
	return &deliverError{class: class, retry: true}
}
