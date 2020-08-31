package ibft

import (
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/ibft/minter"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth             *eth.Ethereum
	ibftEngine      consensus.Istanbul
	minter          *minter.Minter
	stop            chan struct{}
	miningInProcess bool
}

func NewIBFTService(ctx *node.ServiceContext, eth *eth.Ethereum) (*IBFTService, error) {
	ibftEngine := eth.Engine().(consensus.Istanbul)
	minter := minter.New(eth, ibftEngine, ctx.NodeKey(), ctx.EventMux)

	service := &IBFTService{
		eth:        eth,
		ibftEngine: ibftEngine,
		minter:     minter,
		stop:       make(chan struct{}),
	}
	return service, nil
}

func (s *IBFTService) Protocols() []p2p.Protocol {
	return []p2p.Protocol{}
}

func (s *IBFTService) APIs() []rpc.API {
	return []rpc.API{}
}

func (s *IBFTService) Start(server *p2p.Server) error {
	s.minter.Start()
	return nil
}

func (s *IBFTService) Stop() error {
	s.minter.Stop()
	close(s.stop)
	return nil
}
