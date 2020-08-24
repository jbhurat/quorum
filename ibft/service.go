package ibft

import (
	"time"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth *eth.Ethereum

	// Blockchain events
	eventMux         *event.TypeMux
	newMinedBlockSub *event.TypeMuxSubscription

	stop chan struct{}
}

func NewIBFTService(eth *eth.Ethereum, eventMux *event.TypeMux) *IBFTService {
	return &IBFTService{eth: eth, eventMux: eventMux, stop: make(chan struct{})}
}

func (s *IBFTService) Protocols() []p2p.Protocol {
	return []p2p.Protocol{}
}

func (s *IBFTService) APIs() []rpc.API {
	return []rpc.API{}
}

func (s *IBFTService) Start(server *p2p.Server) error {
	go s.eventLoop()
	go s.mintingLoop()
	return nil
}

func (s *IBFTService) Stop() error {
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
				time.Sleep(50 * time.Millisecond) // Simulates the time of a consensus execution.
				// TODO: Insert the block into blockchain.
			}
		case <-s.stop:
			return
		}
	}
}

func (s *IBFTService) mintingLoop() {
	// TODO: Mint new blocks and publish them as NewMinedBlockEvents
}
