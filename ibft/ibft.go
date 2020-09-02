package ibft

import (
	"github.com/ethereum/go-ethereum/core/types"
	"math/big"
	"time"
)

type Proposer interface {
	RequestBlock() (proposal <-chan *types.Block)
}

type StateMachine interface {
	Apply(block *types.Block) (err error)
}

type Consensus interface {
	Execute(sequence *big.Int, proposer Proposer) (decision <-chan *types.Block)
}

type FakeConsensus struct {}

func (c *FakeConsensus) Execute(sequence *big.Int, proposer Proposer) <-chan *types.Block {
	proposal := proposer.RequestBlock()
	decision := make(chan *types.Block)
	go func() {
		time.Sleep(20 * time.Millisecond)
		decision <- <-proposal
	}()
	return decision
}
