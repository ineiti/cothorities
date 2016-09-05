package network

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"golang.org/x/net/context"
)

// NewLocalRouter returns a fresh router which uses local connections. It takes
// the default manager.
func NewLocalRouter(sid *ServerIdentity) (*Router, error) {
	return NewLocalRouterWithManager(defaultLocalManager, sid)
}

// NewLocalRouterWithmanager is the same as NewLocalRouter but takes a specific
// Localmanager. This is useful to run parallel different local overlays.
func NewLocalRouterWithManager(ctx *LocalManager, sid *ServerIdentity) (*Router, error) {
	h, err := NewLocalHostWithManager(ctx, sid.Address)
	if err != nil {
		return nil, err
	}
	return NewRouter(sid, h), nil
}

// LocalManager keeps reference to all opened local connections
// It also keeps tracks of who is "listening", so it's possible to mimics
// Conn & Listener.
type LocalManager struct {
	// queues maps a remote endpoint to its packet queue. It's the main
	//stucture used to communicate.
	queues map[endpoint]*connQueue
	sync.Mutex
	listening map[Address]func(Conn)

	baseUID uint64
}

// NewLocalManager returns a fresh new manager that can be used by LocalConn,
// LocalListener & LocalHost.
func NewLocalManager() *LocalManager {
	return &LocalManager{
		queues:    make(map[endpoint]*connQueue),
		listening: make(map[Address]func(Conn)),
	}
}

var defaultLocalManager = NewLocalManager()

// endpoint represents one endpoint of a connection.
type endpoint struct {
	addr Address
	// uid is a unique identifier of the remote endpoint
	// it's unique  for each direction:
	// 127.0.0.1:2000 -> 127.0.0.1:2000 => 14
	// 127.0.0.1:2000 <- 127.0.0.1:2000 => 15
	uid uint64
}

// LocalReset reset the whole map of connections + listener so it is like
// a fresh defaultLocalManager.
func LocalReset() {
	defaultLocalManager = NewLocalManager()

}

// isListening returns true if the remote address is listening "virtually"
func (lm *LocalManager) isListening(remote Address) bool {
	lm.Lock()
	defer lm.Unlock()
	_, ok := lm.listening[remote]
	return ok
}

// setListening marks the address as being able to accept incoming connection.
// For each incoming connection, fn will be called in a go routine.
func (lm *LocalManager) setListening(addr Address, fn func(Conn)) {
	lm.Lock()
	defer lm.Unlock()
	lm.listening[addr] = fn
}

// unsetListening marks the address as *not* being able to accept incoming
// connections.
func (lm *LocalManager) unsetListening(addr Address) {
	lm.Lock()
	defer lm.Unlock()
	delete(lm.listening, addr)
}

// connect will check if the remote address is listening, if yes it creates
// the two connections, and launch the listening function in a go routine.
// It returns the outgoing connection with any error.
func (lm *LocalManager) connect(local, remote Address) (*LocalConn, error) {
	lm.Lock()
	defer lm.Unlock()

	fn, ok := lm.listening[remote]
	if !ok {
		return nil, fmt.Errorf("%s can't connect to %s: it's not listening", local, remote)
	}

	outEndpoint := endpoint{local, lm.baseUID}
	lm.baseUID++

	incEndpoint := endpoint{remote, lm.baseUID}
	lm.baseUID++

	outgoing := newLocalConn(lm, outEndpoint, incEndpoint)
	incoming := newLocalConn(lm, incEndpoint, outEndpoint)

	// outgoing knows how to store packet into the incoming's queue
	lm.queues[outEndpoint] = outgoing.connQueue
	// incoming knows how to store packet into the outgoing's queue
	lm.queues[incEndpoint] = incoming.connQueue

	go fn(incoming)
	return outgoing, nil
}

// send will get the connection denoted by this endpoint and will call queueMsg
// with the packet as argument on it. It returns ErrClosed if it does not find
// the connection.
func (lm *LocalManager) send(e endpoint, nm Packet) error {
	lm.Lock()
	defer lm.Unlock()
	q, ok := lm.queues[e]
	if !ok {
		return ErrClosed
	}

	q.push(nm)
	return nil
}

// close will get the connection denoted by this endpoint and will Close it if
// present.
func (lm *LocalManager) close(conn *LocalConn) {
	lm.Lock()
	defer lm.Unlock()
	// delete this conn
	delete(lm.queues, conn.local)
	// and delete the remote one + close it
	remote, ok := lm.queues[conn.remote]
	if !ok {
		return
	}
	delete(lm.queues, conn.remote)
	remote.close()
}

// len returns how many local connections is there
func (lm *LocalManager) len() int {
	lm.Lock()
	defer lm.Unlock()
	return len(lm.queues)
}

// LocalConn is a connection that send and receive messages to other
// connections locally
type LocalConn struct {
	local  endpoint
	remote endpoint

	// connQueue is the part that is accesible from the LocalManager (i.e. is
	// shared). Reason why we can't directly share LocalConn is because go test
	// -race detects as data race (while it's *protected*)
	*connQueue

	manager *LocalManager
}

// newLocalConn simply init the fields of a LocalConn but do not try to
// connect. It should not be used as-is, most user wants to call NewLocalConn.
func newLocalConn(ctx *LocalManager, local, remote endpoint) *LocalConn {
	return &LocalConn{
		remote:    remote,
		local:     local,
		connQueue: newConnQueue(),
		manager:   ctx,
	}
}

// NewLocalConn returns a new channel connection from local to remote
// Mimics the behavior of NewTCPConn => tries connecting right away.
// It uses the default local manager.
func NewLocalConn(local, remote Address) (*LocalConn, error) {
	return NewLocalConnWithManager(defaultLocalManager, local, remote)
}

// NewLocalConnWithManager is similar to NewLocalConn but takes a specific
// LocalManager.
func NewLocalConnWithManager(ctx *LocalManager, local, remote Address) (*LocalConn, error) {
	return ctx.connect(local, remote)
}

// Send implements the Conn interface.
func (lc *LocalConn) Send(ctx context.Context, msg Body) error {

	var body Body
	var val = reflect.ValueOf(msg)
	if val.Kind() == reflect.Ptr {
		val = val.Elem()
	}
	body = val.Interface()

	var typ = TypeFromData(body)
	nm := Packet{
		MsgType: typ,
		Msg:     body,
	}
	return lc.manager.send(lc.remote, nm)
}

// Receive implements the Conn interface.
func (lc *LocalConn) Receive(ctx context.Context) (Packet, error) {
	return lc.pop()
}

// Local implements the Conn interface
func (lc *LocalConn) Local() Address {
	return lc.local.addr
}

// Remote implements the Conn interface
func (lc *LocalConn) Remote() Address {
	return lc.remote.addr
}

// Close implements the Conn interface
func (lc *LocalConn) Close() error {
	lc.connQueue.close()
	// close the remote conn also
	lc.manager.close(lc)
	return nil
}

// Rx implements the Conn interface
func (lc *LocalConn) Rx() uint64 {
	return 0
}

// Tx implements the Conn interface
func (lc *LocalConn) Tx() uint64 {
	return 0
}

// Type implements the Conn interface
func (lc *LocalConn) Type() ConnType {
	return Local
}

// connQueue is a struct that managers the message queue of a LocalConn.
// Messages are pushed and retrieved FIFO style.
// All operations are thread-safe.
type connQueue struct {
	*sync.Cond
	queue    []Packet
	isClosed bool
}

func newConnQueue() *connQueue {
	return &connQueue{
		Cond: sync.NewCond(&sync.Mutex{}),
	}
}

// push insert the packet into the queue.
func (c *connQueue) push(p Packet) {
	c.L.Lock()
	defer c.L.Unlock()
	if c.isClosed {
		return
	}
	c.queue = append(c.queue, p)
	c.Signal()
}

// pop retrieve a packet out of the queue.
// If there is no messages, then it blocks UNTIL there is a call to push() or
// close(). pop also returns directly in case this queue is already closed.
func (c *connQueue) pop() (Packet, error) {
	c.L.Lock()
	defer c.L.Unlock()
	for len(c.queue) == 0 {
		if c.isClosed {
			return EmptyApplicationPacket, ErrClosed
		}
		c.Wait()
	}
	if c.isClosed {
		return EmptyApplicationPacket, ErrClosed
	}
	nm := c.queue[0]
	c.queue = c.queue[1:]
	return nm, nil
}

// close set that queue to be unusable and signal every current pop() operations
// to return.
func (c *connQueue) close() {
	c.L.Lock()
	defer c.L.Unlock()
	c.isClosed = true
	c.Broadcast()
}

// LocalListener is a Listener that uses LocalConn to communicate. It tries to
// behave as much as possible as a real golang net.Listener but using LocalConn
// as the underlying communication layer.
type LocalListener struct {
	// addr is the addr we're listening to
	addr Address
	// are we listening or not
	listening bool

	sync.Mutex

	// quit is used to stop the listening routine
	quit chan bool

	manager *LocalManager
}

// NewLocalListener returns a fresh LocalListener which implements the Listener
// interface.
func NewLocalListener(addr Address) (*LocalListener, error) {
	return NewLocalListenerWithManager(defaultLocalManager, addr)
}

// NewLocalListenerWithManager is similar to NewLocalListener but taking a
// specific LocalManager to use to communicate.
func NewLocalListenerWithManager(ctx *LocalManager, addr Address) (*LocalListener, error) {
	l := &LocalListener{
		quit:    make(chan bool),
		manager: ctx,
	}
	return l, l.bind(addr)
}

func (ll *LocalListener) bind(addr Address) error {
	ll.Lock()
	defer ll.Unlock()
	if addr.ConnType() != Local {
		return errors.New("Wrong address type for local listener")
	}
	if ll.manager.isListening(addr) {
		return fmt.Errorf("%s is already listening: can't listen again", addr)
	}
	ll.addr = addr
	return nil
}

// Listen implements the Listener interface
func (ll *LocalListener) Listen(fn func(Conn)) error {
	ll.Lock()
	ll.quit = make(chan bool)
	ll.manager.setListening(ll.addr, fn)
	ll.listening = true
	ll.Unlock()

	<-ll.quit
	return nil
}

// Stop implements the Listener interface
func (ll *LocalListener) Stop() error {
	ll.Lock()
	defer ll.Unlock()
	ll.manager.unsetListening(ll.addr)
	if ll.listening {
		close(ll.quit)
	}
	ll.listening = false
	return nil
}

// Address returns the listening address used.
func (ll *LocalListener) Address() Address {
	ll.Lock()
	defer ll.Unlock()
	return ll.addr
}

// Listening returns true if this Listener is actually listening for any
// incoming connections.
func (ll *LocalListener) Listening() bool {
	ll.Lock()
	defer ll.Unlock()
	return ll.listening
}

// LocalHost is a Host implementation using LocalConn and LocalListener as
// the underlying means of communication. It is a implementation of the Host
// interface.
type LocalHost struct {
	addr Address
	*LocalListener
	ctx *LocalManager
}

// NewLocalHost returns a fresh Host using Local communication that will listen
// on the given addr.
func NewLocalHost(addr Address) (*LocalHost, error) {
	return NewLocalHostWithManager(defaultLocalManager, addr)
}

// NewLocalHostWithManager is similar to NewLocalHost but specify which
// LocalManager to use for communicating.
func NewLocalHostWithManager(ctx *LocalManager, addr Address) (*LocalHost, error) {
	lh := &LocalHost{
		addr: addr,
		ctx:  ctx,
	}
	var err error
	lh.LocalListener, err = NewLocalListenerWithManager(ctx, addr)
	return lh, err

}

// Connect implements the Host interface.
func (lh *LocalHost) Connect(addr Address) (Conn, error) {
	if addr.ConnType() != Local {
		return nil, errors.New("Can't connect to non-Local address")
	}
	var finalErr error
	for i := 0; i < MaxRetryConnect; i++ {
		c, err := NewLocalConnWithManager(lh.ctx, lh.addr, addr)
		if err == nil {
			return c, nil
		}
		finalErr = err
		time.Sleep(WaitRetry)
	}
	return nil, finalErr

}

// NewLocalClient returns Client that uses the Local communication layer.
func NewLocalClient() *Client {
	return NewLocalClientWithManager(defaultLocalManager)
}

// NewLocalClientWithManager is similar to NewLocalClient but takes a specific
// LocalManager to communicate.
func NewLocalClientWithManager(ctx *LocalManager) *Client {
	fn := func(own, remote *ServerIdentity) (Conn, error) {
		return NewLocalConnWithManager(ctx, own.Address, remote.Address)
	}
	return newClient(fn)

}

// NewLocalAddress returns an Address of type Local with the given raw addr.
func NewLocalAddress(addr string) Address {
	return NewAddress(Local, addr)
}

/*// GetStatus implements the Host interface*/
//func (l *chanHost) GetStatus() Status {
//m := make(map[string]string)
//m["Connections"] = strings.Join(l.conns.Get(), "\n")
//m["Host"] = l.Address()
//m["Total"] = strconv.Itoa(l.conns.Len())
//m["Packets_Received"] = strconv.FormatUint(0, 10)
//m["Packets_Sent"] = strconv.FormatUint(0, 10)
//return m
//}
