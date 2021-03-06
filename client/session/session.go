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
	"fmt"
	"launchpad.net/ubuntu-push/client/session/levelmap"
	"launchpad.net/ubuntu-push/logger"
	"launchpad.net/ubuntu-push/protocol"
	"launchpad.net/ubuntu-push/util"
	"math/rand"
	"net"
	"sync/atomic"
	"time"
)

var wireVersionBytes = []byte{protocol.ProtocolWireVersion}

type Notification struct {
	// something something something
}

type serverMsg struct {
	Type string `json:"T"`
	protocol.BroadcastMsg
	protocol.NotificationsMsg
}

// ClientSessionState is a way to broadly track the progress of the session
type ClientSessionState uint32

const (
	Error ClientSessionState = iota
	Disconnected
	Connected
	Started
	Running
)

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
	retrier      util.AutoRedialer
	// status
	stateP *uint32
	ErrCh  chan error
	MsgCh  chan *Notification
}

func NewSession(serverAddr string, pem []byte, exchangeTimeout time.Duration,
	deviceId string, log logger.Logger) (*ClientSession, error) {
	state := uint32(Disconnected)
	sess := &ClientSession{
		ExchangeTimeout: exchangeTimeout,
		ServerAddr:      serverAddr,
		DeviceId:        deviceId,
		Log:             log,
		Protocolator:    protocol.NewProtocol0,
		Levels:          levelmap.NewLevelMap(),
		TLS:             &tls.Config{InsecureSkipVerify: true}, // XXX
		stateP:          &state,
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

func (sess *ClientSession) State() ClientSessionState {
	return ClientSessionState(atomic.LoadUint32(sess.stateP))
}

func (sess *ClientSession) setState(state ClientSessionState) {
	atomic.StoreUint32(sess.stateP, uint32(state))
}

// connect to a server using the configuration in the ClientSession
// and set up the connection.
func (sess *ClientSession) connect() error {
	conn, err := net.DialTimeout("tcp", sess.ServerAddr, sess.ExchangeTimeout)
	if err != nil {
		sess.setState(Error)
		return fmt.Errorf("connect: %s", err)
	}
	sess.Connection = tls.Client(conn, sess.TLS)
	sess.setState(Connected)
	return nil
}

func (sess *ClientSession) stopRedial() {
	if sess.retrier != nil {
		sess.retrier.Stop()
		sess.retrier = nil
	}
}

func (sess *ClientSession) AutoRedial(doneCh chan uint32) {
	sess.stopRedial()
	sess.retrier = util.NewAutoRedialer(sess)
	go func() { doneCh <- sess.retrier.Redial() }()
}

func (sess *ClientSession) Close() {
	sess.stopRedial()
	sess.doClose()
}
func (sess *ClientSession) doClose() {
	if sess.Connection != nil {
		sess.Connection.Close()
		// we ignore Close errors, on purpose (the thinking being that
		// the connection isn't really usable, and you've got nothing
		// you could do to recover at this stage).
		sess.Connection = nil
	}
	sess.setState(Disconnected)
}

// handle "ping" messages
func (sess *ClientSession) handlePing() error {
	err := sess.proto.WriteMessage(protocol.PingPongMsg{Type: "pong"})
	if err == nil {
		sess.Log.Debugf("ping.")
	} else {
		sess.setState(Error)
		sess.Log.Errorf("unable to pong: %s", err)
	}
	return err
}

// handle "broadcast" messages
func (sess *ClientSession) handleBroadcast(bcast *serverMsg) error {
	err := sess.proto.WriteMessage(protocol.AckMsg{"ack"})
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to ack broadcast: %s", err)
		return err
	}
	sess.Log.Debugf("broadcast chan:%v app:%v topLevel:%d payloads:%s",
		bcast.ChanId, bcast.AppId, bcast.TopLevel, bcast.Payloads)
	if bcast.ChanId == protocol.SystemChannelId {
		// the system channel id, the only one we care about for now
		sess.Log.Debugf("sending it over")
		sess.Levels.Set(bcast.ChanId, bcast.TopLevel)
		sess.MsgCh <- &Notification{}
		sess.Log.Debugf("sent it over")
	} else {
		sess.Log.Debugf("what is this weird channel, %#v?", bcast.ChanId)
	}
	return nil
}

// loop runs the session with the server, emits a stream of events.
func (sess *ClientSession) loop() error {
	var err error
	var recv serverMsg
	sess.setState(Running)
	for {
		deadAfter := sess.pingInterval + sess.ExchangeTimeout
		sess.proto.SetDeadline(time.Now().Add(deadAfter))
		err = sess.proto.ReadMessage(&recv)
		if err != nil {
			sess.setState(Error)
			return err
		}
		switch recv.Type {
		case "ping":
			err = sess.handlePing()
		case "broadcast":
			err = sess.handleBroadcast(&recv)
		}
		if err != nil {
			return err
		}
	}
}

// Call this when you've connected and want to start looping.
func (sess *ClientSession) start() error {
	conn := sess.Connection
	err := conn.SetDeadline(time.Now().Add(sess.ExchangeTimeout))
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to start: set deadline: %s", err)
		return err
	}
	_, err = conn.Write(wireVersionBytes)
	// The Writer docs: Write must return a non-nil error if it returns
	// n < len(p). So, no need to check number of bytes written, hooray.
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to start: write version: %s", err)
		return err
	}
	proto := sess.Protocolator(conn)
	proto.SetDeadline(time.Now().Add(sess.ExchangeTimeout))
	err = proto.WriteMessage(protocol.ConnectMsg{
		Type:     "connect",
		DeviceId: sess.DeviceId,
		Levels:   sess.Levels.GetAll(),
	})
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to start: connect: %s", err)
		return err
	}
	var connAck protocol.ConnAckMsg
	err = proto.ReadMessage(&connAck)
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to start: connack: %s", err)
		return err
	}
	if connAck.Type != "connack" {
		sess.setState(Error)
		return fmt.Errorf("expecting CONNACK, got %#v", connAck.Type)
	}
	pingInterval, err := time.ParseDuration(connAck.Params.PingInterval)
	if err != nil {
		sess.setState(Error)
		sess.Log.Errorf("unable to start: parse ping interval: %s", err)
		return err
	}
	sess.proto = proto
	sess.pingInterval = pingInterval
	sess.Log.Debugf("Connected %v.", conn.LocalAddr())
	sess.setState(Started)
	return nil
}

// run calls connect, and if it works it calls start, and if it works
// it runs loop in a goroutine, and ships its return value over ErrCh.
func (sess *ClientSession) run(closer func(), connecter, starter, looper func() error) error {
	closer()
	err := connecter()
	if err == nil {
		err = starter()
		if err == nil {
			sess.ErrCh = make(chan error, 1)
			sess.MsgCh = make(chan *Notification)
			go func() { sess.ErrCh <- looper() }()
		}
	}
	return err
}

// This Jitter returns a random time.Duration somewhere in [-spread, spread].
func (sess *ClientSession) Jitter(spread time.Duration) time.Duration {
	if spread < 0 {
		panic("spread must be non-negative")
	}
	n := int64(spread)
	return time.Duration(rand.Int63n(2*n+1) - n)
}

// Dial takes the session from newly created (or newly disconnected)
// to running the main loop.
func (sess *ClientSession) Dial() error {
	if sess.Protocolator == nil {
		// a missing protocolator means you've willfully overridden
		// it; returning an error here would prompt AutoRedial to just
		// keep on trying.
		panic("can't Dial() without a protocol constructor.")
	}
	return sess.run(sess.doClose, sess.connect, sess.start, sess.loop)
}

func init() {
	rand.Seed(time.Now().Unix()) // good enough for us (we're not using it for crypto)
}
