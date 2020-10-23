package minter

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/consensus"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/state"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rlp"
)

type Minter struct {
	mu         sync.Mutex
	eth        *eth.Ethereum
	ibftEngine consensus.Istanbul
	nodeKey    *ecdsa.PrivateKey
	request    chan chan<- *types.Block
	eventMux   *event.TypeMux
	stop       chan struct{}
	work       *work
}

func New(eth *eth.Ethereum, ibftEngine consensus.Istanbul, nodeKey *ecdsa.PrivateKey, eventMux *event.TypeMux) *Minter {
	minter := &Minter{
		eth:        eth,
		ibftEngine: ibftEngine,
		nodeKey:    nodeKey,
		request:    make(chan chan<- *types.Block),
		eventMux:   eventMux,
		stop:       make(chan struct{})}

	return minter
}

// Current state information for building the next block
type work struct {
	transactions    *types.TransactionsByPriceAndNonce
	publicState     *state.StateDB
	privateState    *state.StateDB
	header          *types.Header
	publicReceipts  types.Receipts
	privateReceipts types.Receipts
	logs            []*types.Log
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

func (m *Minter) Close() {
	close(m.stop)
}

func (m *Minter) RequestBlock() <-chan *types.Block {
	m.mu.Lock()
	proposal := make(chan *types.Block)
	go func() {
		m.eth.TxPool().WaitForPendingTxns()
		cbTime := time.Now()
		block, err := m.createBlock()
		if err != nil {
			panic(err) // FIXME: proper error handling
		}
		proposal <- block
		log.Info("createBlock time", "block", block.NumberU64(), "time", time.Since(cbTime))
	}()
	return proposal
}

func (m *Minter) createBlock() (*types.Block, error) {
	work := m.createWork()
	m.work = work

	var committedTxs types.Transactions
	committedTxs, work.publicReceipts, work.privateReceipts, work.logs = m.commitTransactions(work)
	log.Info("Committed", "transactions", committedTxs.Len())

	// update block hash since it is now available, but was not when the
	// receipt/log of individual transactions were created:
	headerHash := work.header.Hash()
	for _, l := range work.logs {
		l.BlockHash = headerHash
	}

	block, err := m.ibftEngine.FinalizeAndAssemble(m.eth.BlockChain(), work.header, work.publicState, committedTxs, nil, work.publicReceipts)
	if err != nil {
		return nil, err
	}
	m.ibftEngine.SealHash(block.Header())

	elapsed := time.Since(time.Unix(0, int64(work.header.Time)))
	log.Info("🔨  Mined block", "number", block.Number(), "hash", fmt.Sprintf("%x", block.Hash().Bytes()[:4]), "elapsed", elapsed)
	return block, nil
}

func (m *Minter) Apply(block *types.Block) error {
	defer m.mu.Unlock()

	_, err := m.eth.BlockChain().InsertChain(types.Blocks{block})
	if err != nil {
		log.Error("block insertion failed", "block", block.Hash(), "number", block.NumberU64(), "err", err)
		return err
	}

	m.eventMux.Post(core.NewMinedBlockEvent{Block: block})

	return nil
}

func (m *Minter) insertBlockAndUpdateState(block *types.Block) {
	hash := block.Hash()
	// Different block could share same sealhash, deep copy here to prevent write-write conflict.
	var (
		w           = m.work
		pubReceipts = make([]*types.Receipt, len(w.publicReceipts))
		prvReceipts = make([]*types.Receipt, len(w.privateReceipts))
		logs        []*types.Log
	)
	offset := len(w.publicReceipts)
	for i, receipt := range w.publicReceipts {
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

	for i, receipt := range w.privateReceipts {
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
	_, err := m.eth.BlockChain().WriteBlockWithState(block, allReceipts, w.publicState, w.privateState)
	if err != nil {
		log.Error("Failed writing block to chain", "err", err)
		return
	}
	if err := rawdb.WritePrivateBlockBloom(m.eth.ChainDb(), block.NumberU64(), w.privateReceipts); err != nil {
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
		work.privateState.Prepare(tx.Hash(), common.Hash{}, txCount)

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
