package platform_test
import (
	"io/ioutil"
	dbg "github.com/dedis/cothority/lib/debug_lvl"
	"testing"
	"github.com/dedis/cothority/deploy/platform"
	"strings"
)

var testfile = `Machines = 8
App = "sign"

Hpn, Rounds
2, 30
4, 30`

func TestReadRunfile(t *testing.T) {
	dbg.DebugVisible = 0
	tplat := &TPlat{}

	tmpfile := "/tmp/testrun.toml"
	err := ioutil.WriteFile(tmpfile, []byte(testfile), 0666)
	if err != nil {
		dbg.Fatal("Couldn't create file:", err)
	}

	tests := platform.ReadRunFile(tplat, tmpfile)
	dbg.Lvl2(tplat)
	dbg.Lvlf2("%+v\n", tests[0])
	if tplat.App != "sign" {
		dbg.Fatal("App should be 'sign'")
	}
	if len(tests) != 2 {
		dbg.Fatal("There should be 2 tests")
	}
	if ! strings.Contains(string(tests[0]), "Machines = 8\n"){
		dbg.Fatal("Machines = 8 has not been copied into RunConfig")
	}
}

type TPlat struct {
	App      string
	Machines int
}

func (t *TPlat)Configure() {}
func (t *TPlat)Build(s string) error { return nil }
func (t *TPlat)Deploy(rc platform.RunConfig) error { return nil}
func (t *TPlat)Start() error { return nil}
func (t *TPlat)Stop() error {return nil}
