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
	"math/big"
	"reflect"

	"github.com/ethereum/go-ethereum/consensus/istanbul"
)

func (c *core) sendCommit() {
	sub := c.current.Subject()
	c.broadcastCommit(sub)
}

func (c *core) sendCommitForOldBlock(view *View, digest istanbul.Proposal) {
	sub := &Subject{
		View:   view,
		Digest: digest,
	}
	c.broadcastCommit(sub)
}

func (c *core) broadcastCommit(sub *Subject) {
	logger := c.logger.New("state", c.state)

	encodedSubject, err := Encode(sub)
	if err != nil {
		logger.Error("Failed to encode", "subject", sub)
		return
	}
	c.broadcast(&message{
		Code: msgCommit,
		Msg:  encodedSubject,
	})
	c.current.msgSent[msgCommit] = true
}

func (c *core) handleCommit(msg *message, src istanbul.Validator) error {
	// Decode COMMIT message
	var commit *Subject
	err := msg.Decode(&commit)
	if err != nil {
		return errFailedDecodeCommit
	}

	if err := c.checkMessage(msgCommit, commit.View); err != nil {
		return err
	}

	c.acceptCommit(msg, src)

	// Commit the proposal once we have enough COMMIT messages and we are not in the Committed state.
	//
	// If we already have a proposal, we may have chance to speed up the consensus process
	// by committing the proposal without PREPARE messages.
	if c.current.Commits.Size() >= c.QuorumSize() && c.VerifyCommitMessages() {
		if c.current.Proposal() == nil || c.current.Proposal().Number().Uint64() != commit.View.Sequence.Uint64() {
			c.current.setProposal(commit.Digest)
		}

		// If Commit is not set or if it from the previous block
		if !(commit.View.Sequence.Uint64() < c.current.sequence.Uint64()) && !c.committedBlock[commit.View.Sequence.Uint64()] {
			c.updateCommittedBlockMap(commit.View.Sequence)
			c.commit()
		} else {
			c.logger.Trace("Not committing block as it is either from previous sequence or already committed for this sequence")
		}
	}

	return nil
}

// VerifyCommitMessages returns true if all the commit messages are identical
func (c *core) VerifyCommitMessages() bool {
	c.logger.Trace("VerifyCommitMessages()", "sequence", c.current.sequence.Uint64(), "round", c.current.round.Uint64())
	if c.current.Commits.Size() <= 1 {
		return true
	}
	return c.current.verifyPrepareOrCommitMessages(c.current.Commits)
}

// verifyCommit verifies if the received COMMIT message is equivalent to our subject
func (c *core) verifyCommit(commit *Subject, src istanbul.Validator) error {
	logger := c.logger.New("from", src, "state", c.state)

	sub := c.current.Subject()
	if !reflect.DeepEqual(commit.View, sub.View) || commit.Digest.Hash().Hex() != sub.Digest.Hash().Hex() {
		logger.Warn("Inconsistent subjects between commit and proposal", "expected", sub, "got", commit)
		return errInconsistentSubject
	}

	return nil
}

func (c *core) acceptCommit(msg *message, src istanbul.Validator) error {
	logger := c.logger.New("from", src, "state", c.state)

	// Add the COMMIT message to current round state
	if err := c.current.Commits.Add(msg); err != nil {
		logger.Error("Failed to record commit message", "msg", msg, "err", err)
		return err
	}

	return nil
}

func (c *core) updateCommittedBlockMap(sequence *big.Int) {
	c.logger.Trace("UpdateCommitedBlockMap", "sequence", sequence.Uint64())
	for k := range c.committedBlock {
		delete(c.committedBlock, k)
	}
	c.committedBlock[sequence.Uint64()] = true
}
