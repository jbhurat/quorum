package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
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

func NewIBFTService(stack *node.Node, eth *eth.Ethereum) (*IBFTService, error) {
	eth.AcceptTxs()
	ibftEngine := eth.Engine().(consensus.Istanbul)
	service := &IBFTService{
		eth:        eth,
		ibftEngine: ibftEngine,
		minter:     minter.New(eth, ibftEngine, stack.GetNodeKey(), stack.EventMux()),
		consensus:  NewIBFTConsensus(eth.BlockChain(), ibftEngine),
		//consensus:  &FakeConsensus{},
		stop: make(chan struct{}),
	}

	stack.RegisterAPIs(service.APIs())
	stack.RegisterLifecycle(service)

	log.Info("Registered IBFTService")

	return service, nil
}

func (s *IBFTService) Protocols() []p2p.Protocol {
	return []p2p.Protocol{}
}

func (s *IBFTService) APIs() []rpc.API {
	return []rpc.API{
		{
			Namespace: "istanbul",
			Version:   "1.0",
			// TODO Fix Service
			Service: s,
			Public:  true,
		},
	}
}

func (s *IBFTService) Start() error {
	log.Info("IBFT - Starting IBFTService")
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
	log.Info("IBFT - Starting consensusLoop()")

	chainHeadCh := make(chan core.ChainHeadEvent, 10)
	chainHeadSub := s.eth.BlockChain().SubscribeChainHeadEvent(chainHeadCh)
	defer chainHeadSub.Unsubscribe()

	decisions := make(chan *types.Block)

	startExecuteTime := time.Now()
	consensusStop := s.consensus.Execute(s.minter, decisions)
	for {
		select {
		case block := <-decisions:
			if block != nil {
				log.Info("IBFT - Execute Time", "block", block.NumberU64(), "time", time.Since(startExecuteTime))
				startApplyTime := time.Now()
				err := s.minter.Apply(block) // this will trigger a ChainHeadEvent on chainHeadCh
				if err != nil {
					log.Error("IBFT - Error applying block", "block", block.Hash(), "number", block.NumberU64(), "err", err)
				} else {
					log.Info("IBFT - Apply Time", "block", block.NumberU64(), "time", time.Since(startApplyTime))
				}
			}
		case chainHeadEvent := <-chainHeadCh:
			log.Info("IBFT - New chain head", "block", chainHeadEvent.Block.Number())
			close(consensusStop)
			log.Info("IBFT - Closed consensusStop")

			h, ok := s.ibftEngine.(consensus.Handler)
			if !ok {
				panic("Couldn't cast ibftEngine to Handler")
			} // else panic or something
			log.Info("IBFT - Calling NewChainHead() to start new round")
			startedNewRoundCh, _ := h.NewChainHead()
			log.Info("IBFT - waiting for new round to start")
			<-startedNewRoundCh
			log.Info("IBFT - new round started")

			startExecuteTime = time.Now()
			consensusStop = s.consensus.Execute(s.minter, decisions)
		case <-s.stop:
			close(consensusStop)
			return
		}
	}
}
