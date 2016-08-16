package randhound

import (
	"errors"
	"fmt"

	"github.com/dedis/cothority/crypto"
	"github.com/dedis/crypto/abstract"
	"github.com/dedis/crypto/random"
)

// Package (XXX: name) provides functionality to show equality of discrete
// logarithms (dlog) through non-interactive zero-knowledge (NIZK) proofs.

// Proof resembles a NIZK dlog-equality proof. Can hold multiple proofs.
type Proof struct {
	suite abstract.Suite
	base  []ProofBase
	core  []ProofCore
}

// ProofBase contains the base points for a proof
type ProofBase struct {
	g abstract.Point
	h abstract.Point
}

// ProofCore ...
type ProofCore struct {
	c  abstract.Scalar // challenge
	r  abstract.Scalar // response
	gv abstract.Point  // public commitment with respect to base point g
	hv abstract.Point  // public commitment with respect to base point h
}

// NewProof creates a new NIZK dlog-equality proof.
func NewProof(suite abstract.Suite, point ...abstract.Point) (*Proof, error) {

	if len(point)%2 != 0 {
		return nil, errors.New("Received odd number of points")
	}

	base := make([]ProofBase, len(point)/2)

	for i := 0; i < len(point)/2; i += 1 {
		base[i] = ProofBase{g: point[2*i], h: point[2*i+1]}
	}

	return &Proof{suite: suite, base: base}, nil
}

// Setup initializes the proof by randomly selecting a commitment v and then
// determining the challenge c = H(G,H,x,v) and the response r = v - cx.
func (p *Proof) Setup(scalar ...abstract.Scalar) error {

	if len(scalar) != len(p.base) {
		return errors.New("Received unexpected number of scalars")
	}

	p.core = make([]ProofCore, len(scalar))
	for i, x := range scalar {

		gx := p.suite.Point().Mul(p.base[i].g, x)
		hx := p.suite.Point().Mul(p.base[i].h, x)

		// Commitment
		v := p.suite.Scalar().Pick(random.Stream)
		gv := p.suite.Point().Mul(p.base[i].g, v)
		hv := p.suite.Point().Mul(p.base[i].h, v)

		// Challenge
		cb, err := crypto.HashArgsSuite(p.suite, gx, hx, x, v)
		if err != nil {
			return err
		}
		c := p.suite.Scalar().Pick(p.suite.Cipher(cb))

		// Response
		r := p.suite.Scalar()
		r.Mul(x, c).Sub(v, r)

		p.core[i] = ProofCore{c, r, gv, hv}
	}

	return nil
}

// SetupCollective ...
//func (p *Proof) SetupCollective(scalar ...abstract.Scalar) error {

//if len(scalar) != len(p.base) {
//return errors.New("Received number of points does not match number of base points")
//}

//p.core = make([]ProofCore, len(scalar))
//v := make([]abstract.Scalar, len(scalar))
//X := make([]abstract.Point, len(scalar))
//Y := make([]abstract.Point, len(scalar))
//V := make([]abstract.Point, 2*len(scalar))
//for i, x := range scalar {

//X[i] = p.suite.Point().Mul(p.base[i].g, x) // gx
//Y[i] = p.suite.Point().Mul(p.base[i].h, x) // hx

//// Commitments
//v[i] = p.suite.Scalar().Pick(random.Stream)     // v
//V[i] = p.suite.Point().Mul(p.base[i].g, v[i])   // gv
//V[i+1] = p.suite.Point().Mul(p.base[i].h, v[i]) // hv
//}

//X = append(X, Y...)
//X = append(X, V...)

//// Collective challenge
//cb, err := crypto.HashArgsSuite(p.suite, X...)
//if err != nil {
//return err
//}
//c := p.suite.Scalar().Pick(p.suite.Cipher(cb))

//// Responses
//for i, x := range scalar {
//r := p.suite.Scalar()
//r.Mul(x, c).Sub(v[i], r)
//p.core[i] = ProofCore{c, r, V[i], V[i+1]}
//}

//return nil
//}

// Verify validates the proof against the given input by checking that
// v * G == r * G + c * (x * G) and v * H == r * H + c * (x * H).
func (p *Proof) Verify(point ...abstract.Point) ([]int, error) {

	if len(point) != 2*len(p.base) {
		return nil, errors.New("Received unexpected number of points")
	}

	failed := make([]int, 0)
	for i := 0; i < len(p.base); i += 2 {

		gr := p.suite.Point().Mul(p.base[i].g, p.core[i].r)
		hr := p.suite.Point().Mul(p.base[i].h, p.core[i].r)
		gxc := p.suite.Point().Mul(point[2*i], p.core[i].c)
		hxc := p.suite.Point().Mul(point[2*i+1], p.core[i].c)
		x := p.suite.Point().Add(gr, gxc)
		y := p.suite.Point().Add(hr, hxc)

		fmt.Println(p.core[i].gv.Equal(x), p.core[i].hv.Equal(y))

		if !(p.core[i].gv.Equal(x) && p.core[i].hv.Equal(y)) {
			failed = append(failed, i)
		}
	}
	return failed, nil
}
