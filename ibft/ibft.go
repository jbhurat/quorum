package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/log"
)

type Proposer interface {
	RequestBlock() (proposal <-chan *types.Block)
}

type StateMachine interface {
	Apply(block *types.Block) (err error)
}

type Consensus interface {
	Execute(proposer Proposer, decisions chan<- *types.Block) (stop chan<- struct{})
}

type FakeConsensus struct{}

func (c *FakeConsensus) Execute(proposer Proposer, decisions chan<- *types.Block) chan<- struct{} {
	proposal := proposer.RequestBlock()
	go func() {
		time.Sleep(20 * time.Millisecond)
		decisions <- <-proposal
	}()
	return make(chan<- struct{})
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

func (c *IBFTConsensus) Execute(proposer Proposer, decisions chan<- *types.Block) chan<- struct{} {
	log.Info("IBFT - ->IBFTConsensus#Execute")
	log.Info("IBFT - IBFTConsensus#Execute - waiting on <-proposer.RequestBlock()")
	proposal := <-proposer.RequestBlock()
	log.Info("IBFT - IBFTConsensus#Execute - got proposal block", "#", proposal.Number())
	stop := make(chan struct{})
	c.engine.Seal(c.chain, proposal, decisions, stop)
	log.Info("IBFT - IBFTConsensus#Execute->")
	return stop
}
