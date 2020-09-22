// Copyright 2017 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

package core

import (
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/consensus/istanbul"
	"github.com/ethereum/go-ethereum/rlp"
)

func (c *core) sendPreprepare(request *Request) {
	logger := c.logger.New("state", c.state)

	// If I'm the proposer and I have the same sequence with the proposal
	if c.current.Sequence().Cmp(request.Proposal.Number()) == 0 && c.IsProposer() {
		curView := c.currentView()
		preprepare, err := Encode(&Preprepare{
			View:     curView,
			Proposal: request.Proposal,
		})
		if err != nil {
			logger.Error("Failed to encode", "view", curView)
			return
		}
		// Encode RoundChange messages that piggyback Preprepare message

		var piggybackMsgPayload []byte
		rcMsgs := request.RCMessages
		prepareMsgs := request.PrepareMessages
		if rcMsgs == nil {
			rcMsgs = newMessageSet(c.valSet)
		}
		if prepareMsgs == nil {
			prepareMsgs = newMessageSet(c.valSet)
		}
		piggybackMsgPayload, err = Encode(&PiggybackMessages{RCMessages: rcMsgs, PreparedMessages: prepareMsgs})
		if err != nil {
			logger.Error("Failed to encode Piggyback messages accompanying Preprepare", "err", err)
			return
		}

		c.broadcast(&message{
			Code:          msgPreprepare,
			Msg:           preprepare,
			PiggybackMsgs: piggybackMsgPayload,
		})
		// Set the preprepareSent to the current round
		c.current.preprepareSent = curView.Round
	}
}

func (c *core) handlePreprepare(msg *message, src istanbul.Validator) error {
	logger := c.logger.New("from", src, "state", c.state)

	// Decode PRE-PREPARE
	var preprepare *Preprepare
	err := msg.Decode(&preprepare)
	if err != nil {
		logger.Debug("Failed to decode preprepare message", "err", err)
		return errFailedDecodePreprepare
	}

	// Decode messages that piggyback Preprepare message
	var piggyBackMsgs *PiggybackMessages
	if msg.PiggybackMsgs != nil && len(msg.PiggybackMsgs) > 0 {
		if err := rlp.DecodeBytes(msg.PiggybackMsgs, &piggyBackMsgs); err != nil {
			logger.Error("Failed to decode messages that piggyback Preprepare messages", "err", err)
			return errFailedDecodePiggybackMsgs
		}
	}

	// Ensure we have the same view with the PRE-PREPARE message
	// If it is old message, see if we need to broadcast COMMIT
	if err := c.checkMessage(msgPreprepare, preprepare.View); err != nil {
		if err == errOldMessage {
			// Get validator set for the given proposal
			valSet := c.backend.ParentValidators(preprepare.Proposal).Copy()
			previousProposer := c.backend.GetProposer(preprepare.Proposal.Number().Uint64() - 1)
			valSet.CalcProposer(previousProposer, preprepare.View.Round.Uint64())
			// Broadcast COMMIT if it is an existing block
			// 1. The proposer needs to be a proposer matches the given (Sequence + Round)
			// 2. The given block must exist
			if valSet.IsProposer(src.Address()) && c.backend.HasPropsal(preprepare.Proposal.Hash(), preprepare.Proposal.Number()) {
				c.sendCommitForOldBlock(preprepare.View, preprepare.Proposal)
				return nil
			}
		}
		return err
	}

	// Check if the message comes from current proposer
	if !c.valSet.IsProposer(src.Address()) {
		logger.Warn("Ignore preprepare messages from non-proposer")
		return errNotFromProposer
	}

	if preprepare.View.Round.Uint64() > 0 && !justify(preprepare.Proposal, piggyBackMsgs.RCMessages, piggyBackMsgs.PreparedMessages, c.QuorumSize()) {
		logger.Error("Unable to justify PRE-PREPARE message")
		return errInvalidPreparedBlock
	}

	// Verify the proposal we received
	if duration, err := c.backend.Verify(preprepare.Proposal); err != nil {
		// if it's a future block, we will handle it again after the duration
		if err == consensus.ErrFutureBlock {
			logger.Info("Proposed block will be handled in the future", "err", err, "duration", duration)
			c.stopFuturePreprepareTimer()
			c.futurePreprepareTimer = time.AfterFunc(duration, func() {
				c.sendEvent(backlogEvent{
					src: src,
					msg: msg,
				})
			})
		}
		return err
	}

	// Here is about to accept the PRE-PREPARE
	if c.state == StateAcceptRequest {
		c.newRoundChangeTimer()
		c.acceptPreprepare(preprepare)
		c.setState(StatePreprepared)
		c.sendPrepare()
	}

	return nil
}

func (c *core) acceptPreprepare(preprepare *Preprepare) {
	c.consensusTimestamp = time.Now()
	c.current.SetPreprepare(preprepare)
}
