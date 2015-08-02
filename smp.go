package otr3

import (
	"crypto/sha256"
	"math/big"
)

type smp struct {
	state    smpState
	question *string
	secret   *big.Int
	s1       smp1State
	s2       smp2State
	s3       smp3State
}

const smpVersion = 1

// SMPQuestion returns the current SMP question and ok if there is one, and not ok if there isn't one.
func (c *Conversation) SMPQuestion() (string, bool) {
	if c.smp.question == nil {
		return "", false
	}
	return *c.smp.question, true
}

func generateSMPSecret(initiatorFingerprint, recipientFingerprint, ssid, secret []byte) []byte {
	h := sha256.New()
	h.Write([]byte{smpVersion})
	h.Write(initiatorFingerprint)
	h.Write(recipientFingerprint)
	h.Write(ssid)
	h.Write(secret)
	return h.Sum(nil)
}

func generateDZKP(r, a, c *big.Int) *big.Int {
	return subMod(r, mul(a, c), q)
}

func generateZKP(r, a *big.Int, ix byte) (c, d *big.Int) {
	c = hashMPIsBN(nil, ix, modExp(g1, r))
	d = generateDZKP(r, a, c)
	return
}

func verifyZKP(d, gen, c *big.Int, ix byte) bool {
	r := modExp(g1, d)
	s := modExp(gen, c)
	t := hashMPIsBN(nil, ix, mulMod(r, s, p))
	return eq(c, t)
}

func verifyZKP2(g2, g3, d5, d6, pb, qb, cp *big.Int, ix byte) bool {
	l := mulMod(
		modExp(g3, d5),
		modExp(pb, cp),
		p)
	r := mulMod(mul(modExp(g1, d5),
		modExp(g2, d6)),
		modExp(qb, cp),
		p)
	t := hashMPIsBN(nil, ix, l, r)
	return eq(cp, t)
}

func verifyZKP3(cp, g2, g3, d5, d6, pa, qa *big.Int, ix byte) bool {
	l := mulMod(modExp(g3, d5), modExp(pa, cp), p)
	r := mulMod(mul(modExp(g1, d5), modExp(g2, d6)), modExp(qa, cp), p)
	t := hashMPIsBN(nil, ix, l, r)
	return eq(cp, t)
}

func verifyZKP4(cr, g3a, d7, qaqb, ra *big.Int, ix byte) bool {
	l := mulMod(modExp(g1, d7), modExp(g3a, cr), p)
	r := mulMod(modExp(qaqb, d7), modExp(ra, cr), p)
	t := hashMPIsBN(nil, ix, l, r)
	return eq(cr, t)
}

func genSMPTLV(tp uint16, mpis ...*big.Int) tlv {
	data := make([]byte, 0, 1000)

	data = appendWord(data, uint32(len(mpis)))
	data = appendMPIs(data, mpis...)
	length := uint16(len(data))
	out := tlv{
		tlvType:   tp,
		tlvLength: length,
		tlvValue:  data,
	}

	return out
}
