/*
Identity is a service that allows storing of key/value pairs that belong to
a given identity that is shared between multiple devices. In order to
add/remove devices or add/remove key/value-pairs, a 'threshold' of devices
need to vote on those changes.

The key/value-pairs are stored in a personal blockchain and signed by the
cothority using forward-links, so that an external observer can check the
collective signatures and be assured that the blockchain is valid.
*/

package sidentity

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"sync"
	"time"

	"github.com/dedis/cothority/crypto"
	"github.com/dedis/cothority/log"
	"github.com/dedis/cothority/network"
	"github.com/dedis/cothority/protocols/manage"
	"github.com/dedis/cothority/protocols/swupdate"
	"github.com/dedis/cothority/sda"
	"github.com/dedis/cothority/services/ca"
	"github.com/dedis/cothority/services/common_structs"
	"github.com/dedis/cothority/services/skipchain"
	//"github.com/dedis/cothority/services/timestamp"
	"github.com/dedis/crypto/abstract"
)

// ServiceName can be used to refer to the name of this service
const ServiceName = "SIdentity"

var IdentityService sda.ServiceID

var dummyVerfier = func(rootAndTimestamp []byte) bool {
	l := len(rootAndTimestamp)
	_, err := bytesToTimestamp(rootAndTimestamp[l-10 : l])
	if err != nil {
		log.Error("Got some invalid timestamp.")
	}
	return true
}

func init() {
	sda.RegisterNewService(ServiceName, newIdentityService)
	IdentityService = sda.ServiceFactory.ServiceID(ServiceName)
	network.RegisterPacketType(&StorageMap{})
	network.RegisterPacketType(&Storage{})
}

// Service handles identities
type Service struct {
	*sda.ServiceProcessor
	skipchain *skipchain.Client
	ca        *ca.CSRDispatcher
	//stamper   *timestamp.Client
	*StorageMap
	identitiesMutex sync.Mutex
	path            string
	// 'Publics' holds the map between the ServerIdentity of each web server and its public key (to be
	// used by the devices for encryption of the web server's private tls key)
	Publics       map[string]abstract.Point
	EpochDuration time.Duration
	TheRoster     *sda.Roster
	//signMsg       func(roster *sda.Roster, m []byte) []byte
	signMsg func(m []byte) []byte
}

// StorageMap holds the map to the storages so it can be marshaled.
type StorageMap struct {
	Identities map[string]*Storage
}

// Storage stores one identity together with the skipblocks.
type Storage struct {
	sync.Mutex
	ID         skipchain.SkipBlockID
	Latest     *common_structs.Config
	Proposed   *common_structs.Config
	Votes      map[string]*crypto.SchnorrSig
	Root       *skipchain.SkipBlock
	Data       *skipchain.SkipBlock
	SkipBlocks map[string]*skipchain.SkipBlock
	//Certs      []*ca.Cert
	// Certs keeps the mapping between the config (the hash of the skipblock that contains it) and the cert(s)
	// that was(were) issued for that particular config
	//Certs map[string][]*ca.Cert
	CertInfo *common_structs.CertInfo

	// Latest PoF (on the Latest config)
	PoF *common_structs.SignatureResponse
}

// NewProtocol is called by the Overlay when a new protocol request comes in.
func (s *Service) NewProtocol(tn *sda.TreeNodeInstance, conf *sda.GenericConfig) (sda.ProtocolInstance, error) {
	log.Lvl3(s.ServerIdentity(), "Identity received New Protocol event", conf)
	switch tn.ProtocolName() {
	case "Propagate":
		pi, err := manage.NewPropagateProtocol(tn)
		if err != nil {
			return nil, err
		}
		pi.(*manage.Propagate).RegisterOnData(s.Propagate)
		return pi, err
	}
	log.LLvlf2("%v: Timestamp Service received New Protocol event", s.String())
	pi, err := swupdate.NewCoSiUpdate(tn, dummyVerfier)
	if err != nil {
		log.LLvlf2("%v", err)
	}
	return pi, err
}

/*
 * API messages
 */

// CreateIdentity will register a new SkipChain and add it to our list of
// managed identities.
func (s *Service) CreateIdentity(si *network.ServerIdentity, ai *CreateIdentity) (network.Body, error) {
	//log.LLvlf2("haaa %v", len(s.Identities))
	//log.LLvlf2("%s Creating new site identity with config %+v", s, ai.Config)
	ids := &Storage{
		Latest: ai.Config,
	}
	log.Lvl3("Creating Root-skipchain")
	var err error
	ids.Root, err = s.skipchain.CreateRoster(ai.Roster, 2, 10,
		skipchain.VerifyNone, nil)
	if err != nil {
		return nil, err
	}
	log.Lvl3("Creating Data-skipchain")
	ids.Root, ids.Data, err = s.skipchain.CreateData(ids.Root, 2, 10,
		skipchain.VerifyNone, ai.Config)
	if err != nil {
		return nil, err
	}

	ids.SkipBlocks = make(map[string]*skipchain.SkipBlock)
	ids.setSkipBlockByID(ids.Data)

	roster := ids.Root.Roster
	cert, _ := s.ca.SignCert(ai.Config, nil, ids.Data.Hash)
	certinfo := &common_structs.CertInfo{
		Cert:   cert[0],
		SbHash: ids.Data.Hash,
	}
	ids.CertInfo = certinfo
	ids.ID = ids.Data.Hash

	/*
		certs, _ := s.ca.SignCert(ai.Config, ids.Data.Hash)
		if certs == nil {
			log.LLvlf2("No certs returned")
		}

		ids.Certs = make(map[string][]*ca.Cert)
		hash := ids.Data.Hash
		for _, cert := range certs {
			slice := ids.Certs[string(hash)]
			slice = append(slice, cert)
			ids.Certs[string(hash)] = slice

			//ids.Certs = append(ids.Certs, cert)
			//log.LLvlf2("---------NEW CERT!--------")
			//log.LLvlf2("siteID: %v, hash: %v, sig: %v, public: %v", cert.ID, cert.Hash, cert.Signature, cert.Public)
		}
	*/
	replies, err := manage.PropagateStartAndWait(s.Context, roster,
		&PropagateIdentity{ids}, propagateTimeout, s.Propagate)
	if err != nil {
		return nil, err
	}

	if replies != len(roster.List) {
		log.Warn("Did only get", replies, "out of", len(roster.List))
	}
	//log.LLvlf2("New chain is\n%x", []byte(ids.Data.Hash))
	/*
		// Init stamper and start it
		_, err = s.stamper.SetupStamper(ai.Roster, time.Millisecond*200000, 1)

		go s.SendConfigsToStamper()
	*/

	s.save()
	//log.Lvlf2("CreateIdentity(): End having %v certs", len(ids.Certs))
	/*
		cnt := 0
		for _, certarray := range ids.Certs {
			for _, _ = range certarray {
				cnt++
			}
		}
		log.LLvlf2("CreateIdentity(): End having %v certs", cnt)
	*/
	log.LLvlf2("------CreateIdentity(): Successfully created a new identity-------")
	return &CreateIdentityReply{
		Root: ids.Root,
		Data: ids.Data,
	}, nil
}

/*
func (s *Service) SendConfigsToStamper() {
	//c := time.Tick(time.Millisecond * 250000)
	c := time.Tick(time.Millisecond * 1000)
	for _ = range c {
		var res []chan *timestamp.SignatureResponse
		for index := range res {
			res[index] = make(chan *timestamp.SignatureResponse)
		}
		index := 0
		for _, sid := range s.Identities {
			latestconf := sid.Latest
			for _, server := range latestconf.ProxyRoster.List {
				log.LLvlf2("%v %v", index, server)
			}
			rootIdentity := latestconf.ProxyRoster.Get(0)
			hash, _ := latestconf.Hash()
			//go func() {
			log.LLvlf2("%v %v %v", index, rootIdentity, hash)
			result, err := s.stamper.SignMsg(rootIdentity, hash)
			log.ErrFatal(err, "Couldn't send")
			res[index] <- result
			//}()
			index++
		}

		log.Lvl1("Waiting on responses ...")
		index = 0
		for _, sid := range s.Identities {
			sid.PoF = <-res[index]
			log.LLvlf2("1")
			roster := sid.Latest.ProxyRoster
			replies, err := manage.PropagateStartAndWait(s.Context, roster,
				&PropagatePoF{sid}, propagateTimeout, s.Propagate)
			if err != nil {
				log.ErrFatal(err, "Couldn't send")
			}

			if replies != len(roster.List) {
				log.Warn("Did only get", replies, "out of", len(roster.List))
			}
			index++
		}
	}
}
*/
// ConfigUpdate returns a new configuration update
func (s *Service) ConfigUpdate(si *network.ServerIdentity, cu *ConfigUpdate) (network.Body, error) {
	sid := s.getIdentityStorage(cu.ID)
	if sid == nil {
		return nil, fmt.Errorf("Didn't find Identity: %v", cu.ID)
	}
	sid.Lock()
	defer sid.Unlock()
	log.Lvl3(s, "Sending config-update")
	return &ConfigUpdateReply{
		Config: sid.Latest,
	}, nil
}

func (s *Storage) setSkipBlockByID(latest *skipchain.SkipBlock) bool {
	s.SkipBlocks[string(latest.Hash)] = latest
	return true
}

// getSkipBlockByID returns the skip-block or false if it doesn't exist
func (s *Storage) getSkipBlockByID(sbID skipchain.SkipBlockID) (*skipchain.SkipBlock, bool) {
	b, ok := s.SkipBlocks[string(sbID)]
	//b, ok := s.SkipBlocks["georgia"]
	return b, ok
}

/*
// forward traversal of the skipchain
func (s *Service) GetUpdateChain(si *network.ServerIdentity, latestKnown *GetUpdateChain) (network.Body, error) {
	sid := s.getIdentityStorage(latestKnown.ID)
	//log.Lvlf2("GetUpdateChain(): Start having %v certs", len(sid.Certs))

	//	cnt := 0
	//	certs := make([]*ca.Cert, 0)
	//	for _, certarray := range sid.Certs {
	//		for _, cert := range certarray {
	//			cnt++
	//			certs = append(certs, cert)
	//		}
	//	}
	//	log.LLvlf2("GetUpdateChain(): Start having %v certs", cnt)

	log.LLvlf2("GetUpdateChain(): Latest known block has hash: %v", latestKnown.LatestID)
	block, ok := sid.getSkipBlockByID(latestKnown.LatestID)
	if !ok {
		return nil, errors.New("Couldn't find latest skipblock!!")
	}

	blocks := []*skipchain.SkipBlock{block}
	log.Lvl3("Starting to search chain")
	for len(block.ForwardLink) > 0 {
		//link := block.ForwardLink[len(block.ForwardLink)-1]
		// for linear forward traversal of the skipchain:
		link := block.ForwardLink[0]
		hash := link.Hash
		block, ok = sid.getSkipBlockByID(hash)
		if !ok {
			return nil, errors.New("Missing block in forward-chain")
		}
		blocks = append(blocks, block)
		//fmt.Println("another block found with hash: ", skipchain.SkipBlockID(hash))
	}
	log.LLvlf2("Found %v blocks", len(blocks))
	for index, block := range blocks {
		log.LLvlf2("block: %v with hash: %v", index, block.Hash)
	}

	//	cnt = 0
	//	certs = make([]*ca.Cert, 0)
	//	for _, certarray := range sid.Certs {
	//		for _, cert := range certarray {
	//			cnt++
	//			certs = append(certs, cert)
	//		}
	//	}
	//	log.Lvlf2("GetUpdateChain(): End having %v certs", cnt)


	_, err := s.CheckRefreshCert(latestKnown.ID)
	if err != nil {
		return nil, err
	}

	reply := &GetUpdateChainReply{
		Update: blocks,
		Cert:   sid.CertInfo.Cert,
	}
	return reply, nil
}
*/
// ProposeSend only stores the proposed configuration internally. Signatures
// come later.
func (s *Service) ProposeSend(si *network.ServerIdentity, p *ProposeSend) (network.Body, error) {
	log.LLvlf2("Storing new proposal")
	sid := s.getIdentityStorage(p.ID)
	if sid == nil {
		log.Lvlf2("Didn't find Identity")
		return nil, errors.New("Didn't find Identity")
	}
	roster := sid.Root.Roster
	replies, err := manage.PropagateStartAndWait(s.Context, roster,
		p, propagateTimeout, s.Propagate)
	if err != nil {
		return nil, err
	}
	if replies != len(roster.List) {
		log.Warn("Did only get", replies, "out of", len(roster.List))
	}
	return nil, nil
}

// ProposeUpdate returns an eventual config-proposition
func (s *Service) ProposeUpdate(si *network.ServerIdentity, cnc *ProposeUpdate) (network.Body, error) {
	log.Lvl3(s, "Sending proposal-update to client")
	sid := s.getIdentityStorage(cnc.ID)
	if sid == nil {
		return nil, errors.New("Didn't find Identity")
	}
	sid.Lock()
	defer sid.Unlock()
	return &ProposeUpdateReply{
		Propose: sid.Proposed,
	}, nil
}

// ProposeVote takes int account a vote for the proposed config. It also verifies
// that the voter is in the latest config.
// An empty signature signifies that the vote has been rejected.
func (s *Service) ProposeVote(si *network.ServerIdentity, v *ProposeVote) (network.Body, error) {
	log.Lvl2(s, "Voting on proposal")
	// First verify if the signature is legitimate
	sid := s.getIdentityStorage(v.ID)
	if sid == nil {
		return nil, errors.New("Didn't find identity")
	}

	// Putting this in a function because of the lock which needs to be held
	// over all calls that might return an error.
	err := func() error {
		sid.Lock()
		defer sid.Unlock()
		log.Lvl3("Voting on", sid.Proposed.Device)
		owner, ok := sid.Latest.Device[v.Signer]
		if !ok {
			return errors.New("Didn't find signer")
		}
		if sid.Proposed == nil {
			return errors.New("No proposed block")
		}
		hash, err := sid.Proposed.Hash()
		if err != nil {
			return errors.New("Couldn't get hash")
		}
		if _, exists := sid.Votes[v.Signer]; exists {
			return errors.New("Already voted for that block")
		}

		// Check whether our clock is relatively close or not to the proposed timestamp
		err2 := sid.Proposed.CheckTimeDiff(maxdiff_sign)
		if err2 != nil {
			log.Lvlf2("Cothority %v", err2)
			return err2
		}

		log.Lvl3(v.Signer, "voted", v.Signature)
		if v.Signature != nil {
			err = crypto.VerifySchnorr(network.Suite, owner.Point, hash, *v.Signature)
			if err != nil {
				return errors.New("Wrong signature: " + err.Error())
			}
		}
		return nil
	}()
	if err != nil {
		return nil, err
	}

	// Propagate the vote
	_, err = manage.PropagateStartAndWait(s.Context, sid.Root.Roster, v, propagateTimeout, s.Propagate)
	if err != nil {
		return nil, err
	}
	if len(sid.Votes) >= sid.Latest.Threshold ||
		len(sid.Votes) == len(sid.Latest.Device) {
		// If we have enough signatures, make a new data-skipblock and
		// propagate it
		log.Lvl3("Having majority or all votes")

		// Making a new data-skipblock
		log.Lvl3("Sending data-block with", sid.Proposed.Device)
		reply, err := s.skipchain.ProposeData(sid.Root, sid.Data, sid.Proposed)
		if err != nil {
			return nil, err
		}
		_, msg, _ := network.UnmarshalRegistered(reply.Latest.Data)
		log.Lvl3("SB signed is", msg.(*common_structs.Config).Device)
		usb := &UpdateSkipBlock{
			ID:       v.ID,
			Latest:   reply.Latest,
			Previous: reply.Previous,
		}
		sid.setSkipBlockByID(usb.Latest)
		sid.setSkipBlockByID(usb.Previous)
		_, err = manage.PropagateStartAndWait(s.Context, sid.Root.Roster,
			usb, propagateTimeout, s.Propagate)
		if err != nil {
			return nil, err
		}
		s.save()
		//fmt.Println("latest block's hash: ", sid.Data.Hash, "number of flinks: ", len(sid.Data.ForwardLink))
		//fmt.Println(sid.Data.BackLinkIds[0])
		//block1, _ := sid.getSkipBlockByID(ID(sid.Data.BackLinkIds[0]))
		//fmt.Println(len(block1.ForwardLink))
		//fmt.Println("latest block's hash: ", usb.Latest.Hash)
		return &ProposeVoteReply{sid.Data}, nil
	}
	return nil, nil
}

/*
 * Internal messages
 */

// Propagate handles propagation of all data in the identity-service
func (s *Service) Propagate(msg network.Body) {
	log.Lvlf4("Got msg %+v %v", msg, reflect.TypeOf(msg).String())
	id := skipchain.SkipBlockID(nil)
	switch msg.(type) {
	case *PushPublicKey:
		p := msg.(*PushPublicKey)
		public := p.Public
		serverID := p.ServerID
		key := fmt.Sprintf("tls:%v", serverID)
		s.Publics[key] = public
		return
	case *ProposeSend:
		id = msg.(*ProposeSend).ID
	case *ProposeVote:
		id = msg.(*ProposeVote).ID
	case *UpdateSkipBlock:
		id = msg.(*UpdateSkipBlock).ID
	case *PropagateIdentity:
		pi := msg.(*PropagateIdentity)
		id = pi.Data.Hash
		if s.getIdentityStorage(id) != nil {
			log.Error("Couldn't store new identity")
			return
		}
		log.Lvl3("Storing identity in", s)
		s.setIdentityStorage(id, pi.Storage)

		sid := s.getIdentityStorage(id)
		sid.SkipBlocks = make(map[string]*skipchain.SkipBlock)
		sid.setSkipBlockByID(pi.Data)
		return
	case *PropagateCert:
		pc := msg.(*PropagateCert)
		cert := pc.CertInfo.Cert
		id = cert.ID
		s.setIdentityStorage(id, pc.Storage)
		log.LLvlf2("Fresh cert is now stored")
		return
	/*
		case *PropagatePoF:
			log.LLvlf2("Trying to store PoF at: %v", s.String())
			pof := msg.(*PropagatePoF)
			id = pof.CertInfo.Cert.ID
			s.setIdentityStorage(id, pof.Storage)
			log.LLvlf2("PoF is now stored at: %v", s.String())
			return
	*/
	case *PropagatePoF:
		log.LLvlf2("Trying to store PoFs at: %v", s.String())
		//s.identitiesMutex.Lock()
		sids := msg.(*PropagatePoF).Storages
		for _, sid := range sids {
			id = sid.ID
			s.Identities[string(id)] = sid
		}
		//s.identitiesMutex.Unlock()
		log.LLvlf2("PoFs are now stored at: %v", s.String())
		return
	}

	if id != nil {
		sid := s.getIdentityStorage(id)
		if sid == nil {
			log.Error("Didn't find entity in", s)
			return
		}
		sid.Lock()
		defer sid.Unlock()
		switch msg.(type) {
		case *ProposeSend:
			p := msg.(*ProposeSend)
			sid.Proposed = p.Config
			sid.Votes = make(map[string]*crypto.SchnorrSig)
		case *ProposeVote:
			v := msg.(*ProposeVote)
			if len(sid.Votes) == 0 {
				sid.Votes = make(map[string]*crypto.SchnorrSig)
			}
			sid.Votes[v.Signer] = v.Signature
			sid.Proposed.Device[v.Signer].Vote = v.Signature
		case *UpdateSkipBlock:
			skipblock_previous := msg.(*UpdateSkipBlock).Previous
			skipblock_latest := msg.(*UpdateSkipBlock).Latest
			_, msgLatest, err := network.UnmarshalRegistered(skipblock_latest.Data)
			if err != nil {
				log.Error(err)
				return
			}
			al, ok := msgLatest.(*common_structs.Config)
			if !ok {
				log.Error(err)
				return
			}
			sid.Data = skipblock_latest
			sid.Latest = al
			sid.Proposed = nil
			sid.Votes = make(map[string]*crypto.SchnorrSig)
			sid.setSkipBlockByID(skipblock_latest)
			sid.setSkipBlockByID(skipblock_previous)
		}
	}
}

/*
// backward traversal of the skipchain until finding a skipblock whose config has been certified
// (a cert has been issued for it)
func (s *Service) GetSkipblocks(si *network.ServerIdentity, req *GetSkipblocks) (network.Body, error) {
	log.LLvlf2("GetSkipblocks(): Start")
	id := req.ID
	latest := req.Latest
	sid := s.getIdentityStorage(id)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return nil, errors.New("Didn't find identity")
	}
	// Follow the backward links until finding the skipblock whose config was certified by a CA
	// All these skipblocks will be returned (from the oldest to the newest)
	sbs := make([]*skipchain.SkipBlock, 1)
	//sbs = append(sbs, latest)
	block := latest
	hash := block.Hash
	var ok bool
	_, exists := sid.Certs[string(hash)]
	for {
		block, ok = sid.getSkipBlockByID(hash)
		//log.LLvlf2("hash: %v", block.Hash)
		if !ok {
			log.LLvlf2("Skipblock with hash: %v not found", hash)
			return nil, fmt.Errorf("Skipblock with hash: %v not found", hash)
		}
		sbs = append(sbs, block)
		if exists {
			break
		}
		hash = block.BackLinkIds[0]
		_, exists = sid.Certs[string(hash)]
	}

	sbs_from_oldest := make([]*skipchain.SkipBlock, len(sbs))
	for index, block := range sbs {
		sbs_from_oldest[len(sbs)-1-index] = block
	}
	log.LLvlf2("GetSkipblocks(): End with %v blocks to return", len(sbs))
	log.LLvlf2("GetSkipblocks(): End with %v blocks to return", len(sbs_from_oldest))
	return &GetSkipblocksReply{Skipblocks: sbs_from_oldest}, nil
}
*/

// Forward traversal of the skipchain from the oldest block as the latter is
// specified by its hash in the request's 'Hash1' field (if Hash1==[]byte{0}, then start
// fetching from the skipblock for the config of which the latest cert is acquired) until
// finding the newest block as it is specified by its hash in the request's 'Hash2' field
// (if Hash2==[]byte{0}, then fetch all skipblocks until the current skipchain head one).
// Skipblocks will be returned from the oldest to the newest
func (s *Service) GetValidSbPath(si *network.ServerIdentity, req *GetValidSbPath) (network.Body, error) {
	log.LLvlf2("GetValidSbPath(): Start")
	id := req.ID
	h1 := req.Hash1
	h2 := req.Hash2
	sid := s.getIdentityStorage(id)
	if sid == nil {
		log.LLvlf2("Didn't find identity: %v", id)
		return nil, errors.New("Didn't find identity")
	}

	_, err := s.CheckRefreshCert(id)
	if err != nil {
		return nil, err
	}

	var ok bool
	var sb1 *skipchain.SkipBlock
	if !bytes.Equal(h1, []byte{0}) {
		sb1, ok = sid.getSkipBlockByID(h1)
		if !ok {
			log.LLvlf2("NO VALID PATH: Skipblock with hash: %v not found", h1)
			return nil, fmt.Errorf("NO VALID PATH: Skipblock with hash: %v not found", h1)
		}
	} else {
		// fetch all the blocks starting from the one for the config of
		// which the latest cert is acquired

		_, err = s.CheckRefreshCert(id)
		if err != nil {
			return nil, err
		}

		h1 = sid.CertInfo.SbHash
		sb1, ok = sid.getSkipBlockByID(h1)
		if !ok {
			log.LLvlf2("NO VALID PATH: Skipblock with hash: %v not found", h1)
			return nil, fmt.Errorf("NO VALID PATH: Skipblock with hash: %v not found", h1)
		}
		log.LLvlf2("Last certified skipblock has hash: %v", h1)
	}

	var sb2 *skipchain.SkipBlock
	if !bytes.Equal(h2, []byte{0}) {
		sb2, ok = sid.getSkipBlockByID(h2)
		if !ok {
			log.LLvlf2("NO VALID PATH: Skipblock with hash: %v not found", h2)
			return nil, fmt.Errorf("NO VALID PATH: Skipblock with hash: %v not found", h2)
		}
	} else {
		// fetch skipblocks until finding the current head of the skipchain
		h2 = sid.Data.Hash
		sb2 = sid.Data
		log.LLvlf2("Current head skipblock has hash: %v", h2)
	}

	oldest := sb1
	newest := sb2
	/*
		_, data, _ := network.UnmarshalRegistered(sb1.Data)
		conf1, _ := data.(*common_structs.Config)

		_, data, _ = network.UnmarshalRegistered(sb2.Data)
		conf2, _ := data.(*common_structs.Config)

		newest := sb1
		oldest := sb2
		is_older := conf1.IsOlderConfig(conf2)
		log.LLvl2(is_older)
		if is_older {
			log.LLvlf2("Swapping blocks")
			newest = sb2
			oldest = sb1
		}
	*/
	log.LLvlf2("Oldest skipblock has hash: %v", oldest.Hash)
	log.LLvlf2("Newest skipblock has hash: %v", newest.Hash)
	sbs := make([]*skipchain.SkipBlock, 0)
	sbs = append(sbs, oldest)
	block := oldest
	log.LLvlf2("Skipblock with hash: %v", block.Hash)
	for len(block.ForwardLink) > 0 {
		link := block.ForwardLink[0]
		hash := link.Hash
		log.LLvlf2("Appending skipblock with hash: %v", hash)
		block, ok = sid.getSkipBlockByID(hash)
		if !ok {
			log.LLvlf2("Skipblock with hash: %v not found", hash)
			return nil, fmt.Errorf("Skipblock with hash: %v not found", hash)
		}
		sbs = append(sbs, block)
		if bytes.Equal(hash, sid.Data.Hash) || bytes.Equal(hash, newest.Hash) {
			break
		}
	}

	log.LLvlf2("Num of returned blocks: %v", len(sbs))
	return &GetValidSbPathReply{Skipblocks: sbs, Cert: sid.CertInfo.Cert}, nil
}

func (s *Service) GetCert(si *network.ServerIdentity, req *GetCert) (network.Body, error) {
	sid := s.getIdentityStorage(req.ID)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return nil, errors.New("Didn't find identity")
	}

	/*certs := make([]*ca.Cert, 0)
	for _, certarray := range sid.Certs {
		for _, cert := range certarray {
			certs = append(certs, cert)
		}
	}*/

	_, err := s.CheckRefreshCert(req.ID)
	if err != nil {
		return nil, err
	}

	cert := sid.CertInfo.Cert
	hash := sid.CertInfo.SbHash
	return &GetCertReply{Cert: cert, SbHash: hash}, nil
}

func (s *Service) GetPoF(si *network.ServerIdentity, req *GetPoF) (network.Body, error) {
	sid := s.getIdentityStorage(req.ID)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return nil, errors.New("Didn't find identity")
	}

	pof := sid.PoF
	hash := sid.Data.Hash
	return &GetPoFReply{PoF: pof, SbHash: hash}, nil
}

// Checks whether the current valid cert for a given site is going to expire soon/it has already expired
// in which case a fresh cert by a CA should be acquired
func (s *Service) CheckRefreshCert(id skipchain.SkipBlockID) (bool, error) {
	sid := s.getIdentityStorage(id)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return false, errors.New("Didn't find identity")
	}

	cert_sb_ID := sid.CertInfo.SbHash // hash of the skipblock whose config is the latest certified one
	cert_sb, _ := sid.getSkipBlockByID(cert_sb_ID)
	_, data, _ := network.UnmarshalRegistered(cert_sb.Data)
	cert_conf, _ := data.(*common_structs.Config)
	diff := time.Since(time.Unix(0, cert_conf.Timestamp*1000000))
	diff_int := diff.Nanoseconds() / 1000000

	if cert_conf.MaxDuration-diff_int >= refresh_bound {
		log.LLvlf2("We will not get a fresh cert today because the old one is still \"very\" valid")
		return false, nil
	}

	// Get a fresh cert for the 'latestconf' which is included into the site skipchain's current head block
	_, data, _ = network.UnmarshalRegistered(sid.Data.Data)
	latestconf, _ := data.(*common_structs.Config)

	var prevconf *common_structs.Config
	if !bytes.Equal(id, sid.Data.Hash) { // if site's skipchain is constituted of more than one (the genesis) skiblocks
		// Find 'prevconf' which is included into the second latest head skipblock of the skipchain
		prevhash := sid.Data.BackLinkIds[0]
		prevblock, ok := sid.getSkipBlockByID(prevhash)
		if !ok {
			log.LLvlf2("Skipblock with hash: %v not found", prevhash)
			return false, fmt.Errorf("Skipblock with hash: %v not found", prevhash)
		}
		_, data, _ = network.UnmarshalRegistered(prevblock.Data)
		prevconf, _ = data.(*common_structs.Config)
	} else {
		prevconf = nil
	}

	// Ask for a cert for the 'latestconf'
	cert, _ := s.ca.SignCert(latestconf, prevconf, id)
	certinfo := &common_structs.CertInfo{
		Cert:   cert[0],
		SbHash: sid.Data.Hash,
	}
	sid.CertInfo = certinfo
	//s.setIdentityStorage(id, sid)

	roster := sid.Root.Roster
	replies, err := manage.PropagateStartAndWait(s.Context, roster,
		&PropagateCert{sid}, propagateTimeout, s.Propagate)
	if err != nil {
		return false, err
	}
	if replies != len(roster.List) {
		log.Warn("Did only get", replies, "out of", len(roster.List))
	}

	log.LLvlf2("_ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ ")
	log.LLvlf2("CERT REFRESHED!")
	log.LLvlf2("_ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ _ ")
	return true, nil
}

func (s *Service) PushPublicKey(si *network.ServerIdentity, req *PushPublicKey) (network.Body, error) {
	log.LLvlf2("sidentity.Service's PushPublicKey(): Start")
	//id := req.ID
	roster := req.Roster
	public := req.Public
	serverID := req.ServerID
	/*sid := s.getIdentityStorage(id)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return nil, errors.New("Didn't find identity")
	}
	*/
	key := fmt.Sprintf("tls:%v", serverID)
	//sid.Publics[key] = public
	s.Publics[key] = public
	//s.setIdentityStorage(id, sid)

	//roster := sid.Root.Roster
	replies, err := manage.PropagateStartAndWait(s.Context, roster,
		req, propagateTimeout, s.Propagate)
	if err != nil {
		return nil, err
	}
	if replies != len(roster.List) {
		log.Warn("Did only get", replies, "out of", len(roster.List))
	}

	return &PushPublicKeyReply{}, nil
}

func (s *Service) PullPublicKey(si *network.ServerIdentity, req *PullPublicKey) (network.Body, error) {
	log.LLvlf2("PullPublicKey(): Start")
	//id := req.ID
	serverID := req.ServerID
	/*sid := s.getIdentityStorage(id)
	if sid == nil {
		log.LLvlf2("Didn't find identity")
		return nil, errors.New("Didn't find identity")
	}
	*/
	key := fmt.Sprintf("tls:%v", serverID)
	public := s.Publics[key]

	return &PullPublicKeyReply{Public: public}, nil
}

// getIdentityStorage returns the corresponding IdentityStorage or nil
// if none was found
func (s *Service) getIdentityStorage(id skipchain.SkipBlockID) *Storage {
	s.identitiesMutex.Lock()
	defer s.identitiesMutex.Unlock()
	is, ok := s.Identities[string(id)]
	if !ok {
		return nil
	}
	//log.LLvlf2("******* --------- len: %v", len(s.Identities))
	return is
}

// setIdentityStorage saves an IdentityStorage
func (s *Service) setIdentityStorage(id skipchain.SkipBlockID, is *Storage) {
	//log.LLvlf2("******* --------- setIdentityStorage(): BEFORE len: %v", len(s.Identities))
	s.identitiesMutex.Lock()
	defer s.identitiesMutex.Unlock()
	log.Lvlf3("%s %x %v", s.Context.ServerIdentity(), id[0:8], is.Latest.Device)
	s.Identities[string(id)] = is
	//log.LLvlf2("******* --------- len: %v", len(s.Identities))
}

// saves the actual identity
func (s *Service) save() {
	log.Lvl3("Saving service")
	b, err := network.MarshalRegisteredType(s.StorageMap)
	if err != nil {
		log.Error("Couldn't marshal service:", err)
	} else {
		err = ioutil.WriteFile(s.path+"/sidentity.bin", b, 0660)
		if err != nil {
			log.Error("Couldn't save file:", err)
		}
	}
}

func (s *Service) ClearIdentities() {
	s.Identities = make(map[string]*Storage)
}

// Tries to load the configuration and updates if a configuration
// is found, else it returns an error.
func (s *Service) tryLoad() error {
	configFile := s.path + "/sidentity.bin"
	b, err := ioutil.ReadFile(configFile)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("Error while reading %s: %s", configFile, err)
	}
	if len(b) > 0 {
		_, msg, err := network.UnmarshalRegistered(b)
		if err != nil {
			return fmt.Errorf("!!! Couldn't unmarshal: %s", err)
		}
		log.Lvl3("Successfully loaded")
		s.StorageMap = msg.(*StorageMap)
	}
	return nil
}

func (s *Service) RunLoop(roster *sda.Roster, services []sda.Service) {
	c := time.Tick(s.EpochDuration)
	log.LLvlf2("_______________________________________________________")
	log.LLvlf2("------------------TIMESTAMPER BEGINS-------------------")
	log.LLvlf2("_______________________________________________________")

	for now := range c {
		for _, s := range services {
			service := s.(*Service)
			service.identitiesMutex.Lock()
		}
		log.LLvlf2("_______________________________________________________")
		log.LLvlf2("START OF A TIMESTAMPER ROUND")
		log.LLvlf2("_______________________________________________________")
		data := make([][]byte, 0)
		data2 := make([]crypto.HashID, 0)
		ids := make([]skipchain.SkipBlockID, 0)
		for _, sid := range s.Identities {
			latestconf := sid.Latest
			hash, _ := latestconf.Hash()
			data = append(data, []byte(hash))
			data2 = append(data2, hash)
			log.LLvlf2("site: %v, %v", latestconf.FQDN, []byte(hash))
			ids = append(ids, sid.ID)
		}
		num := len(s.Identities)
		if num > 0 {
			log.LLvl2("------- Signing tree root with timestamp:", now, "got", num, "requests")

			// create merkle tree and message to be signed:
			root, proofs := crypto.ProofTree(sha256.New, data2)
			//msg := RecreateSignedMsg(root, now.Unix())
			timestamp := time.Now().Unix() * 1000
			msg := RecreateSignedMsg(root, timestamp)
			log.LLvlf2("------ Before signing")
			for _, server := range roster.List {
				log.LLvlf2("%v", server)
			}

			signature := s.signMsg(msg)
			//signature := []byte{0}
			//log.LLvlf2("%v", msg)
			log.LLvlf2("--------- %s: Signed a message.\n", time.Now().Format("Mon Jan 2 15:04:05 -0700 MST 2006"))

			i := 0
			log.LLvlf2("sites: %v proofs: %v", len(s.Identities), len(proofs))
			log.LLvlf2("root hash: %v", []byte(root))
			log.LLvlf2("timestamp: %v", timestamp)
			log.LLvlf2("signature: %v", signature)
			sids := make([]*Storage, 0)
			for _, id := range ids {
				sid := s.Identities[string(id)]
				pof := &common_structs.SignatureResponse{
					//Timestamp: now.Unix(),
					// the number of ms elapsed since January 1, 1970 UTC
					ID:        id,
					Timestamp: timestamp,
					Proof:     proofs[i],
					Root:      root,
					// Collective signature on Timestamp||hash(treeroot)
					Signature: signature,
				}

				// check the validity of pofs
				signedmsg := RecreateSignedMsg(root, timestamp)
				publics := make([]abstract.Point, 0)
				for _, proxy := range roster.List {
					publics = append(publics, proxy.Public)
				}
				err := swupdate.VerifySignature(network.Suite, publics, signedmsg, signature)
				if err != nil {
					log.LLvlf2("Warm Key Holders' signature doesn't verify")
				}
				// verify inclusion proof
				origmsg := data2[i]
				log.LLvlf2("site: %v, proof: %v", sid.Latest.FQDN, proofs[i])
				log.LLvlf2("%v", []byte(origmsg))
				validproof := pof.Proof.Check(sha256.New, root, []byte(origmsg))
				if !validproof {
					log.LLvlf2("Invalid inclusion proof!")
				}
				sid.PoF = pof
				sids = append(sids, sid)
				i++
			}
			log.LLvlf2("Everything OK with the proofs")
			replies, err := manage.PropagateStartAndWait(s.Context, roster,
				&PropagatePoF{Storages: sids}, propagateTimeout, s.Propagate)

			if err != nil {
				log.ErrFatal(err, "Couldn't send")
			}

			if replies != len(roster.List) {
				log.Warn("Did only get", replies, "out of", len(roster.List))
			}

		} else {
			log.Lvl3("No follow-sites at epoch:", time.Now().Format("Mon Jan 2 15:04:05 -0700 MST 2006"))
		}
		log.LLvlf2("_______________________________________________________")
		log.LLvlf2("END OF A TIMESTAMPER ROUND")
		log.LLvlf2("_______________________________________________________")
		for _, s := range services {
			service := s.(*Service)
			service.identitiesMutex.Unlock()
		}
	}
}

//func (s *Service) cosiSign(roster *sda.Roster, msg []byte) []byte {
func (s *Service) cosiSign(msg []byte) []byte {
	log.LLvlf2("server: %s", s.String())
	sdaTree := s.TheRoster.GenerateBinaryTree()
	//log.LLvlf2("cosiSign(): 1 %v", sdaTree.Dump())
	tni := s.NewTreeNodeInstance(sdaTree, sdaTree.Root, swupdate.ProtocolName)
	//log.LLvlf2("cosiSign(): 2 %v", tni)
	pi, err := swupdate.NewCoSiUpdate(tni, dummyVerfier)
	if err != nil {
		log.LLvl2("Couldn't make new protocol: " + err.Error())
		panic("Couldn't make new protocol: " + err.Error())
	}
	//log.LLvlf2("cosiSign(): 3")
	s.RegisterProtocolInstance(pi)

	pi.SigningMessage(msg)
	// Take the raw message (already expecting a hash for the timestamp
	// service)
	response := make(chan []byte)
	//log.LLvlf2("cosiSign(): 5 %v", msg)
	pi.RegisterSignatureHook(func(sig []byte) {
		response <- sig
	})

	go pi.Dispatch()

	go pi.Start()

	res := <-response
	log.LLvlf2("cosiSign(): Received cosi response")
	return res

}

// RecreateSignedMsg is a helper that can be used by the client to recreate the
// message signed by the timestamp service (which is treeroot||timestamp)
func RecreateSignedMsg(treeroot []byte, timestamp int64) []byte {
	timeB := timestampToBytes(timestamp)
	m := make([]byte, len(treeroot)+len(timeB))
	m = append(m, treeroot...)
	m = append(m, timeB...)
	return m
}

func newIdentityService(c *sda.Context, path string) sda.Service {
	s := &Service{
		ServiceProcessor: sda.NewServiceProcessor(c),
		path:             path,
		skipchain:        skipchain.NewClient(),
		ca:               ca.NewCSRDispatcher(),
		//stamper:          timestamp.NewClient(),
		StorageMap: &StorageMap{make(map[string]*Storage)},
		Publics:    make(map[string]abstract.Point),
		//EpochDuration:    time.Millisecond * 250000,
		EpochDuration: time.Millisecond * 1000,
	}
	s.signMsg = s.cosiSign
	//log.LLvlf2("******* --------- len: %v", len(s.Identities))
	if err := s.tryLoad(); err != nil {
		log.Error(err)
	}
	for _, f := range []interface{}{s.ProposeSend, s.ProposeVote,
		s.CreateIdentity, s.ProposeUpdate, s.ConfigUpdate,
		//s.GetUpdateChain,
		s.GetValidSbPath, s.PushPublicKey, s.PullPublicKey, s.GetCert, s.GetPoF,
	} {
		if err := s.RegisterMessage(f); err != nil {
			log.Fatal("Registration error:", err)
		}
	}
	return s
}
