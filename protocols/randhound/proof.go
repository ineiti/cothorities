package randhound

import (
	"errors"

	"github.com/dedis/cothority/crypto"
	"github.com/dedis/crypto/abstract"
	"github.com/dedis/crypto/poly"
	"github.com/dedis/crypto/random"
)

// Package (XXX: name) provides functionality to show equality of discrete
// logarithms (dlog) through non-interactive zero-knowledge (NIZK) proofs.

// Proof resembles a NIZK dlog-equality proof. Allows to handle multiple proofs.
type Proof struct {
	suite abstract.Suite
	base  []ProofBase
	core  []ProofCore
}

// ProofBase contains the base points against which the core proof is created.
type ProofBase struct {
	g abstract.Point
	h abstract.Point
}

// ProofCore contains the core elements of the NIZK dlog-equality proof.
type ProofCore struct {
	c  abstract.Scalar // challenge
	r  abstract.Scalar // response
	vG abstract.Point  // public commitment with respect to base point G
	vH abstract.Point  // public commitment with respect to base point H
}

// NewProof creates a new NIZK dlog-equality proof.
func NewProof(suite abstract.Suite, g []abstract.Point, h []abstract.Point, core []ProofCore) (*Proof, error) {

	if len(g) != len(h) {
		return nil, errors.New("Received non-matching number of points")
	}

	n := len(g)
	base := make([]ProofBase, n)
	for i := 0; i < n; i++ {
		base[i] = ProofBase{g: g[i], h: h[i]}
	}

	return &Proof{suite: suite, base: base, core: core}, nil
}

// Setup initializes the proof by randomly selecting a commitment v and then
// determining the challenge c = H(xG,xH,vG,vH) and the response r = v - cx.
func (p *Proof) Setup(scalar ...abstract.Scalar) ([]abstract.Point, []abstract.Point, []ProofCore, error) {

	if len(scalar) != len(p.base) {
		return nil, nil, nil, errors.New("Received unexpected number of scalars")
	}

	n := len(scalar)
	p.core = make([]ProofCore, n)
	xG := make([]abstract.Point, n)
	xH := make([]abstract.Point, n)
	for i, x := range scalar {

		xG[i] = p.suite.Point().Mul(p.base[i].g, x)
		xH[i] = p.suite.Point().Mul(p.base[i].h, x)

		// Commitment
		v := p.suite.Scalar().Pick(random.Stream)
		vG := p.suite.Point().Mul(p.base[i].g, v)
		vH := p.suite.Point().Mul(p.base[i].h, v)

		// Challenge
		cb, err := crypto.HashArgsSuite(p.suite, xG[i], xH[i], vG, vH)
		if err != nil {
			return nil, nil, nil, err
		}
		c := p.suite.Scalar().Pick(p.suite.Cipher(cb))

		// Response
		r := p.suite.Scalar()
		r.Mul(x, c).Sub(v, r)

		p.core[i] = ProofCore{c, r, vG, vH}
	}

	return xG, xH, p.core, nil
}

// SetupCollective is similar to Setup with the difference that the challenge
// is computed as the hash over all base points and commitments.
func (p *Proof) SetupCollective(scalar ...abstract.Scalar) ([]abstract.Point, []abstract.Point, []ProofCore, error) {

	if len(scalar) != len(p.base) {
		return nil, nil, nil, errors.New("Received unexpected number of scalars")
	}

	n := len(scalar)
	p.core = make([]ProofCore, n)
	v := make([]abstract.Scalar, n)
	xG := make([]abstract.Point, n)
	xH := make([]abstract.Point, n)
	vG := make([]abstract.Point, n)
	vH := make([]abstract.Point, n)
	for i, x := range scalar {

		xG[i] = p.suite.Point().Mul(p.base[i].g, x)
		xH[i] = p.suite.Point().Mul(p.base[i].h, x)

		// Commitments
		v[i] = p.suite.Scalar().Pick(random.Stream)
		vG[i] = p.suite.Point().Mul(p.base[i].g, v[i])
		vH[i] = p.suite.Point().Mul(p.base[i].h, v[i])
	}

	// Collective challenge
	cb, err := crypto.HashArgsSuite(p.suite, xG, xH, vG, vH)
	if err != nil {
		return nil, nil, nil, err
	}
	c := p.suite.Scalar().Pick(p.suite.Cipher(cb))

	// Responses
	for i, x := range scalar {
		r := p.suite.Scalar()
		r.Mul(x, c).Sub(v[i], r)
		p.core[i] = ProofCore{c, r, vG[i], vH[i]}
	}

	return xG, xH, p.core, nil
}

// Verify validates the proof against the given input by checking that
// vG == rG + c(xG) and vH == rH + c(xH).
func (p *Proof) Verify(xG []abstract.Point, xH []abstract.Point) ([]int, error) {

	if len(xG) != len(xH) {
		return nil, errors.New("Received unexpected number of points")
	}

	var failed []int
	for i := 0; i < len(p.base); i++ {

		rG := p.suite.Point().Mul(p.base[i].g, p.core[i].r)
		rH := p.suite.Point().Mul(p.base[i].h, p.core[i].r)
		cxG := p.suite.Point().Mul(xG[i], p.core[i].c)
		cxH := p.suite.Point().Mul(xH[i], p.core[i].c)
		a := p.suite.Point().Add(rG, cxG)
		b := p.suite.Point().Add(rH, cxH)

		if !(p.core[i].vG.Equal(a) && p.core[i].vH.Equal(b)) {
			failed = append(failed, i)
		}
	}

	if len(failed) != 0 {
		return failed, errors.New("Verification of discrete logarithm proofs failed")
	}

	return nil, nil
}

// PVSS implements public verifiable secret sharing.
type PVSS struct {
	suite abstract.Suite // Suite
	h     abstract.Point // Base point for polynomial commits
	t     int            // Secret sharing threshold
}

// NewPVSS creates a new PVSS struct using the given suite, base point, and
// secret sharing threshold.
func NewPVSS(s abstract.Suite, h abstract.Point, t int) *PVSS {
	return &PVSS{suite: s, h: h, t: t}
}

// Split creates PVSS shares encrypted with the keys in X and furthermore
// provides NIZK encryption consistency proofs.
func (pv *PVSS) Split(X []abstract.Point, secret abstract.Scalar) ([]abstract.Point, []ProofCore, []byte, error) {

	n := len(X)

	// Create secret sharing polynomial
	p := new(poly.PriPoly).Pick(pv.suite, pv.t, secret, random.Stream)

	// Create secret set of shares
	s := new(poly.PriShares).Split(p, n)

	// Create public polynomial commitments with respect to basis H
	P := new(poly.PubPoly).Commit(p, pv.h)

	// Prepare data for verification proofs
	share := make([]abstract.Scalar, n)
	H := make([]abstract.Point, n)
	for i := 0; i < n; i++ {
		share[i] = s.Share(i)
		H[i] = pv.h
	}

	// Create encryption consistency proof
	proof, err := NewProof(pv.suite, H, X, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	_, sX, encProof, err := proof.SetupCollective(share...)
	if err != nil {
		return nil, nil, nil, err
	}

	polyBin, err := P.MarshalBinary()
	if err != nil {
		return nil, nil, nil, err
	}

	return sX, encProof, polyBin, nil
}

// Verify checks that log_H(sH) == log_X(sX) using the given proof.
func (pv *PVSS) Verify(H abstract.Point, X []abstract.Point, sH []abstract.Point, sX []abstract.Point, core []ProofCore) ([]int, error) {

	n := len(X)
	Y := make([]abstract.Point, n)
	for i := 0; i < n; i++ {
		Y[i] = H
	}
	proof, err := NewProof(pv.suite, Y, X, core)
	if err != nil {
		return nil, err
	}
	return proof.Verify(sH, sX)
}

// Commits reconstructs a list of commits from the given polynomials and indices.
func (pv *PVSS) Commits(polyBin [][]byte, index []int) ([]abstract.Point, error) {

	if len(polyBin) != len(index) {
		return nil, errors.New("Inputs have different lengths")
	}

	n := len(polyBin)
	sH := make([]abstract.Point, n)
	for i := 0; i < n; i++ {
		P := new(poly.PubPoly)
		P.Init(pv.suite, pv.t, pv.h)
		if err := P.UnmarshalBinary(polyBin[i]); err != nil {
			return nil, err
		}
		sH[i] = P.Eval(index[i]) // XXX: probably needs an index parameter to better control which commits are reconstructed
	}
	return sH, nil
}

// Reveal decrypts the shares in xS using the secret key x and creates an NIZK
// decryption consistency proof.
func (pv *PVSS) Reveal(x abstract.Scalar, xS []abstract.Point) ([]abstract.Point, []ProofCore, error) {

	// Decrypt shares
	S := make([]abstract.Point, len(xS))
	G := make([]abstract.Point, len(xS))
	y := make([]abstract.Scalar, len(xS))
	for i := range xS {
		S[i] = pv.suite.Point().Mul(xS[i], pv.suite.Scalar().Inv(x))
		G[i] = pv.suite.Point().Base()
		y[i] = x
	}

	proof, err := NewProof(pv.suite, G, S, nil)
	if err != nil {
		return nil, nil, err
	}
	_, _, decProof, err := proof.Setup(y...)
	if err != nil {
		return nil, nil, err
	}
	return S, decProof, nil
}

// Recover recreates the PVSS secret from the given shares.
func (pv *PVSS) Recover(S []abstract.Point) (abstract.Point, error) {

	if len(S) < pv.t {
		return nil, errors.New("Not enough shares to recover secret")
	}

	pp := new(poly.PubPoly).InitNull(pv.suite, pv.t, pv.suite.Point().Base())
	ps := new(poly.PubShares).Split(pp, len(S)) // XXX: ackward way to init shares

	for i := 0; i < len(S); i++ {
		ps.SetShare(i, S[i])
	}

	return ps.SecretCommit(), nil
}
