// Copyright © 2024 Kaleido, Inc.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package blockindexer

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hyperledger/firefly-common/pkg/i18n"
	"github.com/hyperledger/firefly-common/pkg/log"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/hyperledger/firefly-signer/pkg/rpcbackend"
	"github.com/kaleido-io/paladin/kata/internal/confutil"
	"github.com/kaleido-io/paladin/kata/internal/msgs"
	"github.com/kaleido-io/paladin/kata/internal/retry"
	"github.com/kaleido-io/paladin/kata/internal/rpcclient"
)

// blockListener has two functions:
// 1) To establish and keep track of what the head block height of the blockchain is, so event streams know how far from the head they are
// 2) To feed new block information to any registered consumers
type blockListener struct {
	ctx                        context.Context
	wsConn                     rpcbackend.WebSocketRPCClient // if configured the getting the blockheight will not complete until WS connects, overrides backend once connected
	listenLoopDone             chan struct{}
	initialBlockHeightObtained chan struct{}
	newHeadsTap                chan struct{}
	newHeadsSub                rpcbackend.Subscription
	highestBlock               uint64
	mux                        sync.Mutex
	blockPollingInterval       time.Duration
	unstableHeadLength         int
	canonicalChain             *list.List
	retry                      *retry.Retry
	newBlocks                  chan *BlockInfoJSONRPC
}

func newBlockListener(ctx context.Context, conf *Config, wsConfig *rpcclient.WSConfig) (bl *blockListener, err error) {
	wscConf, err := rpcclient.ParseWSConfig(ctx, wsConfig)
	if err != nil {
		return nil, err
	}
	chainHeadCacheLen := confutil.IntMin(conf.ChainHeadCacheLen, 1, *DefaultConfig.ChainHeadCacheLen)
	bl = &blockListener{
		ctx:                        log.WithLogField(ctx, "role", "blocklistener"),
		initialBlockHeightObtained: make(chan struct{}),
		newHeadsTap:                make(chan struct{}, 1),
		highestBlock:               0,
		blockPollingInterval:       confutil.DurationMin(conf.BlockPollingInterval, 1*time.Millisecond, *DefaultConfig.BlockPollingInterval),
		canonicalChain:             list.New(),
		unstableHeadLength:         chainHeadCacheLen,
		retry:                      retry.NewRetryIndefinite(&conf.Retry),
		wsConn:                     rpcbackend.NewWSRPCClient(wscConf),
		newBlocks:                  make(chan *BlockInfoJSONRPC, chainHeadCacheLen),
	}
	return bl, nil
}

func (bl *blockListener) start() {
	bl.mux.Lock()
	defer bl.mux.Unlock()
	if bl.listenLoopDone == nil {
		bl.listenLoopDone = make(chan struct{})
		go bl.listenLoop()
	}
}

func (bl *blockListener) channel() <-chan *BlockInfoJSONRPC {
	return bl.newBlocks
}

func (bl *blockListener) newHeadsSubListener() {
	for range bl.newHeadsSub.Notifications() {
		bl.tapNewHeads()
	}
}

func (bl *blockListener) tapNewHeads() {
	select {
	case bl.newHeadsTap <- struct{}{}:
		// Do nothing apart from tap the listener to wake up early
		// when there's a notification to the change of the head.
	default:
	}
}

// getBlockHeightWithRetry keeps retrying attempting to get the initial block height until successful
func (bl *blockListener) establishBlockHeightWithRetry() error {
	wsConnected := false
	return bl.retry.Do(bl.ctx, func(attempt int) (retry bool, err error) {

		// Connect the websocket if not yet connected on this retry iteration
		if !wsConnected {
			if err := bl.wsConn.Connect(bl.ctx); err != nil {
				log.L(bl.ctx).Warnf("WebSocket connection failed, blocking startup of block listener: %s", err)
				return true, err
			}
			// if we retry subscribe, we don't want to retry connect
			wsConnected = true
		}
		if bl.newHeadsSub == nil {
			// Once subscribed the backend will keep us subscribed over reconnect
			sub, rpcErr := bl.wsConn.Subscribe(bl.ctx, "newHeads")
			if rpcErr != nil {
				return true, rpcErr.Error()
			}
			bl.newHeadsSub = sub
			go bl.newHeadsSubListener()
		}

		// Now get the block height
		var hexBlockHeight ethtypes.HexUint64
		rpcErr := bl.wsConn.CallRPC(bl.ctx, &hexBlockHeight, "eth_blockNumber")
		if rpcErr != nil {
			log.L(bl.ctx).Warnf("Block height could not be obtained: %s", rpcErr.Message)
			return true, rpcErr.Error()
		}
		bl.mux.Lock()
		bl.highestBlock = hexBlockHeight.Uint64()
		bl.mux.Unlock()
		return false, nil
	})
}

func isNotFound(err *rpcbackend.RPCError) bool {
	if err != nil && err.Error() != nil {
		lowerCaseErr := strings.ToLower(err.Error().Error())
		if strings.Contains(lowerCaseErr, "not found") {
			return true
		}
	}
	return false
}

func (bl *blockListener) getBlockInfoByHash(ctx context.Context, blockHash string) (*BlockInfoJSONRPC, error) {
	var info *BlockInfoJSONRPC
	log.L(ctx).Debugf("Fetching block by hash %s", blockHash)
	err := bl.wsConn.CallRPC(ctx, &info, "eth_getBlockByHash", blockHash, false)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err.Error()
	}
	return info, nil
}

func (bl *blockListener) getBlockInfoByNumber(ctx context.Context, blockNumber ethtypes.HexUint64) (*BlockInfoJSONRPC, error) {
	var info *BlockInfoJSONRPC
	log.L(ctx).Debugf("Fetching block by number %d", blockNumber)
	err := bl.wsConn.CallRPC(ctx, &info, "eth_getBlockByNumber", blockNumber, false)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err.Error()
	}
	return info, nil
}

func (bl *blockListener) listenLoop() {
	defer close(bl.listenLoopDone)

	err := bl.establishBlockHeightWithRetry()
	close(bl.initialBlockHeightObtained)
	if err != nil {
		log.L(bl.ctx).Warnf("Block listener exiting before establishing initial block height: %s", err)
		return
	}

	var filter string
	failCount := 0
	for {
		if failCount > 0 {
			if err := bl.retry.WaitDelay(bl.ctx, failCount); err != nil {
				log.L(bl.ctx).Debugf("Block listener loop exiting: %s", err)
				return
			}
		} else {
			// Sleep for the polling interval, or until we're shoulder tapped by the newHeads listener
			select {
			case <-time.After(bl.blockPollingInterval):
			case <-bl.newHeadsTap:
			case <-bl.ctx.Done():
				log.L(bl.ctx).Debugf("Block listener loop stopping")
				return
			}
		}

		if filter == "" {
			err := bl.wsConn.CallRPC(bl.ctx, &filter, "eth_newBlockFilter")
			if err != nil {
				log.L(bl.ctx).Errorf("Failed to establish new block filter: %s", err.Message)
				failCount++
				continue
			}
		}

		var blockHashes []ethtypes.HexBytes0xPrefix
		rpcErr := bl.wsConn.CallRPC(bl.ctx, &blockHashes, "eth_getFilterChanges", filter)
		if rpcErr != nil {
			if isNotFound(rpcErr) {
				log.L(bl.ctx).Warnf("Block filter '%v' no longer valid. Recreating filter: %s", filter, rpcErr.Message)
				filter = ""
			}
			log.L(bl.ctx).Errorf("Failed to query block filter changes: %s", rpcErr.Message)
			failCount++
			continue
		}

		var notifyPos *list.Element
		for _, h := range blockHashes {
			// Do a lookup of the block (which will then go into our cache).
			bi, err := bl.getBlockInfoByHash(bl.ctx, h.String())
			switch {
			case err != nil:
				log.L(bl.ctx).Debugf("Failed to query block '%s': %s", h, err)
			case bi == nil:
				log.L(bl.ctx).Debugf("Block '%s' no longer available after notification (assuming due to re-org)", h)
			default:
				candidate := bl.reconcileCanonicalChain(bi)
				// Check this is the lowest position to notify from
				if candidate != nil && (notifyPos == nil || candidate.Value.(*BlockInfoJSONRPC).Number < notifyPos.Value.(*BlockInfoJSONRPC).Number) {
					notifyPos = candidate
				}
			}
		}
		if notifyPos != nil {
			// We notify for all hashes from the point of change in the chain onwards
			for notifyPos != nil {
				bl.notifyBlock(notifyPos.Value.(*BlockInfoJSONRPC))
				notifyPos = notifyPos.Next()
			}
		}

		// Reset retry count when we have a full successful loop
		failCount = 0

	}
}

func (bl *blockListener) notifyBlock(bi *BlockInfoJSONRPC) {
	// Does not block if context is cancelled, we'll handle that in other processing
	select {
	case bl.newBlocks <- bi:
	case <-bl.ctx.Done():
	}
}

// reconcileCanonicalChain takes an update on a block, and reconciles it against the in-memory view of the
// head of the canonical chain we have. If these blocks do not just fit onto the end of the chain, then we
// work backwards building a new view and notify about all blocks that are changed in that process.
func (bl *blockListener) reconcileCanonicalChain(bi *BlockInfoJSONRPC) *list.Element {

	bl.mux.Lock()
	if bi.Number.Uint64() > bl.highestBlock {
		bl.highestBlock = bi.Number.Uint64()
	}
	bl.mux.Unlock()

	// Find the position of this block in the block sequence
	pos := bl.canonicalChain.Back()
	for {
		if pos == nil || pos.Value == nil {
			// We've eliminated all the existing chain (if there was any)
			return bl.handleNewBlock(bi, nil)
		}
		posBlock := pos.Value.(*BlockInfoJSONRPC)
		switch {
		case posBlock.Number == bi.Number && posBlock.Hash.Equals(bi.Hash) && posBlock.ParentHash.Equals(bi.ParentHash):
			// This is a duplicate - no need to notify of anything
			return nil
		case posBlock.Number == bi.Number:
			// We are replacing a block in the chain
			return bl.handleNewBlock(bi, pos.Prev())
		case posBlock.Number < bi.Number:
			// We have a position where this block goes
			return bl.handleNewBlock(bi, pos)
		default:
			// We've not wound back to the point this block fits yet
			pos = pos.Prev()
		}
	}
}

// handleNewBlock rebuilds the canonical chain around a new block, checking if we need to rebuild our
// view of the canonical chain behind it, or trimming anything after it that is invalidated by a new fork.
func (bl *blockListener) handleNewBlock(bi *BlockInfoJSONRPC, addAfter *list.Element) *list.Element {

	// If we have an existing canonical chain before this point, then we need to check we've not
	// invalidated that with this block. If we have, then we have to re-verify our whole canonical
	// chain from the first block. Then notify from the earliest point where it has diverged.
	if addAfter != nil {
		prevBlock := addAfter.Value.(*BlockInfoJSONRPC)
		if prevBlock.Number != (bi.Number-1) || !prevBlock.Hash.Equals(bi.ParentHash) {
			log.L(bl.ctx).Infof("Notified of block %d / %s that does not fit after block %d / %s (expected parent: %s)", bi.Number, bi.Hash, prevBlock.Number, prevBlock.Hash, bi.ParentHash)
			return bl.rebuildCanonicalChain()
		}
	}

	// Ok, we can add this block
	var newElem *list.Element
	if addAfter == nil {
		_ = bl.canonicalChain.Init()
		newElem = bl.canonicalChain.PushBack(bi)
	} else {
		newElem = bl.canonicalChain.InsertAfter(bi, addAfter)
		// Trim everything from this point onwards. Note that the following cases are covered on other paths:
		// - This was just a duplicate notification of a block that fits into our chain - discarded in reconcileCanonicalChain()
		// - There was a gap before us in the chain, and the tail is still valid - we would have called rebuildCanonicalChain() above
		nextElem := newElem.Next()
		for nextElem != nil {
			toRemove := nextElem
			nextElem = nextElem.Next()
			_ = bl.canonicalChain.Remove(toRemove)
		}
	}

	// See if we have blocks that are valid for notification
	for bl.canonicalChain.Len() > bl.unstableHeadLength {
		_ = bl.canonicalChain.Remove(bl.canonicalChain.Front())
	}

	log.L(bl.ctx).Debugf("Added block %d / %s parent=%s to in-memory canonical chain (new length=%d)", bi.Number, bi.Hash, bi.ParentHash, bl.canonicalChain.Len())

	return newElem

}

// rebuildCanonicalChain is called (only on non-empty case) when our current chain does not seem to line up with
// a recent block advertisement. So we need to work backwards to the last point of consistency with the current
// chain and re-query the chain state from there.
func (bl *blockListener) rebuildCanonicalChain() *list.Element {

	log.L(bl.ctx).Debugf("Rebuilding in-memory canonical chain")

	// If none of our blocks were valid, start from the first block number we've notified about previously
	lastValidBlock := bl.trimToLastValidBlock()
	var nextBlockNumber ethtypes.HexUint64
	var expectedParentHash ethtypes.HexBytes0xPrefix
	if lastValidBlock != nil {
		nextBlockNumber = lastValidBlock.Number + 1
		expectedParentHash = lastValidBlock.Hash
	} else {
		firstBlock := bl.canonicalChain.Front()
		if firstBlock == nil || firstBlock.Value == nil {
			return nil
		}
		nextBlockNumber = firstBlock.Value.(*BlockInfoJSONRPC).Number
		// Clear out the whole chain
		bl.canonicalChain = bl.canonicalChain.Init()
	}
	var notifyPos *list.Element
	for {
		var bi *BlockInfoJSONRPC
		err := bl.retry.Do(bl.ctx, func(attempt int) (retry bool, err error) {
			bi, err = bl.getBlockInfoByNumber(bl.ctx, nextBlockNumber)
			return true, err
		})
		if err != nil {
			return nil // Context must have been cancelled
		}
		if bi == nil {
			log.L(bl.ctx).Debugf("Block listener canonical chain view rebuilt to head at block %d", nextBlockNumber-1)
			break
		}

		// It's possible the chain will change while we're doing this, and we fall back to the next block notification
		// to sort that out.
		if expectedParentHash != nil && !bi.ParentHash.Equals(expectedParentHash) {
			log.L(bl.ctx).Debugf("Block listener canonical chain view rebuilt up to new re-org at block %d", nextBlockNumber)
			break
		}
		expectedParentHash = bi.Hash
		nextBlockNumber++

		// Note we do not trim to a length here, as we need to notify for every block we haven't notified for.
		// Trimming to a length will happen when we get blocks that slot into our existing view
		newElem := bl.canonicalChain.PushBack(bi)
		if notifyPos == nil {
			notifyPos = newElem
		}

		bl.mux.Lock()
		if bi.Number.Uint64() > bl.highestBlock {
			bl.highestBlock = bi.Number.Uint64()
		}
		bl.mux.Unlock()

	}
	return notifyPos
}

func (bl *blockListener) trimToLastValidBlock() (lastValidBlock *BlockInfoJSONRPC) {
	// First remove from the end until we get a block that matches the current un-cached query view from the chain
	lastElem := bl.canonicalChain.Back()
	for lastElem != nil && lastElem.Value != nil {

		// Query the block that is no at this blockNumber
		currentViewBlock := lastElem.Value.(*BlockInfoJSONRPC)
		var freshBlockInfo *BlockInfoJSONRPC
		err := bl.retry.Do(bl.ctx, func(attempt int) (retry bool, err error) {
			freshBlockInfo, err = bl.getBlockInfoByNumber(bl.ctx, currentViewBlock.Number)
			return true, err
		})
		if err != nil {
			return nil // Context must have been cancelled
		}

		if freshBlockInfo != nil && freshBlockInfo.Hash.Equals(currentViewBlock.Hash) {
			log.L(bl.ctx).Debugf("Canonical chain matches current chain up to block %d", currentViewBlock.Number)
			lastValidBlock = currentViewBlock
			// Trim everything after this point, as it's invalidated
			nextElem := lastElem.Next()
			for nextElem != nil {
				toRemove := lastElem
				nextElem = nextElem.Next()
				_ = bl.canonicalChain.Remove(toRemove)
			}
			break
		}
		lastElem = lastElem.Prev()

	}
	return lastValidBlock
}

func (bl *blockListener) getHighestBlock(ctx context.Context) (uint64, error) {
	// if not yet initialized, wait to be initialized
	select {
	case <-bl.initialBlockHeightObtained:
	case <-ctx.Done(): // Inform caller we timed out, or were closed
		return 0, i18n.NewError(bl.ctx, msgs.MsgContextCanceled)
	case <-bl.ctx.Done(): // Inform caller we timed out, or were closed
		return 0, i18n.NewError(bl.ctx, msgs.MsgContextCanceled)
	}
	bl.mux.Lock()
	highestBlock := bl.highestBlock
	bl.mux.Unlock()
	log.L(bl.ctx).Debugf("ChainHead=%d", highestBlock)
	return highestBlock, nil
}

func (bl *blockListener) waitClosed() {
	bl.mux.Lock()
	listenLoopDone := bl.listenLoopDone
	bl.mux.Unlock()
	if bl.wsConn != nil {
		_ = bl.wsConn.UnsubscribeAll(bl.ctx)
		bl.wsConn.Close()
	}
	if listenLoopDone != nil {
		<-listenLoopDone
	}
}