package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/ibft/minter"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth           *eth.Ethereum
	ibftEngine    consensus.Istanbul
	stop          chan struct{}
	minter        *minter.Minter
	consensus     Consensus
	chainHeadChan chan core.ChainHeadEvent
	chainHeadSub  event.Subscription
}

func NewIBFTService(ctx *node.ServiceContext, eth *eth.Ethereum) (*IBFTService, error) {
	ibftEngine := eth.Engine().(consensus.Istanbul)
	service := &IBFTService{
		eth:           eth,
		ibftEngine:    ibftEngine,
		minter:        minter.New(eth, ibftEngine, ctx.NodeKey(), ctx.EventMux),
		consensus:     NewIBFTConsensus(eth.BlockChain(), ibftEngine),
		stop:          make(chan struct{}),
		chainHeadChan: make(chan core.ChainHeadEvent, core.GetChainHeadChannleSize()),
	}
	service.chainHeadSub = eth.BlockChain().SubscribeChainHeadEvent(service.chainHeadChan)
	return service, nil
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
	go s.eventLoop()
	return nil
}

func (s *IBFTService) Stop() error {
	s.minter.Close()
	s.ibftEngine.Stop()
	close(s.stop)
	return nil
}

func (s *IBFTService) eventLoop() {
	defer s.chainHeadSub.Unsubscribe()
	for {
		select {
		case <-s.chainHeadChan:
			if handler, ok := s.ibftEngine.(consensus.Handler); ok {
				log.Trace("handling NewChainHead()")
				handler.NewChainHead()
			}

		// system stopped
		case <-s.chainHeadSub.Err():
			return

		}
	}
}

func (s *IBFTService) consensusLoop() {
	for {
		executeTime := time.Now()
		decision := s.consensus.Execute(s.minter)
		select {
		case block := <-decision:
			log.Info("Execute Time", "block", block.NumberU64(), "time", time.Since(executeTime))
			applyTime := time.Now()
			s.minter.Apply(block)
			log.Info("Apply Time", "block", block.NumberU64(), "time", time.Since(applyTime))
		case <-s.stop:
			return
		}
	}
}
