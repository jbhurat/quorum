package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/consensus"

	"github.com/ethereum/go-ethereum/core/types"
)

type Proposer interface {
	RequestBlock() (proposal <-chan *types.Block)
}

type StateMachine interface {
	Apply(block *types.Block) (err error)
}

type Consensus interface {
	Execute(proposer Proposer) (decision <-chan *types.Block)
}

type FakeConsensus struct{}

func (c *FakeConsensus) Execute(proposer Proposer) <-chan *types.Block {
	proposal := proposer.RequestBlock()
	decision := make(chan *types.Block)
	go func() {
		time.Sleep(20 * time.Millisecond)
		decision <- <-proposal
	}()
	return decision
}

type IBFTConsensus struct {
	engine consensus.Engine
	chain  consensus.ChainReader
}

func NewIBFTConsensus(chain consensus.ChainReader, engine consensus.Engine) *IBFTConsensus {
	return &IBFTConsensus{
		engine: engine,
		chain:  chain,
	}
}

func (c *IBFTConsensus) Execute(proposer Proposer) <-chan *types.Block {
	proposal := <-proposer.RequestBlock()
	decision := make(chan *types.Block)
	stop := make(chan struct{})
	c.engine.Seal(c.chain, proposal, decision, stop)
	return decision
}
