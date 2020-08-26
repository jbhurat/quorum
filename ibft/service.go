package ibft

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"
)

type IBFTService struct {
	eth        *eth.Ethereum
	ibftEngine consensus.Istanbul
	shouldMine bool
	nodeKey    *ecdsa.PrivateKey

	// Blockchain events
	eventMux         *event.TypeMux
	newMinedBlockSub *event.TypeMuxSubscription
	txPreChan        chan core.NewTxsEvent
	txPreSub         event.Subscription

	stop chan struct{}
}

func NewIBFTService(ctx *node.ServiceContext, eth *eth.Ethereum, eventMux *event.TypeMux) *IBFTService {
	backend := eth.Engine().(consensus.Istanbul)

	service := &IBFTService{
		eth:        eth,
		ibftEngine: backend,
		eventMux:   eventMux,
		stop:       make(chan struct{}),
		nodeKey:    ctx.NodeKey(),
		txPreChan:  make(chan core.NewTxsEvent, 4096),
	}

	service.txPreSub = eth.TxPool().SubscribeNewTxsEvent(service.txPreChan)

	return service
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

// Notify the minting loop that minting should occur, if it's not already been
// requested. Due to the use of a RingChannel, this function is idempotent if
// called multiple times before the minting occurs.
func (s *IBFTService) requestMinting() {
	s.shouldMine = true
}

func (s *IBFTService) eventLoop() {
	s.newMinedBlockSub = s.eventMux.Subscribe(core.NewMinedBlockEvent{})
	defer s.newMinedBlockSub.Unsubscribe()

	defer s.txPreSub.Unsubscribe()

	for {
		select {
		case muxEvent := <-s.newMinedBlockSub.Chan():
			switch muxEvent.Data.(type) {
			case core.NewMinedBlockEvent:
				time.Sleep(50 * time.Millisecond) // Simulates the time of a consensus execution.
				// TODO: Insert the block into blockchain.
				s.shouldMine = false
			}
		case <-s.txPreChan:
			s.requestMinting()
		case <-s.stop:
			return
		case <-s.txPreSub.Err():
			return
		}
	}
}

func (s *IBFTService) mintingLoop() {
	for {
		// FIXME This is very rudimentary and might skip processing transactions if they are in pending and no new transactions are coming in
		if s.shouldMine {
			if err := s.mintNewBlock(); err != nil {
				log.Error("Error minting new block", "err", err)
			}
		}
	}
}

func (s *IBFTService) mintNewBlock() error {
	work := s.createWork()
	transactions := s.getTransactions()

	committedTxes, publicReceipts, _, logs := work.commitTransactions(transactions, s.eth.BlockChain())
	txCount := len(committedTxes)

	if txCount == 0 {
		log.Info("Not minting a new block since there are no pending transactions")
		return nil
	}
	s.firePendingBlockEvents(logs)

	header := work.header

	// commit state root after all state transitions.
	header.Root = work.publicState.IntermediateRoot(s.eth.BlockChain().Config().IsEIP158(work.header.Number))

	// update block hash since it is now available, but was not when the
	// receipt/log of individual transactions were created:
	headerHash := header.Hash()
	for _, l := range logs {
		l.BlockHash = headerHash
	}

	block, err := s.ibftEngine.FinalizeAndAssemble(s.eth.BlockChain(), header, work.publicState, committedTxes, nil, publicReceipts)
	if err != nil {
		return err
	}
	sealHash := s.ibftEngine.SealHash(block.Header())
	block, err = s.sealBlock(block, sealHash)
	if err != nil {
		return err
	}

	s.eventMux.Post(core.NewMinedBlockEvent{Block: block})

	elapsed := time.Since(time.Unix(0, int64(header.Time)))
	log.Info("🔨  Mined block", "number", block.Number(), "hash", fmt.Sprintf("%x", block.Hash().Bytes()[:4]), "elapsed", elapsed)
	return nil
}

func (s *IBFTService) sealBlock(block *types.Block, sealHash common.Hash) (*types.Block, error) {
	header := block.Header()

	hashData := crypto.Keccak256(sealHash.Bytes())
	sig, err := crypto.Sign(hashData, s.nodeKey)
	if err != nil {
		return nil, err
	}
	if len(sig)%types.IstanbulExtraSeal != 0 {
		return nil, errors.New("invalid signature")
	}

	istanbulExtra, err := types.ExtractIstanbulExtra(header)
	if err != nil {
		return nil, err
	}

	istanbulExtra.Seal = sig
	payload, err := rlp.EncodeToBytes(&istanbulExtra)
	if err != nil {
		return nil, err
	}

	header.Extra = append(header.Extra[:types.IstanbulExtraVanity], payload...)
	return block.WithSeal(header), nil
}

// Current state information for building the next block
type work struct {
	config       *params.ChainConfig
	publicState  *state.StateDB
	privateState *state.StateDB
	Block        *types.Block
	header       *types.Header
}

func (s *IBFTService) createWork() *work {
	parent := s.eth.BlockChain().CurrentBlock()
	parentNumber := parent.Number()
	// FIXME For simplicity just setting Time to current time
	tstamp := time.Now().Unix()

	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     parentNumber.Add(parentNumber, common.Big1),
		GasLimit:   s.eth.CalcGasLimit(parent),
		GasUsed:    0,
		Time:       uint64(tstamp),
	}

	// Calling Prepare from Engine as there are bunch header updates like header.Extra which are required for the block verification
	err := s.ibftEngine.Prepare(s.eth.BlockChain(), header)
	if err != nil {
		// Handle Error
	}

	publicState, privateState, err := s.eth.BlockChain().StateAt(parent.Root())
	if err != nil {
		panic(fmt.Sprint("failed to get parent state: ", err))
	}

	return &work{
		config:       s.eth.BlockChain().Config(),
		publicState:  publicState,
		privateState: privateState,
		header:       header,
	}
}

func (s *IBFTService) getTransactions() *types.TransactionsByPriceAndNonce {
	// FIXME Ignoring duplicate transactions for now
	allAddrTxes, err := s.eth.TxPool().Pending()
	if err != nil { // TODO: handle
		panic(err)
	}
	signer := types.MakeSigner(s.eth.BlockChain().Config(), s.eth.BlockChain().CurrentBlock().Number())
	return types.NewTransactionsByPriceAndNonce(signer, allAddrTxes)
}

func (env *work) commitTransactions(txes *types.TransactionsByPriceAndNonce, bc *core.BlockChain) (types.Transactions, types.Receipts, types.Receipts, []*types.Log) {
	var allLogs []*types.Log
	var committedTxes types.Transactions
	var publicReceipts types.Receipts
	var privateReceipts types.Receipts

	gp := new(core.GasPool).AddGas(env.header.GasLimit)
	txCount := 0

	for {
		tx := txes.Peek()
		if tx == nil {
			break
		}

		env.publicState.Prepare(tx.Hash(), common.Hash{}, txCount)

		publicReceipt, privateReceipt, err := env.commitTransaction(tx, bc, gp)
		switch {
		case err != nil:
			log.Info("TX failed, will be removed", "hash", tx.Hash(), "err", err)
			txes.Pop() // skip rest of txes from this account
		default:
			txCount++
			committedTxes = append(committedTxes, tx)

			publicReceipts = append(publicReceipts, publicReceipt)
			allLogs = append(allLogs, publicReceipt.Logs...)

			if privateReceipt != nil {
				privateReceipts = append(privateReceipts, privateReceipt)
				allLogs = append(allLogs, privateReceipt.Logs...)
			}

			txes.Shift()
		}
	}

	return committedTxes, publicReceipts, privateReceipts, allLogs
}

func (env *work) commitTransaction(tx *types.Transaction, bc *core.BlockChain, gp *core.GasPool) (*types.Receipt, *types.Receipt, error) {
	publicSnapshot := env.publicState.Snapshot()
	privateSnapshot := env.privateState.Snapshot()

	var author *common.Address
	var vmConf vm.Config
	txnStart := time.Now()
	publicReceipt, privateReceipt, err := core.ApplyTransaction(env.config, bc, author, gp, env.publicState, env.privateState, env.header, tx, &env.header.GasUsed, vmConf)
	if err != nil {
		env.publicState.RevertToSnapshot(publicSnapshot)
		env.privateState.RevertToSnapshot(privateSnapshot)

		return nil, nil, err
	}
	log.EmitCheckpoint(log.TxCompleted, "tx", tx.Hash().Hex(), "time", time.Since(txnStart))

	return publicReceipt, privateReceipt, nil
}

// Sends-off events asynchronously.
// TODO We can disable this for POC
func (s *IBFTService) firePendingBlockEvents(logs []*types.Log) {
	// Copy logs before we mutate them, adding a block hash.
	copiedLogs := make([]*types.Log, len(logs))
	for i, l := range logs {
		copiedLogs[i] = new(types.Log)
		*copiedLogs[i] = *l
	}

	go func() {
		s.eventMux.Post(core.PendingLogsEvent{Logs: copiedLogs})
		s.eventMux.Post(core.PendingStateEvent{})
	}()
}
