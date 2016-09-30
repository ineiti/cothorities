package randhound

import (
	"time"

	"github.com/BurntSushi/toml"
	"github.com/dedis/cothority/log"
	"github.com/dedis/cothority/sda"
)

func init() {
	sda.SimulationRegister("RandHound", NewRHSimulation)
}

// RHSimulation implements a RandHound simulation
type RHSimulation struct {
	sda.SimulationBFTree
	Groups  int
	Faulty  int
	Purpose string
}

// NewRHSimulation creates a new RandHound simulation
func NewRHSimulation(config string) (sda.Simulation, error) {
	rhs := &RHSimulation{}
	_, err := toml.Decode(config, rhs)
	if err != nil {
		return nil, err
	}
	return rhs, nil
}

// Setup configures a RandHound simulation with certain parameters
func (rhs *RHSimulation) Setup(dir string, hosts []string) (*sda.SimulationConfig, error) {
	sim := new(sda.SimulationConfig)
	rhs.CreateRoster(sim, hosts, 2000)
	err := rhs.CreateTree(sim)
	return sim, err
}

// Run initiates a RandHound simulation
func (rhs *RHSimulation) Run(config *sda.SimulationConfig) error {
	client, err := config.Overlay.CreateProtocolSDA("RandHound", config.Tree)
	if err != nil {
		return err
	}
	rh, _ := client.(*RandHound)
	err = rh.Setup(rhs.Hosts, rhs.Faulty, rhs.Groups, rhs.Purpose)
	if err != nil {
		return err
	}
	if err := rh.StartProtocol(); err != nil {
		log.Error("Error while starting protcol:", err)
	}

	select {
	case <-rh.Done:
		log.Print("RandHound - done")
		random, transcript, err := rh.Random()
		if err != nil {
			return err
		}
		log.Print("RandHound - collective randomness: ok")
		_ = random
		_ = transcript

	case <-time.After(time.Second * time.Duration(rhs.Hosts) * 5):
		log.Print("RandHound - time out")
	}

	return nil

}
