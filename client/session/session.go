/*
 Copyright 2013-2014 Canonical Ltd.

 This program is free software: you can redistribute it and/or modify it
 under the terms of the GNU General Public License version 3, as published
 by the Free Software Foundation.

 This program is distributed in the hope that it will be useful, but
 WITHOUT ANY WARRANTY; without even the implied warranties of
 MERCHANTABILITY, SATISFACTORY QUALITY, or FITNESS FOR A PARTICULAR
 PURPOSE.  See the GNU General Public License for more details.

 You should have received a copy of the GNU General Public License along
 with this program.  If not, see <http://www.gnu.org/licenses/>.
*/

// The client/session package handles the minutiae of interacting with
// the Ubuntu Push Notifications server.
package session

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"launchpad.net/ubuntu-push/client/session/levelmap"
	"launchpad.net/ubuntu-push/logger"
	"launchpad.net/ubuntu-push/protocol"
	"net"
	"time"
)

var wireVersionBytes = []byte{protocol.ProtocolWireVersion}

type Notification struct {
	// something something something
}

// ClienSession holds a client<->server session and its configuration.
type ClientSession struct {
	// configuration
	DeviceId        string
	ServerAddr      string
	ExchangeTimeout time.Duration
	Levels          levelmap.LevelMap
	Protocolator    func(net.Conn) protocol.Protocol
	// connection
	Connection   net.Conn
	Log          logger.Logger
	TLS          *tls.Config
	proto        protocol.Protocol
	pingInterval time.Duration
	// status
	ErrCh chan error
	MsgCh chan *Notification
}

func NewSession(serverAddr string, pem []byte, exchangeTimeout time.Duration,
	deviceId string, log logger.Logger) (*ClientSession, error) {
	sess := &ClientSession{
		ExchangeTimeout: exchangeTimeout,
		ServerAddr:      serverAddr,
		DeviceId:        deviceId,
		Log:             log,
		Protocolator:    protocol.NewProtocol0,
		Levels:          levelmap.NewLevelMap(),
		TLS:             &tls.Config{InsecureSkipVerify: true}, // XXX
	}
	if pem != nil {
		cp := x509.NewCertPool()
		ok := cp.AppendCertsFromPEM(pem)
		if !ok {
			return nil, errors.New("could not parse certificate")
		}
		sess.TLS.RootCAs = cp
	}
	return sess, nil
}

// Dial connects to a server using the configuration in the ClientSession
// and sets up the connection.
func (sess *ClientSession) Dial() error {
	conn, err := net.DialTimeout("tcp", sess.ServerAddr, sess.ExchangeTimeout)
	if err != nil {
		return err
	}
	sess.Connection = tls.Client(conn, sess.TLS)
	return nil
}

func (sess *ClientSession) Close() {
	if sess.Connection != nil {
		sess.Connection.Close()
		// we ignore Close errors, on purpose (the thinking being that
		// the connection isn't really usable, and you've got nothing
		// you could do to recover at this stage).
		sess.Connection = nil
	}
}

// call this to ensure the session is sane to run
func (sess *ClientSession) checkRunnable() error {
	if sess.Connection == nil {
		return errors.New("can't run disconnected.")
	}
	if sess.Protocolator == nil {
		return errors.New("can't run without a protocol constructor.")
	}
	return nil
}