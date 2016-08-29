package sda

import (
	"errors"
	"reflect"

	"strings"

	"github.com/dedis/cothority/log"
	"github.com/dedis/cothority/network"
)

// ServiceProcessor allows for an easy integration of external messages
// into the Services. You have to embed it into your Service-structer,
// then it will offer an 'RegisterMessage'-method that takes a message of type
// 	func ReceiveMsg(e *network.ServerIdentity, msg *anyMessageType)(error, *replyMsg)
// where 'ReceiveMsg' is any name and 'anyMessageType' will be registered
// with the network. Once 'anyMessageType' is received by the service,
// the function 'ReceiveMsg' should return an error and any 'replyMsg' it
// wants to send.
type ServiceProcessor struct {
	functions map[network.MessageTypeID]interface{}
	*Context
}

// NewServiceProcessor initializes your ServiceProcessor.
func NewServiceProcessor(c *Context) *ServiceProcessor {
	return &ServiceProcessor{
		functions: make(map[network.MessageTypeID]interface{}),
		Context:   c,
	}
}

// RegisterMessage puts a new message in the message-handler
// XXX More comments are needed as it's not clear whether RegisterMessage waits
// for message for/from Clients or for/from Services.
func (p *ServiceProcessor) RegisterMessage(f interface{}) error {
	ft := reflect.TypeOf(f)
	// Check we have the correct channel-type
	if ft.Kind() != reflect.Func {
		return errors.New("Input is not function")
	}
	if ft.NumIn() != 2 {
		return errors.New("Need two arguments: *network.ServerIdentity and *struct")
	}
	if ft.In(0) != reflect.TypeOf(&network.ServerIdentity{}) {
		return errors.New("First argument must be *network.ServerIdentity")
	}
	cr1 := ft.In(1)
	if cr1.Kind() != reflect.Ptr {
		return errors.New("Second argument must be a *pointer* to struct")
	}
	if cr1.Elem().Kind() != reflect.Struct {
		return errors.New("Second argument must be a pointer to *struct*")
	}
	if ft.NumOut() != 2 {
		return errors.New("Need 2 return values: network.ProtocolMessage and error")
	}
	if ft.Out(0) != reflect.TypeOf((*network.Body)(nil)).Elem() {
		return errors.New("Need 2 return values: *network.ProtocolMessage* and error")
	}
	if ft.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		return errors.New("Need 2 return values: network.ProtocolMessage and *error*")
	}
	// Automatic registration of the message to the network library.
	log.Lvl4("Registering handler", cr1.String())
	typ := network.RegisterMessageUUID(network.RTypeToMessageTypeID(
		cr1.Elem()),
		cr1.Elem())
	p.functions[typ] = f
	return nil
}

// Process implements the Processor interface and dispatches ClientRequest message
// and InterServiceMessage
func (p *ServiceProcessor) Process(packet *network.Packet) {
	p.GetReply(packet.ServerIdentity, packet.MsgType, packet.Msg)
}

// ProcessClientRequest takes a request from a client, calculates the reply
// and sends it back.
func (p *ServiceProcessor) ProcessClientRequest(e *network.ServerIdentity,
	cr *ClientRequest) {
	// unmarshal the inner message
	mt, m, err := network.UnmarshalRegisteredType(cr.Data,
		network.DefaultConstructors(network.Suite))
	if err != nil {
		log.Error("Err unmarshal client request:" + err.Error())
		return
	}
	reply := p.GetReply(e, mt, m)
	if err := p.SendRaw(e, reply); err != nil {
		log.Error(err)
	}
}

// SendISM takes the message and sends it to the corresponding service
func (p *ServiceProcessor) SendISM(e *network.ServerIdentity, msg network.Body) error {
	sName := ServiceFactory.Name(p.Context.ServiceID())
	sm, err := CreateServiceMessage(sName, msg)
	if err != nil {
		return err
	}
	log.Lvl4("Raw-sending to", e)
	return p.SendRaw(e, sm)
}

// SendISMOthers sends an InterServiceMessage to all other services
func (p *ServiceProcessor) SendISMOthers(el *Roster, msg network.Body) error {
	var errStrs []string
	for _, e := range el.List {
		if !e.ID.Equal(p.Context.ServerIdentity().ID) {
			log.Lvl3("Sending to", e)
			err := p.SendISM(e, msg)
			if err != nil {
				errStrs = append(errStrs, err.Error())
			}
		}
	}
	var err error
	if len(errStrs) > 0 {
		err = errors.New(strings.Join(errStrs, "\n"))
	}
	return err
}

// GetReply takes msgType and a message. It dispatches the msg to the right
// function registered, then sends the responses to the sender.
func (p *ServiceProcessor) GetReply(e *network.ServerIdentity, mt network.MessageTypeID, m network.Body) network.Body {
	fu, ok := p.functions[mt]
	if !ok {
		return &network.StatusRet{"Didn't register message-handler: " + mt.String()}
	}

	//to0 := reflect.TypeOf(fu).In(0)
	to1 := reflect.TypeOf(fu).In(1)
	f := reflect.ValueOf(fu)

	log.Lvl4("Dispatching to", e.Address)
	arg0 := reflect.New(reflect.TypeOf(network.ServerIdentity{}))
	arg0.Elem().Set(reflect.ValueOf(e).Elem())
	arg1 := reflect.New(to1.Elem())
	arg1.Elem().Set(reflect.ValueOf(m))

	ret := f.Call([]reflect.Value{arg0, arg1})

	errI := ret[1].Interface()

	if errI != nil {
		return &network.StatusRet{errI.(error).Error()}
	}

	reply := ret[0].Interface()
	if reply == nil {
		reply = network.StatusOK
	}
	return reply
}
