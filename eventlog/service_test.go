package eventlog

import (
	"testing"

	"github.com/dedis/student_18_omniledger/omniledger/darc"
	"github.com/stretchr/testify/require"
	"gopkg.in/dedis/kyber.v2/suites"
	"gopkg.in/dedis/onet.v2"
	"gopkg.in/dedis/onet.v2/log"
)

var tSuite = suites.MustFind("Ed25519")

func TestMain(m *testing.M) {
	log.MainTest(m)
}

func TestService_Init(t *testing.T) {
	s := newSer(t)
	defer s.close()

	// With no signer: error
	_, err := s.services[0].Init(&InitRequest{})
	require.NotNil(t, err)

	owner1 := darc.NewSignerEd25519(nil, nil)
	rules1 := darc.InitRules([]*darc.Identity{owner1.Identity()}, []*darc.Identity{})
	d1 := darc.NewDarc(rules1, []byte("eventlog writer"))

	reply, err := s.services[0].Init(&InitRequest{
		Roster: *s.roster,
		Writer: *d1,
	})
	require.Nil(t, err)
	require.NotNil(t, reply.ID)
	require.False(t, reply.ID.IsNull())
}

type ser struct {
	local    *onet.LocalTest
	hosts    []*onet.Server
	roster   *onet.Roster
	services []*Service
}

func (s *ser) close() {
	s.local.CloseAll()
	for _, x := range s.services {
		close(x.omni.CloseQueues)
	}
}

func newSer(t *testing.T) *ser {
	s := &ser{
		local: onet.NewTCPTest(tSuite),
	}
	s.hosts, s.roster, _ = s.local.GenTree(2, true)

	for _, sv := range s.local.GetServices(s.hosts, sid) {
		service := sv.(*Service)
		s.services = append(s.services, service)
	}

	return s
}
