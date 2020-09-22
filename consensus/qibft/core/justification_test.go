package core

import (
	"math/big"
	"math/rand"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus/istanbul"
	"github.com/ethereum/go-ethereum/consensus/istanbul/validator"
)

// Tests combinations of justifications that evaluate to true.
func TestJustifyTrue(t *testing.T) {
	for quorumSize := 3; quorumSize <= 10; quorumSize++ {
		// All ROUND-CHANGE messages have pr/pb nil
		testParameterizedCase(t, quorumSize, quorumSize, 0, 0, 0, 0, 0, true)

		// Some ROUND-CHANGE message has pr/pb not nil
		for equal := 1; equal <= quorumSize; equal++ {
			for less := 0; less <= quorumSize-equal; less++ {
				nil := quorumSize - equal - less
				testParameterizedCase(t, quorumSize, nil, equal, less, 0, quorumSize, 0, true)
			}
		}
	}
}

// Tests combinations of justifications that evaluate to false.
func TestJustifyFalse(t *testing.T) {
	for quorumSize := 3; quorumSize <= 10; quorumSize++ {
		// Total ROUND-CHANGE messages less than quorumSize
		// all have pr/pb nil
		for totalRoundChange := 0; totalRoundChange < quorumSize; totalRoundChange++ {
			testParameterizedCase(t, quorumSize, totalRoundChange, 0, 0, 0, 0, 0, false)
		}
		// some has pr/pb not nil
		for totalRoundChange := 0; totalRoundChange < quorumSize; totalRoundChange++ {
			for equal := 1; equal <= totalRoundChange; equal++ {
				for less := 0; less <= totalRoundChange-equal; less++ {
					nil := totalRoundChange - equal - less
					testParameterizedCase(t, quorumSize, nil, equal, less, 0, quorumSize, 0, false)
				}
			}
		}

		// Total ROUND-CHANGE messages equal to quorumSize
		for equal := 1; equal <= quorumSize; equal++ {
			for less := 0; less <= quorumSize-equal; less++ {
				nil := quorumSize - equal - less

				// Total PREPARE messages less than quorumSize
				for total := 0; total < quorumSize; total++ {
					testParameterizedCase(t, quorumSize, nil, equal, less, 0, total, quorumSize-total, false)
				}

				// Total PREPARE messages equal to quorumSize and some PREPARE message has round different than others
				for different := 1; different <= quorumSize; different++ {
					testParameterizedCase(t, quorumSize, nil, equal, less, 0, quorumSize-different, different, false)
				}
			}
		}
	}
}

func testParameterizedCase(
	t *testing.T,
	quorumSize int,
	rcForNil int,
	rcEqualToTargetRound int,
	rcLowerThanTargetRound int,
	rcHigherThanTargetRound int,
	preparesForTargetRound int,
	preparesNotForTargetRound int,
	isJustified bool) {

	validatorSet := validator.NewSet(generateValidators(quorumSize), istanbul.RoundRobin)
	block := makeBlock(1)
	var round int64 = 10
	var targetPreparedRound int64 = 5

	rng := rand.New(rand.NewSource(time.Now().UTC().UnixNano()))

	if rcForNil+rcEqualToTargetRound+rcLowerThanTargetRound+rcHigherThanTargetRound > quorumSize {
		t.Errorf("rcForNil (%v) + rcEqualToTargetRound (%v) + rcLowerThanTargetRound (%v) + rcHigherThanTargetRound (%v) > quorumSize (%v)",
			rcForNil, rcEqualToTargetRound, rcLowerThanTargetRound, rcHigherThanTargetRound, quorumSize)
	}

	if preparesForTargetRound+preparesNotForTargetRound > quorumSize {
		t.Errorf("preparesForTargetRound (%v) + preparesNotForTargetRound (%v) > quorumSize (%v)", preparesForTargetRound, preparesNotForTargetRound, quorumSize)
	}

	// ROUND-CHANGE messages
	roundChangeMessages := newMessageSet(validatorSet)
	for index, validator := range validatorSet.List() {
		var m *message
		if index < rcForNil {
			m = createRoundChangeMessage(validator.Address(), round, 0, NilBlock())
		} else if index >= rcForNil && index < rcForNil+rcEqualToTargetRound {
			m = createRoundChangeMessage(validator.Address(), round, targetPreparedRound, block)
		} else if index >= rcForNil+rcEqualToTargetRound && index < rcForNil+rcEqualToTargetRound+rcLowerThanTargetRound {
			m = createRoundChangeMessage(validator.Address(), round, int64(rng.Intn(int(targetPreparedRound)-1)+1), block)
		} else if index >= rcForNil+rcEqualToTargetRound+rcLowerThanTargetRound && index < rcForNil+rcEqualToTargetRound+rcLowerThanTargetRound+rcHigherThanTargetRound {
			m = createRoundChangeMessage(validator.Address(), round, int64(rng.Intn(int(targetPreparedRound))+int(targetPreparedRound)+1), block)
		} else {
			break
		}
		roundChangeMessages.Add(m)
	}

	// PREPARE messages
	prepareMessages := newMessageSet(validatorSet)
	for index, validator := range validatorSet.List() {
		var m *message
		if index < preparesForTargetRound {
			m = createPrepareMessage(validator.Address(), targetPreparedRound, block)
		} else if index >= preparesForTargetRound && index < preparesForTargetRound+preparesNotForTargetRound {
			notTargetPreparedRound := targetPreparedRound
			for notTargetPreparedRound == targetPreparedRound {
				notTargetPreparedRound = rng.Int63()
			}
			m = createPrepareMessage(validator.Address(), notTargetPreparedRound, block)
		} else {
			break
		}
		prepareMessages.Add(m)
	}

	if justify(block, roundChangeMessages, prepareMessages, quorumSize) != isJustified {
		t.Errorf("quorumSize = %v, rcForNil = %v, rcEqualToTargetRound = %v, rcLowerThanTargetRound = %v, rcHigherThanTargetRound = %v, preparesForTargetRound = %v, preparesNotForTargetRound = %v (Expected: %v, Actual: %v)",
			quorumSize, rcForNil, rcEqualToTargetRound, rcLowerThanTargetRound, rcHigherThanTargetRound, preparesForTargetRound, preparesNotForTargetRound, isJustified, !isJustified)

	}
}

func createRoundChangeMessage(from common.Address, round int64, preparedRound int64, preparedBlock istanbul.Proposal) *message {
	m, _ := Encode(&RoundChangeMessage{
		View:          &View{big.NewInt(round), big.NewInt(1)},
		PreparedRound: big.NewInt(preparedRound),
		PreparedBlock: preparedBlock,
	})

	return &message{
		Code:    msgRoundChange,
		Msg:     m,
		Address: from,
	}
}

func createPrepareMessage(from common.Address, round int64, preparedBlock istanbul.Proposal) *message {
	m, _ := Encode(&Subject{
		View:   &View{big.NewInt(round), big.NewInt(1)},
		Digest: preparedBlock,
	})

	return &message{
		Code:    msgPrepare,
		Msg:     m,
		Address: from,
	}
}
