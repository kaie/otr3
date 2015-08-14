package otr3

import (
	"bytes"
	"crypto/sha256"
)

const minimumMessageLength = 3 // length of protocol version (SHORT) and message type (BYTE)

func (c *Conversation) generateNewDHKeyPair() error {
	x, err := c.randMPI(make([]byte, 40))
	if err != nil {
		return err
	}

	c.keys.generateNewDHKeyPair(x)
	wipeBigInt(x)

	return nil
}

func (c *Conversation) akeHasFinished() error {
	c.ake.wipe()

	previousMsgState := c.msgState
	c.msgState = encrypted
	defer c.signalSecurityEventIf(previousMsgState != encrypted, GoneSecure)
	defer c.signalSecurityEventIf(previousMsgState == encrypted, StillSecure)

	if c.ourKey.PublicKey == *c.theirKey {
		c.messageEvent(MessageEventMessageReflected)
	}

	return c.generateNewDHKeyPair()
}

func (c *Conversation) processAKE(msgType byte, msg []byte) (toSend []messageWithHeader, err error) {
	c.ensureAKE()

	var toSendSingle messageWithHeader
	var toSendExtra messageWithHeader

	switch msgType {
	case msgTypeDHCommit:
		c.ake.state, toSendSingle, err = c.ake.state.receiveDHCommitMessage(c, msg)
	case msgTypeDHKey:
		c.ake.state, toSendSingle, err = c.ake.state.receiveDHKeyMessage(c, msg)
	case msgTypeRevealSig:
		c.ake.state, toSendSingle, err = c.ake.state.receiveRevealSigMessage(c, msg)
		toSendExtra, _ = c.maybeRetransmit()
	case msgTypeSig:
		c.ake.state, toSendSingle, err = c.ake.state.receiveSigMessage(c, msg)
		toSendExtra, _ = c.maybeRetransmit()
	default:
		err = newOtrErrorf("unknown message type 0x%X", msgType)
	}
	toSend = compactMessagesWithHeader(toSendSingle, toSendExtra)
	return
}

type authStateBase struct{}
type authStateNone struct{ authStateBase }
type authStateAwaitingDHKey struct{ authStateBase }
type authStateAwaitingRevealSig struct{ authStateBase }
type authStateAwaitingSig struct {
	authStateBase
	// revealSigMsg is only used to store the message so we can re-transmit it if needed
	revealSigMsg messageWithHeader
}

type authState interface {
	receiveDHCommitMessage(*Conversation, []byte) (authState, messageWithHeader, error)
	receiveDHKeyMessage(*Conversation, []byte) (authState, messageWithHeader, error)
	receiveRevealSigMessage(*Conversation, []byte) (authState, messageWithHeader, error)
	receiveSigMessage(*Conversation, []byte) (authState, messageWithHeader, error)
	identity() int
	identityString() string
}

func (authStateBase) receiveDHCommitMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return authStateNone{}.receiveDHCommitMessage(c, msg)
}

func (s authStateNone) receiveDHCommitMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	//We have engaged in a new AKE so we forget all previous keys
	c.keys = c.keys.wipeAndKeepRevealKeys()
	c.ake.wipe()

	dhKeyMsg, err := c.dhKeyMessage()
	if err != nil {
		return s, nil, err
	}

	dhKeyMsg, err = c.wrapMessageHeader(msgTypeDHKey, dhKeyMsg)
	if err != nil {
		return s, nil, err
	}

	if err = c.processDHCommit(msg); err != nil {
		return s, nil, err
	}

	return authStateAwaitingRevealSig{}, dhKeyMsg, nil
}

func (s authStateAwaitingRevealSig) receiveDHCommitMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	//As per spec, we forget the old DH-commit (received before we sent the DH-Key)
	//and use this one, so we forget all the keys
	c.keys = c.keys.wipeAndKeepRevealKeys()
	c.ake.wipeGX()

	if err := c.processDHCommit(msg); err != nil {
		return s, nil, err
	}

	dhKeyMsg, err := c.wrapMessageHeader(msgTypeDHKey, c.serializeDHKey())
	if err != nil {
		return s, nil, err
	}

	return authStateAwaitingRevealSig{}, dhKeyMsg, nil
}

func (s authStateAwaitingDHKey) receiveDHCommitMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	newMsg, _, ok1 := extractData(msg)
	_, theirHashedGx, ok2 := extractData(newMsg)

	if !ok1 || !ok2 {
		return s, nil, errInvalidOTRMessage
	}

	gxMPI := appendMPI(nil, c.ake.theirPublicValue)
	hashedGx := sha256.Sum256(gxMPI)
	//If yours is the higher hash value:
	//Ignore the incoming D-H Commit message, but resend your D-H Commit message.
	if bytes.Compare(hashedGx[:], theirHashedGx) == 1 {
		dhCommitMsg, err := c.wrapMessageHeader(msgTypeDHCommit, c.serializeDHCommit(c.ake.theirPublicValue))
		if err != nil {
			return s, nil, err
		}

		return authStateAwaitingRevealSig{}, dhCommitMsg, nil
	}

	//Otherwise:
	//Forget your old gx value that you sent (encrypted) earlier, and pretend you're in AUTHSTATE_NONE; i.e. reply with a D-H Key Message, and transition authstate to AUTHSTATE_AWAITING_REVEALSIG.
	//This is done as part of receiving a DHCommit message in AUTHSTATE_NONE
	return authStateNone{}.receiveDHCommitMessage(c, msg)
}

func (s authStateNone) receiveDHKeyMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingRevealSig) receiveDHKeyMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingDHKey) receiveDHKeyMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	_, err := c.processDHKey(msg)
	if err != nil {
		return s, nil, err
	}

	var revealSigMsg []byte
	if revealSigMsg, err = c.revealSigMessage(); err != nil {
		return s, nil, err
	}
	revealSigMsg, err = c.wrapMessageHeader(msgTypeRevealSig, revealSigMsg)
	if err != nil {
		return s, nil, err
	}

	//Why do we change keyManagementContext during the AKE if it is supposed
	//to be used only after the AKE has finished?
	c.keys.setTheirCurrentDHPubKey(c.ake.theirPublicValue)
	c.keys.setOurCurrentDHKeys(c.ake.secretExponent, c.ake.ourPublicValue)
	c.keys.ourCounter++

	c.sentRevealSig = true

	return authStateAwaitingSig{revealSigMsg: revealSigMsg}, revealSigMsg, nil
}

func (s authStateAwaitingSig) receiveDHKeyMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	isSame, err := c.processDHKey(msg)
	if err != nil {
		return s, nil, err
	}

	if isSame {
		// Retransmit the Reveal Signature Message
		return s, s.revealSigMsg, nil
	}

	return s, nil, nil
}

func (s authStateNone) receiveRevealSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingRevealSig) receiveRevealSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	err := c.processRevealSig(msg)

	if err != nil {
		return s, nil, err
	}

	sigMsg, err := c.sigMessage()
	if err != nil {
		return s, nil, err
	}

	sigMsg, err = c.wrapMessageHeader(msgTypeSig, sigMsg)
	if err != nil {
		return s, nil, err
	}

	//Why do we change keyManagementContext during the AKE if it is supposed
	//to be used only after the AKE has finished?
	c.keys.setTheirCurrentDHPubKey(c.ake.theirPublicValue)
	c.keys.setOurCurrentDHKeys(c.ake.secretExponent, c.ake.ourPublicValue)
	c.keys.ourCounter++

	c.sentRevealSig = false

	return authStateNone{}, sigMsg, c.akeHasFinished()
}

func (s authStateAwaitingDHKey) receiveRevealSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingSig) receiveRevealSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateNone) receiveSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingRevealSig) receiveSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingDHKey) receiveSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	return s, nil, nil
}

func (s authStateAwaitingSig) receiveSigMessage(c *Conversation, msg []byte) (authState, messageWithHeader, error) {
	err := c.processSig(msg)

	if err != nil {
		return s, nil, err
	}

	//gy was stored when we receive DH-Key
	c.keys.setTheirCurrentDHPubKey(c.ake.theirPublicValue)

	return authStateNone{}, nil, c.akeHasFinished()
}

func (authStateNone) String() string              { return "AUTHSTATE_NONE" }
func (authStateAwaitingDHKey) String() string     { return "AUTHSTATE_AWAITING_DHKEY" }
func (authStateAwaitingRevealSig) String() string { return "AUTHSTATE_AWAITING_REVEALSIG" }
func (authStateAwaitingSig) String() string       { return "AUTHSTATE_AWAITING_SIG" }
