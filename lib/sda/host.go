/*
Implementation of the Secure Distributed API - main module

Node takes care about
* the network
* pre-parsing incoming packets
* instantiating ProtocolInstances
* passing packets to ProtocolInstances

*/

package sda

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dedis/cothority/lib/cliutils"
	"github.com/dedis/cothority/lib/dbg"
	"github.com/dedis/cothority/lib/network"
	"github.com/dedis/crypto/abstract"
	"github.com/dedis/crypto/config"
	"github.com/satori/go.uuid"
	"golang.org/x/net/context"
)

/*
Host is the structure responsible for holding information about the current
 state
*/
type Host struct {
	// Our entity (i.e. identity over the network)
	Entity *network.Entity
	// Our private-key
	private abstract.Secret
	// The TCPHost
	host network.SecureHost
	// mapper is used to uniquely identify instances + helpers so protocol
	// instances can send easily msg
	mapper *protocolMapper
	// The open connections
	connections map[uuid.UUID]network.SecureConn
	// chan of received messages - testmode
	networkChan chan network.NetworkMessage
	// The database of entities this host knows
	entities map[uuid.UUID]*network.Entity
	// The entityLists used for building the trees
	entityLists map[uuid.UUID]*EntityList
	// all trees known to this Host
	trees map[uuid.UUID]*Tree
	// TreeNode that this host represents mapped by their respective TreeID
	treeNodes map[uuid.UUID]*TreeNode
	// treeMarshal that needs to be converted to Tree but host does not have the
	// entityList associated yet.
	// map from EntityList.ID => trees that use this entity list
	pendingTreeMarshal map[uuid.UUID][]*TreeMarshal
	// pendingSDAData are a list of message we received that does not correspond
	// to any local tree or/and entitylist. We first request theses so we can
	// instantiate properly protocolinstance that will use these SDAData msg.
	pendingSDAs []*SDAData
	// The suite used for this Host
	suite abstract.Suite
	// closed channel to notifiy the connections that we close
	closed chan bool
	// lock associated to access network connections
	// and to access entities also.
	networkLock *sync.Mutex
	// lock associated to access entityLists
	entityListsLock *sync.Mutex
	// lock associated to access trees
	treesLock *sync.Mutex
	// lock associated with pending TreeMarshal
	pendingTreeLock *sync.Mutex
	// lock associated with pending SDAdata
	pendingSDAsLock *sync.Mutex
	// working address is mostly for debugging purposes so we know what address
	// is known as right now
	workingAddress string
}

// NewHost starts a new Host that will listen on the network for incoming
// messages. It will store the private-key.
func NewHost(e *network.Entity, pkey abstract.Secret) *Host {
	n := &Host{
		Entity:             e,
		workingAddress:     e.First(),
		connections:        make(map[uuid.UUID]network.SecureConn),
		entities:           make(map[uuid.UUID]*network.Entity),
		trees:              make(map[uuid.UUID]*Tree),
		treeNodes:          make(map[uuid.UUID]*TreeNode),
		entityLists:        make(map[uuid.UUID]*EntityList),
		pendingTreeMarshal: make(map[uuid.UUID][]*TreeMarshal),
		pendingSDAs:        make([]*SDAData, 0),
		host:               network.NewSecureTcpHost(pkey, e),
		private:            pkey,
		suite:              network.Suite,
		networkChan:        make(chan network.NetworkMessage, 1),
		closed:             make(chan bool),
		networkLock:        &sync.Mutex{},
		entityListsLock:    &sync.Mutex{},
		treesLock:          &sync.Mutex{},
		pendingTreeLock:    &sync.Mutex{},
		pendingSDAsLock:    &sync.Mutex{},
	}

	n.mapper = newProtocolMapper(n)
	return n
}

// NewHostKey creates a new host only from the ip-address and port-number. This
// is useful in testing and deployment for measurements
func NewHostKey(address string) (*Host, abstract.Secret) {
	keypair := config.NewKeyPair(network.Suite)
	entity := network.NewEntity(keypair.Public, address)
	return NewHost(entity, keypair.Secret), keypair.Secret
}

// Listen starts listening for messages coming from any host that tries to
// contact this entity / host
func (h *Host) Listen() {
	fn := func(c network.SecureConn) {
		dbg.Lvl3(h.workingAddress, "Accepted Connection from", c.Remote())
		// register the connection once we know it's ok
		h.registerConnection(c)
		h.handleConn(c)
	}
	go func() {
		dbg.Lvl3("Listening in", h.workingAddress)
		err := h.host.Listen(fn)
		if err != nil {
			dbg.Fatal("Couldn't listen in", h.workingAddress, ":", err)
		}
	}()
}

// Connect takes an entity where to connect to
func (h *Host) Connect(id *network.Entity) (network.SecureConn, error) {
	var err error
	var c network.SecureConn
	// try to open connection
	c, err = h.host.Open(id)
	if err != nil {
		return nil, err
	}
	h.registerConnection(c)
	dbg.Lvl2("Host", h.workingAddress, "connected to", c.Remote())
	go h.handleConn(c)
	return c, nil
}

// Close shuts down the listener
func (h *Host) Close() error {
	h.networkLock.Lock()
	for _, c := range h.connections {
		dbg.Lvl3("Closing connection", c)
		c.Close()
	}
	err := h.host.Close()
	h.connections = make(map[uuid.UUID]network.SecureConn)
	close(h.closed)
	h.networkLock.Unlock()
	return err
}

// SendRaw sends to an Entity without wrapping the msg into a SDAMessage
func (h *Host) SendRaw(e *network.Entity, msg network.ProtocolMessage) error {
	if msg == nil {
		return errors.New("Can't send nil-packet")
	}
	if _, ok := h.entities[e.Id]; !ok {
		// Connect to that entity
		_, err := h.Connect(e)
		if err != nil {
			return err
		}
	}
	var c network.SecureConn
	var ok bool
	if c, ok = h.connections[e.Id]; !ok {
		return errors.New("Got no connection tied to this Entity")
	}
	dbg.Lvl4("Sending to", e)
	c.Send(context.TODO(), msg)
	return nil
}

// SendSDA is the main function protocol instance must use in order to send a
// message across the network. A PI must first give its assigned Token, then
// the Entity where it want to send the message then the msg. The message will
// be transformed into a SDAData message automatically.
func (h *Host) SendSDA(from, to *Token, msg network.ProtocolMessage) error {
	tn, err := h.TreeNodeFromToken(to)
	if err != nil {
		return err
	}
	return h.SendSDAToTreeNode(from, tn, msg)
}

// SendSDAToTreeNode sends a message to a treeNode
func (h *Host) SendSDAToTreeNode(from *Token, to *TreeNode, msg network.ProtocolMessage) error {
	if h.mapper.Instance(from) == nil {
		return errors.New("No protocol instance registered with this token.")
	}
	if from == nil {
		return errors.New("From-token is nil")
	}
	if to == nil {
		return errors.New("To-token is nil")
	}
	sda := &SDAData{
		Msg:  msg,
		From: from,
		To:   from.OtherToken(to),
	}
	return h.sendSDAData(to.Entity, sda)
}

// StartNewProtocol starts a new protocol by instantiating a instance of that
// protocol and then call Start on it.
func (h *Host) StartNewProtocol(protocolID uuid.UUID, treeID uuid.UUID) (ProtocolInstance, error) {
	// check everything exists
	if !ProtocolExists(protocolID) {
		return nil, errors.New("Protocol does not exists")
	}
	var tree *Tree
	var ok bool
	h.treesLock.Lock()
	if tree, ok = h.trees[treeID]; !ok {
		return nil, errors.New("TreeId does not exists")
	}
	h.treesLock.Unlock()

	// instantiate
	token := &Token{
		ProtocolID:   protocolID,
		EntityListID: tree.EntityList.Id,
		TreeID:       treeID,
		// Host is handling the generation of protocolInstanceID
		RoundID: cliutils.NewRandomUUID(),
	}
	// instantiate protocol instance
	pi, err := h.protocolInstantiate(token, tree.Root)
	if err != nil {
		return nil, err
	}

	// start it
	dbg.Lvl3("Starting new protocolinstance at", h.Entity.Addresses)
	err = pi.Start()
	if err != nil {
		return nil, err
	}
	return pi, nil
}

func (h *Host) StartNewProtocolName(name string, treeID uuid.UUID) (ProtocolInstance, error) {
	return h.StartNewProtocol(ProtocolNameToUuid(name), treeID)
}

// ProcessMessages checks if it is one of the messages for us or dispatch it
// to the corresponding instance.
// Our messages are:
// * SDAMessage - used to communicate between the Hosts
// * RequestTreeID - ask the parent for a given tree
// * SendTree - send the tree to the child
// * RequestPeerListID - ask the parent for a given peerList
// * SendPeerListID - send the tree to the child
func (h *Host) ProcessMessages() {
	for {
		var err error
		data := h.receive()
		dbg.Lvl3("Message Received from", data.From)
		switch data.MsgType {
		case SDADataMessage:
			err := h.processSDAMessage(&data)
			if err != nil {
				dbg.Error("ProcessSDAMessage returned:", err)
			}
		// A host has sent us a request to get a tree definition
		case RequestTreeMessage:
			tid := data.Msg.(RequestTree).TreeID
			tree, ok := h.trees[tid]
			if ok {
				err = h.SendRaw(data.Entity, tree.MakeTreeMarshal())
			} else {
				// XXX Take care here for we must verify at the other side that
				// the tree is Nil. Should we think of a way of sending back an
				// "error" ?
				err = h.SendRaw(data.Entity, (&Tree{}).MakeTreeMarshal())
			}
		// A Host has replied to our request of a tree
		case SendTreeMessage:
			tm := data.Msg.(TreeMarshal)
			if tm.NodeId == uuid.Nil {
				dbg.Error("Received an empty Tree")
				continue
			}
			il, ok := h.GetEntityList(tm.EntityId)
			// The entity list does not exists, we should request for that too
			if !ok {
				msg := &RequestEntityList{tm.EntityId}
				if err := h.SendRaw(data.Entity, msg); err != nil {
					dbg.Error("Requesting EntityList in SendTree failed", err)
				}

				// put the tree marshal into pending queue so when we receive the
				// entitylist we can create the real Tree.
				h.addPendingTreeMarshal(&tm)
				continue
			}

			tree, err := tm.MakeTree(il)
			if err != nil {
				dbg.Error("Couldn't create tree:", err)
				continue
			}
			h.AddTree(tree)
			h.checkPendingSDA(tree)
		// Some host requested an EntityList
		case RequestEntityListMessage:
			id := data.Msg.(RequestEntityList).EntityListID
			il, ok := h.entityLists[id]
			if ok {
				err = h.SendRaw(data.Entity, il)
			} else {
				dbg.Lvl2("Requested entityList that we don't have")
				h.SendRaw(data.Entity, &EntityList{})
			}
		// Host replied to our request of entitylist
		case SendEntityListMessage:
			il := data.Msg.(EntityList)
			if il.Id == uuid.Nil {
				dbg.Lvl2("Received an empty EntityList")
			} else {
				h.AddEntityList(&il)
				// Check if some trees can be constructed from this entitylist
				h.checkPendingTreeMarshal(&il)
			}
		default:
			dbg.Error("Didn't recognize message", data.MsgType)
		}
		if err != nil {
			dbg.Error("Sending error:", err)
		}
	}
}

// AddEntityList stores the peer-list for further usage
func (h *Host) AddEntityList(il *EntityList) {
	h.entityListsLock.Lock()
	if _, ok := h.entityLists[il.Id]; ok {
		dbg.Lvl2("Added EntityList with same ID")
	}
	h.entityLists[il.Id] = il
	h.entityListsLock.Unlock()
}

// AddTree stores the tree for further usage
// IT also calls checkPendingSDA so we can now instantiate protocol instance
// using this tree
func (h *Host) AddTree(t *Tree) {
	h.treesLock.Lock()
	if _, ok := h.trees[t.Id]; ok {
		dbg.Lvl2("Added Tree with same ID")
	}
	h.trees[t.Id] = t
	h.treesLock.Unlock()
	h.checkPendingSDA(t)
}

// GetEntityList returns the EntityList
func (h *Host) GetEntityList(id uuid.UUID) (*EntityList, bool) {
	h.entityListsLock.Lock()
	il, ok := h.entityLists[id]
	h.entityListsLock.Unlock()
	return il, ok
}

// GetTree returns the TreeList
func (h *Host) GetTree(id uuid.UUID) (*Tree, bool) {
	h.treesLock.Lock()
	t, ok := h.trees[id]
	h.treesLock.Unlock()
	return t, ok
}

// HaveTree returns true if the protocolIDm the ENtityListID and the treeID is
// right or no. If we don't have either the tree or the entitylist, we then
// request them first amd put the message as pending message.
func (h *Host) HaveTree(sda *SDAData) bool {

	return true
}

func (h *Host) TreeNodeFromToken(t *Token) (*TreeNode, error) {
	tree, ok := h.trees[t.TreeID]
	if !ok {
		return nil, errors.New("Didn't find tree")
	}
	tn := tree.GetNode(t.TreeNodeID)
	if tn == nil {
		return nil, errors.New("Didn't find treenode")
	}
	return tn, nil
}

// Suite returns the suite used by the host
// NOTE for the moment the suite is fixed for the host and any protocols
// instance.
func (h *Host) Suite() abstract.Suite {
	return h.suite
}

func (h *Host) Private() abstract.Secret {
	return h.private
}

// ProtocolInstantiate creates a new instance of a protocol given by it's name
func (h *Host) protocolInstantiate(tok *Token, tn *TreeNode) (ProtocolInstance, error) {
	p, ok := protocols[tok.ProtocolID]
	if !ok {
		return nil, errors.New("Protocol doesn't exist")
	}
	tree, ok := h.GetTree(tok.TreeID)
	if !ok {
		return nil, errors.New("Tree does not exists")
	}
	if _, ok := h.GetEntityList(tok.EntityListID); !ok {
		return nil, errors.New("EntityList does not exists")
	}
	if !tn.IsInTree(tree) {
		return nil, errors.New("We are not represented in the tree")
	}
	pi := p(h, tn, tok)
	h.mapper.RegisterProtocolInstance(pi, tok)
	return pi, nil
}

// sendSDAData do its marshalling of the inner msg and then sends a SDAData msg
// to the  appropriate entity
func (h *Host) sendSDAData(e *network.Entity, sdaMsg *SDAData) error {
	b, err := network.MarshalRegisteredType(sdaMsg.Msg)
	if err != nil {
		return fmt.Errorf("Error marshaling  message: %s", err.Error())
	}
	sdaMsg.MsgSlice = b
	sdaMsg.MsgType = network.TypeFromData(sdaMsg.Msg)
	// put to nil so protobuf won't encode it and there won't be any error on the
	// other side (because it doesn't know how to encode it)
	sdaMsg.Msg = nil
	return h.SendRaw(e, sdaMsg)
}

// Receive will return the value of the communication-channel, unmarshalling
// the SDAMessage. Receive is called in ProcessMessages as it takes directly
// the message from the networkChan, and pre-process the SDAMessage
func (h *Host) receive() network.NetworkMessage {
	data := <-h.networkChan
	if data.MsgType == SDADataMessage {
		sda := data.Msg.(SDAData)
		t, msg, err := network.UnmarshalRegisteredType(sda.MsgSlice, data.Constructors)
		if err != nil {
			dbg.Error("Error while marshalling inner message of SDAData:", err)
		}
		// Put the msg into SDAData
		sda.MsgType = t
		sda.Msg = msg
		// Write back the Msg in appplicationMessage
		data.Msg = sda
		dbg.Lvlf3("SDA-Message is: %+v", sda.Msg)
	}
	return data
}

// Handle a connection => giving messages to the MsgChans
func (h *Host) handleConn(c network.SecureConn) {
	address := c.Remote()
	msgChan := make(chan network.NetworkMessage)
	errorChan := make(chan error)
	doneChan := make(chan bool)
	go func() {
		for {
			select {
			case <-doneChan:
				dbg.Lvl3("Closing", c)
				return
			default:
				ctx := context.TODO()
				am, err := c.Receive(ctx)
				// So the receiver can know about the error
				am.SetError(err)
				am.From = address
				if err != nil {
					errorChan <- err
				} else {
					msgChan <- am
				}
			}
		}
	}()
	for {
		select {
		case <-h.closed:
			doneChan <- true
		case am := <-msgChan:
			dbg.Lvl3("Putting message into networkChan:", am.From)
			h.networkChan <- am
		case e := <-errorChan:
			if e == network.ErrClosed || e == network.ErrEOF {
				return
			}
			dbg.Error("Error with connection", address, "=> error", e)
		case <-time.After(timeOut):
			dbg.Error("Timeout with connection", address)
		}
	}
}

// Dispatch SDA message looks if we have all the info to rightly dispatch the
// packet such as the protocol id and the topology id and the protocol instance
// id
func (h *Host) processSDAMessage(am *network.NetworkMessage) error {
	sdaMsg := am.Msg.(SDAData)
	t, msg, err := network.UnmarshalRegisteredType(sdaMsg.MsgSlice, network.DefaultConstructors(h.Suite()))
	if err != nil {
		dbg.Error("Error unmarshaling embedded msg in SDAMessage", err)
	}
	// Set the right type and msg
	sdaMsg.MsgType = t
	sdaMsg.Msg = msg
	sdaMsg.Entity = am.Entity
	if !ProtocolExists(sdaMsg.To.ProtocolID) {
		return errors.New("Protocol does not exists from token")
	}
	// do we have the entitylist ? if not, ask for it.
	if _, ok := h.GetEntityList(sdaMsg.To.EntityListID); !ok {
		dbg.Lvl2("Will ask for entityList + tree from token")
		return h.requestTree(am.Entity, &sdaMsg)
	}
	tree, ok := h.GetTree(sdaMsg.To.TreeID)
	if !ok {
		dbg.Lvl2("Will ask for tree from token")
		return h.requestTree(am.Entity, &sdaMsg)
	}
	// If pi does not exists, then instantiate it !
	if !h.mapper.Exists(sdaMsg.To.Id()) {
		_, err := h.protocolInstantiate(sdaMsg.To, tree.GetNode(sdaMsg.To.TreeNodeID))
		if err != nil {
			return err
		}
	}

	ok, err = h.mapper.DispatchToInstance(&sdaMsg)
	if err != nil {
		return err
	}
	return nil
}

// requestTree will ask for the tree the sdadata is related to.
// it will put the message inside the pending list of sda message waiting to
// have their trees.
func (h *Host) requestTree(e *network.Entity, sdaMsg *SDAData) error {
	h.addPendingSda(sdaMsg)
	treeRequest := &RequestTree{sdaMsg.To.TreeID}
	return h.SendRaw(e, treeRequest)
}

// addPendingSda simply append a sda message to a queue. This queue willbe
// checked each time we receive a new tree / entityList
func (h *Host) addPendingSda(sda *SDAData) {
	h.pendingSDAsLock.Lock()
	h.pendingSDAs = append(h.pendingSDAs, sda)
	h.pendingSDAsLock.Unlock()
}

// checkPendingSda is called each time we receive a new tree if there are some SDA
// messages using this tree. If there are, we can make an instance of a protocolinstance
// and give it the message!.
// NOTE: put that as a go routine so the rest of the processing messages are not
// slowed down, if there are many pending sda message at once (i.e. start many new
// protocols at same time)
func (h *Host) checkPendingSDA(t *Tree) {
	go func() {
		h.pendingSDAsLock.Lock()
		for i := range h.pendingSDAs {
			// if this message referes to this tree
			if uuid.Equal(t.Id, h.pendingSDAs[i].To.TreeID) {
				// instantiate it and go !
				sdaMsg := h.pendingSDAs[i]
				node := t.GetNode(sdaMsg.To.TreeNodeID)
				if node == nil {
					dbg.Error("Didn't find our node in the tree")
					continue
				}
				_, err := h.protocolInstantiate(sdaMsg.To, node)
				if err != nil {
					dbg.Error("Instantiation of the protocol failed (should not happen)", err)
					continue
				}
				ok, err := h.mapper.DispatchToInstance(h.pendingSDAs[i])
				if !ok {
					dbg.Lvl2("dispatching did not work")
				} else if err != nil {
					dbg.Error(err)
				}
			}
		}
		h.pendingSDAsLock.Unlock()
	}()
}

// registerConnection registers a Entity for a new connection, mapped with the
// real physical address of the connection and the connection itself
func (h *Host) registerConnection(c network.SecureConn) {
	h.networkLock.Lock()
	id := c.Entity()
	h.entities[c.Entity().Id] = id
	h.connections[c.Entity().Id] = c
	h.networkLock.Unlock()
}

// addPendingTreeMarshal adds a treeMarshal to the list.
// This list is checked each time we receive a new EntityList
// so trees using this EntityList can be constructed.
func (h *Host) addPendingTreeMarshal(tm *TreeMarshal) {
	h.pendingTreeLock.Lock()
	var sl []*TreeMarshal
	var ok bool
	// initiate the slice before adding
	if sl, ok = h.pendingTreeMarshal[tm.EntityId]; !ok {
		sl = make([]*TreeMarshal, 0)
	}
	sl = append(sl, tm)
	h.pendingTreeMarshal[tm.EntityId] = sl
	h.pendingTreeLock.Unlock()
}

// checkPendingTreeMarshal is called each time we add a new EntityList to the
// system. It checks if some treeMarshal use this entityList so they can be
// converted to Tree.
func (h *Host) checkPendingTreeMarshal(el *EntityList) {
	h.pendingTreeLock.Lock()
	var sl []*TreeMarshal
	var ok bool
	if sl, ok = h.pendingTreeMarshal[el.Id]; !ok {
		// no tree for this entitty list
		return
	}
	for _, tm := range sl {
		tree, err := tm.MakeTree(el)
		if err != nil {
			dbg.Error("Tree from EntityList failed")
			continue
		}
		// add the tree into our "database"
		h.AddTree(tree)
	}
	h.pendingTreeLock.Unlock()
}
