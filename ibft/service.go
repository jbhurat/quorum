package ibft

import (
	"math/big"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ibft/minter"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth        *eth.Ethereum
	ibftEngine consensus.Istanbul
	stop       chan struct{}
	minter     *minter.Minter
	consensus  Consensus
}

func NewIBFTService(ctx *node.ServiceContext, eth *eth.Ethereum) (*IBFTService, error) {
	ibftEngine := eth.Engine().(consensus.Istanbul)
	return &IBFTService{
		eth:        eth,
		ibftEngine: ibftEngine,
		minter:     minter.New(eth, ibftEngine, ctx.NodeKey(), ctx.EventMux),
		stop:       make(chan struct{}),
	}, nil
}

func (s *IBFTService) Protocols() []p2p.Protocol {
	return []p2p.Protocol{}
}

func (s *IBFTService) APIs() []rpc.API {
	return []rpc.API{}
}

func (s *IBFTService) Start(server *p2p.Server) error {
	go s.consensusLoop()
	return nil
}

func (s *IBFTService) Stop() error {
	s.minter.Close()
	close(s.stop)
	return nil
}

func (s *IBFTService) consensusLoop() {
	sequence := big.NewInt(0).Set(s.eth.BlockChain().CurrentHeader().Number)
	for {
		sequence.Add(sequence, big.NewInt(1))
		decision := s.consensus.Execute(sequence, s.minter)
		select {
		case block := <-decision:
			s.minter.Apply(block)
		case <-s.stop:
			return
		}
	}
}
