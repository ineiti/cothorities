package example_channels_test

import (
	"testing"
	"time"

	"gopkg.in/dedis/cothority.v0/lib/dbg"
	"gopkg.in/dedis/cothority.v0/lib/network"
	"gopkg.in/dedis/cothority.v0/lib/sda"
	"gopkg.in/dedis/cothority.v0/protocols/example/channels"
)

// Tests a 2-node system
func TestNode(t *testing.T) {
	defer dbg.AfterTest(t)
	dbg.TestOutput(testing.Verbose(), 4)
	local := sda.NewLocalTest()
	nbrNodes := 2
	_, _, tree := local.GenTree(nbrNodes, false, true, true)
	defer local.CloseAll()

	p, err := local.StartProtocol("ExampleChannels", tree)
	if err != nil {
		t.Fatal("Couldn't start protocol:", err)
	}
	protocol := p.(*example_channels.ProtocolExampleChannels)
	timeout := network.WaitRetry * time.Duration(network.MaxRetry*nbrNodes*2) * time.Millisecond
	select {
	case children := <-protocol.ChildCount:
		dbg.Lvl2("Instance 1 is done")
		if children != nbrNodes {
			t.Fatal("Didn't get a child-cound of", nbrNodes)
		}
	case <-time.After(timeout):
		t.Fatal("Didn't finish in time")
	}
}
