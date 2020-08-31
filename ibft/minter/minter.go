package minter

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
)

type Minter struct {
	eth        *eth.Ethereum
	ibftEngine consensus.Istanbul
	txPreChan  chan core.NewTxsEvent
	txPreSub   event.Subscription
	nodeKey    *ecdsa.PrivateKey
	requests   chan struct{}
	eventMux   *event.TypeMux
	stop       chan struct{}
}

func New(eth *eth.Ethereum, ibftEngine consensus.Istanbul, nodeKey *ecdsa.PrivateKey, eventMux *event.TypeMux) *Minter {
	minter := &Minter{
		eth:        eth,
		ibftEngine: ibftEngine,
		txPreChan:  make(chan core.NewTxsEvent, 4096),
		nodeKey:    nodeKey,
		requests:   make(chan struct{}, 1),
		eventMux:   eventMux,
		stop:       make(chan struct{})}

	return minter
}

// Current state information for building the next block
type work struct {
	transactions *types.TransactionsByPriceAndNonce
	publicState  *state.StateDB
	privateState *state.StateDB
	header       *types.Header
}

func (m *Minter) createWork() *work {
	parent := m.eth.BlockChain().CurrentBlock()
	parentNumber := parent.Number()
	// FIXME For simplicity just setting Time to current time
	tstamp := time.Now().Unix()

	header := &types.Header{
		ParentHash: parent.Hash(),
		Number:     parentNumber.Add(parentNumber, common.Big1),
		GasLimit:   m.eth.CalcGasLimit(parent),
		GasUsed:    0,
		Time:       uint64(tstamp),
	}

	// Calling Prepare from Engine as there are bunch header updates like header.Extra which are required for the block verification
	err := m.ibftEngine.Prepare(m.eth.BlockChain(), header)
	if err != nil {
		// Handle Error
	}

	publicState, privateState, err := m.eth.BlockChain().StateAt(parent.Root())
	if err != nil {
		panic(fmt.Sprint("failed to get parent state: ", err))
	}

	return &work{
		transactions: m.getTransactions(),
		publicState:  publicState,
		privateState: privateState,
		header:       header,
	}
}

func (m *Minter) getTransactions() *types.TransactionsByPriceAndNonce {
	// FIXME Ignoring duplicate transactions for now
	allAddrTxs, err := m.eth.TxPool().Pending()
	if err != nil { // TODO: handle
		panic(err)
	}
	signer := types.MakeSigner(m.eth.BlockChain().Config(), m.eth.BlockChain().CurrentBlock().Number())
	return types.NewTransactionsByPriceAndNonce(signer, allAddrTxs)
}

func (m *Minter) Start() {
	go m.mintingLoop()
}

func (m *Minter) Stop() {
	close(m.stop)
}

func (m *Minter) RequestBlock() {
	m.requests <- struct{}{}
}

func (m *Minter) mintingLoop() {
	m.txPreSub = m.eth.TxPool().SubscribeNewTxsEvent(m.txPreChan)
	defer m.txPreSub.Unsubscribe()

	// Temporary until we enable the consensus logic
	if !m.eth.AccountManager().Config().InsecureUnlockAllowed {
		return
	}

	for {
		select {
		case <-m.requests:
			err := m.createBlock()
			if err != nil {
				panic(err) // FIXME: proper error handling
			}
		case <-m.stop:
			return
		}
	}
}

func (m *Minter) createBlock() error {
	<-m.txPreChan // wait until we have pending transactions

	work := m.createWork()

	committedTxs, publicReceipts, privateReceipts, logs := m.commitTransactions(work)

	// commit state root after all state transitions.
	work.header.Root = work.publicState.IntermediateRoot(m.eth.BlockChain().Config().IsEIP158(work.header.Number))

	// update block hash since it is now available, but was not when the
	// receipt/log of individual transactions were created:
	headerHash := work.header.Hash()
	for _, l := range logs {
		l.BlockHash = headerHash
	}

	block, err := m.ibftEngine.FinalizeAndAssemble(m.eth.BlockChain(), work.header, work.publicState, committedTxs, nil, publicReceipts)
	if err != nil {
		return err
	}
	sealHash := m.ibftEngine.SealHash(block.Header())
	block, err = m.sealBlock(block, sealHash)
	if err != nil {
		return err
	}
	m.Seal()
	m.InsertBlockAndUpdateState(block, publicReceipts, privateReceipts, work.publicState, work.privateState)

	m.eventMux.Post(core.NewMinedBlockEvent{Block: block})

	// TODO State has been ignored in InsertBlockAndUpdateState, this will change based on State type
	var events []interface{}
	events = append(events, core.ChainEvent{Block: block, Hash: block.Hash(), Logs: logs})
	events = append(events, core.ChainHeadEvent{Block: block})
	m.eth.BlockChain().PostChainEvents(events, logs)

	elapsed := time.Since(time.Unix(0, int64(work.header.Time)))
	log.Info("🔨  Mined block", "number", block.Number(), "hash", fmt.Sprintf("%x", block.Hash().Bytes()[:4]), "elapsed", elapsed)
	return nil
}

func (m *Minter) Seal() {
	// Sleeping for 50 millisecond to simulate consensus
	time.Sleep(50 * time.Millisecond)
}

func (m *Minter) InsertBlockAndUpdateState(block *types.Block, publicReceipts types.Receipts, privateReceipts types.Receipts, publicState *state.StateDB, privateState *state.StateDB) {
	hash := block.Hash()
	// Different block could share same sealhash, deep copy here to prevent write-write conflict.
	var (
		pubReceipts = make([]*types.Receipt, len(publicReceipts))
		prvReceipts = make([]*types.Receipt, len(privateReceipts))
		logs        []*types.Log
	)
	offset := len(publicReceipts)
	for i, receipt := range publicReceipts {
		// add block location fields
		receipt.BlockHash = hash
		receipt.BlockNumber = block.Number()
		receipt.TransactionIndex = uint(i)

		pubReceipts[i] = new(types.Receipt)
		*pubReceipts[i] = *receipt
		// Update the block hash in all logs since it is now available and not when the
		// receipt/log of individual transactions were created.
		for _, log := range receipt.Logs {
			log.BlockHash = hash
		}
		logs = append(logs, receipt.Logs...)
	}

	for i, receipt := range privateReceipts {
		// add block location fields
		receipt.BlockHash = hash
		receipt.BlockNumber = block.Number()
		receipt.TransactionIndex = uint(i + offset)

		prvReceipts[i] = new(types.Receipt)
		*prvReceipts[i] = *receipt
		// Update the block hash in all logs since it is now available and not when the
		// receipt/log of individual transactions were created.
		for _, log := range receipt.Logs {
			log.BlockHash = hash
		}
		logs = append(logs, receipt.Logs...)
	}

	allReceipts := mergeReceipts(pubReceipts, prvReceipts)

	// Commit block and state to database.
	// TODO Ignoring the status for now
	_, err := m.eth.BlockChain().WriteBlockWithState(block, allReceipts, publicState, privateState)
	if err != nil {
		log.Error("Failed writing block to chain", "err", err)
		return
	}
	if err := rawdb.WritePrivateBlockBloom(m.eth.ChainDb(), block.NumberU64(), privateReceipts); err != nil {
		log.Error("Failed writing private block bloom", "err", err)
		return
	}
}

func (m *Minter) commitTransactions(work *work) (
	types.Transactions, types.Receipts, types.Receipts, []*types.Log) {
	var allLogs []*types.Log
	var committedTxes types.Transactions
	var publicReceipts types.Receipts
	var privateReceipts types.Receipts

	gp := new(core.GasPool).AddGas(work.header.GasLimit)
	txCount := 0

	for {
		tx := work.transactions.Peek()
		if tx == nil {
			break
		}

		work.publicState.Prepare(tx.Hash(), common.Hash{}, txCount)

		publicReceipt, privateReceipt, err := m.commitTransaction(tx, work, gp)
		switch {
		case err != nil:
			log.Info("TX failed, will be removed", "hash", tx.Hash(), "err", err)
			work.transactions.Pop() // skip rest of txes from this account
		default:
			txCount++
			committedTxes = append(committedTxes, tx)

			publicReceipts = append(publicReceipts, publicReceipt)
			allLogs = append(allLogs, publicReceipt.Logs...)

			if privateReceipt != nil {
				privateReceipts = append(privateReceipts, privateReceipt)
				allLogs = append(allLogs, privateReceipt.Logs...)
			}

			work.transactions.Shift()
		}
	}

	return committedTxes, publicReceipts, privateReceipts, allLogs
}

func (m *Minter) commitTransaction(
	tx *types.Transaction,
	work *work,
	gp *core.GasPool) (*types.Receipt, *types.Receipt, error) {
	publicSnapshot := work.publicState.Snapshot()
	privateSnapshot := work.privateState.Snapshot()

	var author *common.Address
	var vmConf vm.Config
	txnStart := time.Now()
	publicReceipt, privateReceipt, err := core.ApplyTransaction(
		m.eth.BlockChain().Config(), m.eth.BlockChain(), author, gp, work.publicState, work.privateState, work.header, tx, &work.header.GasUsed, vmConf)
	if err != nil {
		work.publicState.RevertToSnapshot(publicSnapshot)
		work.privateState.RevertToSnapshot(privateSnapshot)

		return nil, nil, err
	}
	log.EmitCheckpoint(log.TxCompleted, "tx", tx.Hash().Hex(), "time", time.Since(txnStart))

	return publicReceipt, privateReceipt, nil
}

func (m *Minter) sealBlock(block *types.Block, sealHash common.Hash) (*types.Block, error) {
	header := block.Header()

	hashData := crypto.Keccak256(sealHash.Bytes())
	sig, err := crypto.Sign(hashData, m.nodeKey)
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

// Given a slice of public receipts and an overlapping (smaller) slice of
// private receipts, return a new slice where the default for each location is
// the public receipt but we take the private receipt in each place we have
// one.
func mergeReceipts(pub, priv types.Receipts) types.Receipts {
	m := make(map[common.Hash]*types.Receipt)
	for _, receipt := range pub {
		m[receipt.TxHash] = receipt
	}
	for _, receipt := range priv {
		m[receipt.TxHash] = receipt
	}

	ret := make(types.Receipts, 0, len(pub))
	for _, pubReceipt := range pub {
		ret = append(ret, m[pubReceipt.TxHash])
	}

	return ret
}
