package ibft

import (
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ibft/minter"
	"github.com/ethereum/go-ethereum/log"
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
		consensus:  NewIBFTConsensus(eth.BlockChain(), ibftEngine),
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
	err := s.ibftEngine.Start(
		s.eth.BlockChain(),
		s.eth.BlockChain().CurrentBlock,
		s.eth.BlockChain().HasBadBlock)
	if err != nil {
		log.Crit("IBFT - Error starting consensus engine: %v", err)
		panic(err)
	}
	go s.consensusLoop()
	return nil
}

func (s *IBFTService) Stop() error {
	s.minter.Close()
	s.ibftEngine.Stop()
	close(s.stop)
	return nil
}

func (s *IBFTService) consensusLoop() {
	// Temporary until we enable the consensus logic
	if !s.eth.AccountManager().Config().InsecureUnlockAllowed {
		return
	}
	sequence := big.NewInt(0).Set(s.eth.BlockChain().CurrentHeader().Number)
	for {
		sequence.Add(sequence, big.NewInt(1))
		executeTime := time.Now()
		decision := s.consensus.Execute(s.minter)
		select {
		case block := <-decision:
			log.Info("Execute Time", "block", sequence.Uint64(), "time", time.Since(executeTime))
			applyTime := time.Now()
			s.minter.Apply(block)
			log.Info("Apply Time", "block", block.NumberU64(), "time", time.Since(applyTime))
		case <-s.stop:
			return
		}
	}
}
