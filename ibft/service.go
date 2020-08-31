package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/ibft/minter"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth        *eth.Ethereum
	ibftEngine consensus.Istanbul
	minter     *minter.Minter
	stop       chan struct{}
	// Blockchain events
	eventMux         *event.TypeMux
	newMinedBlockSub *event.TypeMuxSubscription
}

func NewIBFTService(ctx *node.ServiceContext, eth *eth.Ethereum, eventMux *event.TypeMux) *IBFTService {
	ibftEngine := eth.Engine().(consensus.Istanbul)
	minter := minter.New(eth, ibftEngine, ctx.NodeKey(), eventMux)

	return &IBFTService{
		eth:        eth,
		ibftEngine: ibftEngine,
		eventMux:   eventMux,
		minter:     minter,
		stop:       make(chan struct{}),
	}
}

func (s *IBFTService) Protocols() []p2p.Protocol {
	return []p2p.Protocol{}
}

func (s *IBFTService) APIs() []rpc.API {
	return []rpc.API{}
}

func (s *IBFTService) Start(server *p2p.Server) error {
	s.minter.Start()
	go s.eventLoop()
	return nil
}

func (s *IBFTService) Stop() error {
	s.minter.Stop()
	close(s.stop)
	return nil
}

func (s *IBFTService) eventLoop() {
	s.newMinedBlockSub = s.eventMux.Subscribe(core.NewMinedBlockEvent{})
	defer s.newMinedBlockSub.Unsubscribe()

	for {
		select {
		case muxEvent := <-s.newMinedBlockSub.Chan():
			switch muxEvent.Data.(type) {
			case core.NewMinedBlockEvent:
				// FIXME: Change this to async code
				time.Sleep(50 * time.Millisecond) // Simulates the time of a consensus execution.
				// TODO: Insert the block into blockchain.
			}
		case <-s.stop:
			return
		}
	}
}
