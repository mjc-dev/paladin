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

package statemgr

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/kaleido-io/paladin/core/internal/components"
	"github.com/kaleido-io/paladin/core/internal/filters"
	"github.com/kaleido-io/paladin/toolkit/pkg/query"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeCoinABI = `{
	"type": "tuple",
	"internalType": "struct FakeCoin",
	"components": [
		{
			"name": "salt",
			"type": "bytes32"
		},
		{
			"name": "owner",
			"type": "address",
			"indexed": true
		},
		{
			"name": "amount",
			"type": "uint256",
			"indexed": true
		}
	]
}`

type FakeCoin struct {
	Amount ethtypes.HexInteger       `json:"amount"`
	Salt   ethtypes.HexBytes0xPrefix `json:"salt"`
}

func parseFakeCoin(t *testing.T, s *components.State) *FakeCoin {
	var c FakeCoin
	err := json.Unmarshal(s.Data, &c)
	require.NoError(t, err)
	return &c
}

func TestStateFlushAsyncNoWork(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	contractAddress := tktypes.RandAddress()
	flushed := make(chan error)

	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress)
	defer dc.Close(ctx)

	err := dc.InitiateFlush(ctx, func(err error) { flushed <- err })
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		select {
		case err := <-flushed:
			require.NoError(t, err)
			return true
		default:
			return false
		}
	}, 1*time.Second, 10*time.Millisecond)

}

func TestUpsertSchemaEmptyList(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schemas, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{})
	require.NoError(t, err)
	require.Len(t, schemas, 0)

}

func TestUpsertSchemaAndStates(t *testing.T) {

	ctx, ss, done := newDBTestStateManager(t)
	defer done()

	schemas, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{testABIParam(t, fakeCoinABI)})
	require.NoError(t, err)
	require.Len(t, schemas, 1)
	schemaID := schemas[0].ID()
	fakeHash := tktypes.HexBytes(tktypes.RandBytes(32))

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress)
	defer dc.Close(ctx)

	states, err := dc.UpsertStates(ctx,
		&components.StateUpsert{
			SchemaID: schemaID,
			Data:     tktypes.RawJSON(fmt.Sprintf(`{"amount": 100, "owner": "0x1eDfD974fE6828dE81a1a762df680111870B7cDD", "salt": "%s"}`, tktypes.RandHex(32))),
		},
		&components.StateUpsert{
			ID:       fakeHash,
			SchemaID: schemaID,
			Data:     tktypes.RawJSON(fmt.Sprintf(`{"amount": 100, "owner": "0x1eDfD974fE6828dE81a1a762df680111870B7cDD", "salt": "%s"}`, tktypes.RandHex(32))),
		},
	)
	require.NoError(t, err)
	require.Len(t, states, 2)
	assert.NotEmpty(t, states[0].ID)
	assert.Equal(t, fakeHash, states[1].ID)

}

func TestStateContextMintSpendMint(t *testing.T) {

	ctx, ss, done := newDBTestStateManager(t)
	defer done()

	transactionID1 := uuid.New()
	var schemaID tktypes.Bytes32

	// Pop in our widget ABI
	schemas, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{testABIParam(t, fakeCoinABI)})
	require.NoError(t, err)
	assert.Len(t, schemas, 1)
	schemaID = schemas[0].ID()

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress)
	defer dc.Close(ctx)

	// Store some states
	tx1states, err := dc.UpsertStates(ctx,
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 100, "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID1},
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 10,  "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID1},
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 75,  "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID1},
	)
	require.NoError(t, err)
	assert.Len(t, tx1states, 3)

	// Mark an in-memory read - doesn't affect it's availability
	transactionID2 := uuid.New()
	err = dc.AddStateLocks(ctx,
		&components.StateLock{Type: components.StateLockTypeRead.Enum(), State: tx1states[1].ID, Transaction: transactionID2},
	)
	require.NoError(t, err)

	// Query the states, and notice we find the ones that are still in the process of creating
	// even though they've not yet been written to the DB
	_, states, err := dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 3)

	// The values should be sorted according to the requested order
	assert.Equal(t, int64(10), parseFakeCoin(t, states[0]).Amount.Int64())
	assert.Equal(t, int64(75), parseFakeCoin(t, states[1]).Amount.Int64())
	assert.Equal(t, int64(100), parseFakeCoin(t, states[2]).Amount.Int64())
	require.Len(t, states[0].Locks, 2)
	assert.Equal(t, components.StateLockTypeCreate, states[0].Locks[0].Type.V()) // should be marked creating
	assert.Equal(t, transactionID1, states[0].Locks[0].Transaction)              // for the transaction we specified
	assert.Equal(t, components.StateLockTypeRead, states[0].Locks[1].Type.V())   // should be marked read
	assert.Equal(t, transactionID2, states[0].Locks[1].Transaction)              // for the transaction we specified

	// Simulate a transaction where we spend two states, and create 2 new ones
	transactionID3 := uuid.New()
	err = dc.AddStateLocks(ctx,
		&components.StateLock{Type: components.StateLockTypeSpend.Enum(), State: states[0].ID, Transaction: transactionID3}, // 10 +
		&components.StateLock{Type: components.StateLockTypeSpend.Enum(), State: states[1].ID, Transaction: transactionID3}, // 75 +
	)
	require.NoError(t, err)

	// Do a quick check on upsert semantics with un-flushed updates, to make sure the unflushed list doesn't dup
	tx2states, err := dc.UpsertStates(ctx,
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 35, "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID3},
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 50, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID3},
	)
	require.NoError(t, err)
	assert.Len(t, tx2states, 2)
	assert.Equal(t, len(dc.(*domainContext).unFlushed.states), 5)
	assert.Equal(t, len(dc.(*domainContext).txLocks), 8)

	// Query the states on the first address
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().
		Equal("owner", "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180").Sort("-amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 2)
	assert.Equal(t, int64(100), parseFakeCoin(t, states[0]).Amount.Int64())
	assert.Equal(t, int64(35), parseFakeCoin(t, states[1]).Amount.Int64())

	// Query the states on the other address
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().
		Equal("owner", "0x615dD09124271D8008225054d85Ffe720E7a447A").Sort("-amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 1)
	assert.Equal(t, int64(50), parseFakeCoin(t, states[0]).Amount.Int64())

	// Flush the states to the database
	syncFlushContext(t, ctx, dc)

	// Check the DB persisted state is what we expect
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("owner", "amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 3)
	assert.Equal(t, int64(50), parseFakeCoin(t, states[0]).Amount.Int64())
	assert.Equal(t, int64(35), parseFakeCoin(t, states[1]).Amount.Int64())
	assert.Equal(t, int64(100), parseFakeCoin(t, states[2]).Amount.Int64())

	// Write another transaction that splits a coin to two
	transactionID4 := uuid.New()
	err = dc.AddStateLocks(ctx,
		&components.StateLock{Type: components.StateLockTypeSpend.Enum(), State: states[0].ID, Transaction: transactionID4}, // 50
	)
	require.NoError(t, err)
	tx3states, err := dc.UpsertStates(ctx,
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID4},
		&components.StateUpsert{SchemaID: schemaID, Data: tktypes.RawJSON(fmt.Sprintf(`{"amount": 30, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`, tktypes.RandHex(32))), CreatedBy: &transactionID4},
	)
	require.NoError(t, err)
	assert.Len(t, tx3states, 2)

	// Now check that we merge the DB and in-memory state
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("owner", "amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 4)
	assert.Equal(t, int64(20), parseFakeCoin(t, states[0]).Amount.Int64())
	assert.Equal(t, int64(30), parseFakeCoin(t, states[1]).Amount.Int64())
	assert.Equal(t, int64(35), parseFakeCoin(t, states[2]).Amount.Int64())
	assert.Equal(t, int64(100), parseFakeCoin(t, states[3]).Amount.Int64())

	// Check the limit works too across this
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("owner", "amount").Limit(1).Query())
	require.NoError(t, err)
	assert.Len(t, states, 1)
	assert.Equal(t, int64(20), parseFakeCoin(t, states[0]).Amount.Int64())

	syncFlushContext(t, ctx, dc)

	// Write confirmations for all the things that happened above
	var spends []*components.StateSpend
	var confirms []*components.StateConfirm
	for _, lock := range dc.(*domainContext).txLocks {
		switch lock.Type.V() {
		case components.StateLockTypeSpend:
			spends = append(spends, &components.StateSpend{DomainName: "domain1", State: lock.State, Transaction: lock.Transaction})
		case components.StateLockTypeCreate:
			confirms = append(confirms, &components.StateConfirm{DomainName: "domain1", State: lock.State, Transaction: lock.Transaction})
		}
	}
	// We add one extra spend that simulates something happening outside of this context
	transactionID5 := uuid.New()
	spends = append(spends, &components.StateSpend{DomainName: "domain1", State: states[0].ID, Transaction: transactionID5}) //20
	err = ss.WriteStateFinalizations(ss.bgCtx, ss.p.DB(), spends, confirms)
	require.NoError(t, err)

	// So in the domain context, this states will still be visible - because we don't have transactionID5
	// that spends the state, but we have transactionID4 that created the state in our in-memory.
	// So the right thing that would happen in practice, is we would clear transactionID4 when the confirmation
	// was notified to us.
	dc.ResetTransactions(ctx, transactionID1)
	dc.ResetTransactions(ctx, transactionID3)
	dc.ResetTransactions(ctx, transactionID4)

	// We left the read
	assert.Len(t, dc.(*domainContext).txLocks, 1)
	assert.Equal(t, components.StateLockTypeRead, dc.(*domainContext).txLocks[0].Type.V()) // should be marked read
	assert.Equal(t, transactionID2, dc.(*domainContext).txLocks[0].Transaction)            // for the transaction we specified

	// Check the remaining states
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("owner", "amount").Query())
	require.NoError(t, err)
	assert.Len(t, states, 3)
	assert.Equal(t, int64(30), parseFakeCoin(t, states[0]).Amount.Int64())
	assert.Equal(t, int64(35), parseFakeCoin(t, states[1]).Amount.Int64())
	assert.Equal(t, int64(100), parseFakeCoin(t, states[2]).Amount.Int64())

}

func TestStateContextMintSpendWithNullifier(t *testing.T) {

	ctx, ss, done := newDBTestStateManager(t)
	defer done()

	transactionID1 := uuid.New()

	schemas, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{testABIParam(t, fakeCoinABI)})
	require.NoError(t, err)
	assert.Len(t, schemas, 1)
	schemaID := schemas[0].ID()
	stateID1 := tktypes.HexBytes(tktypes.RandBytes(32))
	stateID2 := tktypes.HexBytes(tktypes.RandBytes(32))
	nullifier1 := tktypes.HexBytes(tktypes.RandBytes(32))
	nullifier2 := tktypes.HexBytes(tktypes.RandBytes(32))
	data1 := tktypes.RawJSON(fmt.Sprintf(`{"amount": 100, "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32)))
	data2 := tktypes.RawJSON(fmt.Sprintf(`{"amount": 10,  "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32)))

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress)
	defer dc.Close(ctx)

	// Start with 2 states
	tx1states, err := dc.UpsertStates(ctx,
		&components.StateUpsert{ID: stateID1, SchemaID: schemaID, Data: data1, CreatedBy: &transactionID1},
		&components.StateUpsert{ID: stateID2, SchemaID: schemaID, Data: data2, CreatedBy: &transactionID1},
	)
	require.NoError(t, err)
	assert.Len(t, tx1states, 2)

	_, states, err := dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 2)
	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 0)

	// Attach a nullifier to the first state
	err = dc.UpsertNullifiers(ctx,
		&components.NullifierUpsert{State: stateID1, ID: nullifier1},
	)
	require.NoError(t, err)

	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.NotNil(t, states[0].Nullifier)
	assert.Equal(t, nullifier1, states[0].Nullifier.ID)

	// Flush the states to the database
	syncFlushContext(t, ctx, dc)

	// Confirm still 2 states and 1 nullifier
	_, states, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 2)
	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 1)
	require.NotNil(t, states[0].Nullifier)
	assert.Equal(t, nullifier1, states[0].Nullifier.ID)

	syncFlushContext(t, ctx, dc)

	// Mark both states confirmed
	err = ss.WriteStateFinalizations(ss.bgCtx, ss.p.DB(), []*components.StateSpend{},
		[]*components.StateConfirm{
			{DomainName: "domain1", State: stateID1, Transaction: transactionID1},
			{DomainName: "domain1", State: stateID2, Transaction: transactionID1},
		})
	require.NoError(t, err)

	// Mark the first state as "spending"
	transactionID2 := uuid.New()
	err = dc.AddStateLocks(ctx,
		&components.StateLock{Type: components.StateLockTypeSpend.Enum(), State: stateID1, Transaction: transactionID2},
	)
	assert.NoError(t, err)

	// Confirm no more nullifiers available
	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 0)

	// Reset transaction to unlock
	dc.ResetTransactions(ctx, transactionID2)
	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 1)

	syncFlushContext(t, ctx, dc)

	// Spend the state associated with nullifier
	transactionID3 := uuid.New()
	err = ss.WriteStateFinalizations(ss.bgCtx, ss.p.DB(),
		[]*components.StateSpend{
			{DomainName: "domain1", State: nullifier1, Transaction: transactionID3},
		}, []*components.StateConfirm{})
	require.NoError(t, err)

	// reset the domain context so we're working from the db
	dc.Reset(ctx)

	// Confirm no more nullifiers available
	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	assert.Len(t, states, 0)

	// Attach a nullifier to the second state
	// Note - this is only allowed when the state is loaded into the context for creation, as otherwise:
	// - queries within the context before the nullifier is flushed would not return the nullifier
	// - the creation of the nullifier in the DB might fail due to the state not existing
	err = dc.UpsertNullifiers(ctx, &components.NullifierUpsert{State: stateID2, ID: nullifier2})
	assert.Regexp(t, "PD010126", err)
	_, err = dc.UpsertStates(ctx, &components.StateUpsert{ID: stateID2, SchemaID: schemaID, Data: data2, CreatedBy: &transactionID1})
	require.NoError(t, err)
	err = dc.UpsertNullifiers(ctx, &components.NullifierUpsert{State: stateID2, ID: nullifier2})
	require.NoError(t, err)

	_, states, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Query())
	require.NoError(t, err)
	require.Len(t, states, 1)
	require.NotNil(t, states[0].Nullifier)
	assert.Equal(t, nullifier2, states[0].Nullifier.ID)

}

func TestBadSchema(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	_, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{{}})
	assert.Regexp(t, "PD010114", err)

}

func TestDomainContextFlushErrorCapture(t *testing.T) {

	ctx, ss, db, done := newDBMockStateManager(t)
	defer done()

	db.ExpectExec("INSERT.*schemas").WillReturnResult(driver.ResultNoRows)
	db.ExpectBegin()
	db.ExpectExec("INSERT").WillReturnError(fmt.Errorf("pop"))

	schemas, err := ss.EnsureABISchemas(ctx, ss.p.DB(), "domain1", []*abi.Parameter{testABIParam(t, fakeCoinABI)})
	require.NoError(t, err)

	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schemas[0].ID()), schemas[0])

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress)
	defer dc.Close(ctx)

	data1 := fmt.Sprintf(`{"amount": 100, "owner": "0xf7b1c69F5690993F2C8ecE56cc89D42b1e737180", "salt": "%s"}`, tktypes.RandHex(32))
	tx1 := uuid.New()
	_, err = dc.UpsertStates(ctx, genWidget(t, schemas[0].ID(), &tx1, data1))
	require.NoError(t, err)

	flushed := make(chan error)
	err = dc.InitiateFlush(ctx, func(err error) {
		flushed <- err
	})
	require.NoError(t, err)
	assert.Regexp(t, "pop", <-flushed)

	_, _, err = dc.FindAvailableStates(ctx, schemas[0].ID(), nil)
	assert.Regexp(t, "PD010119.*pop", err)

	_, _, err = dc.FindAvailableStates(ctx, schemas[0].ID(), nil)
	assert.Regexp(t, "PD010119.*pop", err)

	_, err = dc.(*domainContext).mergeUnFlushedApplyLocks(ctx, schemas[0], nil, nil, false)
	assert.Regexp(t, "PD010119.*pop", err)

	_, err = dc.UpsertStates(ctx, genWidget(t, schemas[0].ID(), &tx1, data1))
	assert.Regexp(t, "PD010119.*pop", err)

	err = dc.UpsertNullifiers(ctx)
	assert.Regexp(t, "PD010119.*pop", err)

	err = dc.AddStateLocks(ctx, &components.StateLock{})
	assert.Regexp(t, "PD010119.*pop", err)

	err = dc.InitiateFlush(ctx, func(err error) {})
	assert.Regexp(t, "pop", err)

	dc.Reset(ctx)

	err = dc.(*domainContext).checkResetInitUnFlushed(ctx)
	require.NoError(t, err)
}

func TestDCMergedUnFlushedWhileFlushing(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	s1, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)
	tx1 := uuid.New()
	_, err = dc.UpsertStates(ctx, &components.StateUpsert{ID: s1.ID, SchemaID: schema.ID(), Data: s1.Data, CreatedBy: &tx1})
	require.NoError(t, err)

	// Fake a flush transition
	dc.flushing = dc.unFlushed

	// We'll merge in creating
	states, err := dc.mergeUnFlushedApplyLocks(ctx, schema, []*components.State{}, &query.QueryJSON{
		Sort: []string{".created"},
	}, false /* no nullifier required */)
	require.NoError(t, err)
	assert.Len(t, states, 1)

	// Unless we require a nullifier
	states, err = dc.mergeUnFlushedApplyLocks(ctx, schema, []*components.State{}, &query.QueryJSON{
		Sort: []string{".created"},
	}, true /* nullifier required */)
	require.NoError(t, err)
	assert.Len(t, states, 0)

	// But we can have an unflushed nullifier
	err = dc.UpsertNullifiers(ctx, &components.NullifierUpsert{ID: tktypes.RandBytes(32), State: s1.ID})
	require.NoError(t, err)

	// Fake a flush transition
	dc.flushing.stateNullifiers = append(dc.flushing.stateNullifiers, dc.unFlushed.stateNullifiers...)

	// And then it will return the state
	states, err = dc.mergeUnFlushedApplyLocks(ctx, schema, []*components.State{}, &query.QueryJSON{
		Sort: []string{".created"},
	}, true /* nullifier required */)
	require.NoError(t, err)
	assert.Len(t, states, 1)

}

func TestDCMergedUnFlushedWhileFlushingDedup(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	// Add a first state that will be included in the query
	s1, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 10, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)
	tx1 := uuid.New()
	_, err = dc.UpsertStates(ctx, &components.StateUpsert{ID: s1.ID, SchemaID: schema.ID(), Data: s1.Data, CreatedBy: &tx1})
	require.NoError(t, err)

	// We add a second state, that will be excluded from the query due to a spending lock
	s2, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)
	_, err = dc.UpsertStates(ctx, &components.StateUpsert{ID: s2.ID, SchemaID: schema.ID(), Data: s2.Data, CreatedBy: &tx1})
	require.NoError(t, err)
	err = dc.AddStateLocks(ctx, &components.StateLock{Type: components.StateLockTypeSpend.Enum(), State: s2.ID, Transaction: tx1})
	require.NoError(t, err)

	// Fake a flush transition
	dc.flushing = dc.unFlushed

	spending, _, _, err := dc.getUnFlushedSpends(ctx)
	require.NoError(t, err)
	assert.Len(t, spending, 1)

	// Simulate the DB having returned us the same state we ask for
	dc.stateLock.Lock()
	inTheFlush := dc.flushing.states[0]
	assert.Equal(t, s1.ID, inTheFlush.State.ID)
	dc.stateLock.Unlock()

	states, err := dc.mergeUnFlushedApplyLocks(ctx, schema, []*components.State{
		inTheFlush.State,
	}, &query.QueryJSON{
		Sort: []string{".created"},
	}, false)
	require.NoError(t, err)
	assert.Len(t, states, 1)

}

func TestDCMergedUnFlushedEvalError(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	s1, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)
	tx1 := uuid.New()
	_, err = dc.UpsertStates(ctx, &components.StateUpsert{ID: s1.ID, SchemaID: schema.ID(), Data: s1.Data, CreatedBy: &tx1})
	require.NoError(t, err)

	_, err = dc.mergeUnFlushedApplyLocks(ctx, schema, []*components.State{},
		query.NewQueryBuilder().Equal("wrong", "any").Query(), false)
	assert.Regexp(t, "PD010700", err)

}

func TestDCMergedInMemoryMatchesRecoverLabelsFail(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	s1, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)
	s1.Data = tktypes.RawJSON(`! wrong `)

	// Insert broken state into our unflushed state list
	dc.flushing = dc.ss.writer.newWriteOp("domain1", *contractAddress)
	dc.flushing.states = append(dc.flushing.states, s1)

	_, err = dc.mergeInMemoryMatches(ctx, schema, []*components.State{
		s1.State,
	}, []*components.StateWithLabels{}, nil)
	assert.Regexp(t, "PD010116", err)

}

func TestDCMergedInMemoryMatchesSortFail(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	s1, err := schema.ProcessState(ctx, *contractAddress, tktypes.RawJSON(fmt.Sprintf(
		`{"amount": 20, "owner": "0x615dD09124271D8008225054d85Ffe720E7a447A", "salt": "%s"}`,
		tktypes.RandHex(32))), nil)
	require.NoError(t, err)

	// Insert state into our unflushed state list
	dc.flushing = dc.ss.writer.newWriteOp("domain1", *contractAddress)
	dc.flushing.states = append(dc.flushing.states, s1)

	_, err = dc.mergeInMemoryMatches(ctx, schema, []*components.State{
		s1.State,
	}, []*components.StateWithLabels{}, query.NewQueryBuilder().Sort("wrong").Query())
	assert.Regexp(t, "PD010700", err)
}

func TestDCFindBadQueryAndInsertBadValue(t *testing.T) {

	ctx, ss, done := newDBTestStateManager(t)
	defer done()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	ss.abiSchemaCache.Set(schemaCacheKey("domain1", schema.ID()), schema)

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	schemaID := schema.ID()
	assert.Equal(t, "type=FakeCoin(bytes32 salt,address owner,uint256 amount),labels=[owner,amount]", schema.Signature())

	_, _, err = dc.FindAvailableStates(ctx, schemaID, query.NewQueryBuilder().Sort("wrong").Query())
	assert.Regexp(t, "PD010700", err)

	_, _, err = dc.FindAvailableNullifiers(ctx, schemaID, query.NewQueryBuilder().Sort("wrong").Query())
	assert.Regexp(t, "PD010700", err)

	_, err = dc.UpsertStates(ctx, &components.StateUpsert{
		SchemaID: schemaID, Data: tktypes.RawJSON(`"wrong"`),
	})
	assert.Regexp(t, "FF22038", err)

}

func TestDCResetWithMixedTxns(t *testing.T) {

	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	state1 := tktypes.HexBytes("state1")
	transactionID1 := uuid.New()
	err := dc.AddStateLocks(ctx,
		&components.StateLock{State: state1, Type: components.StateLockTypeRead.Enum(), Transaction: transactionID1})
	require.NoError(t, err)

	state2 := tktypes.HexBytes("state2")
	transactionID2 := uuid.New()
	err = dc.AddStateLocks(ctx,
		&components.StateLock{State: state2, Type: components.StateLockTypeSpend.Enum(), Transaction: transactionID2})
	require.NoError(t, err)

	dc.ResetTransactions(ctx, transactionID1)

	assert.Len(t, dc.txLocks, 1)
	assert.Equal(t, dc.txLocks[0].State, state2)

}

func TestCheckEvalGTTimestamp(t *testing.T) {
	ctx, ss, _, done := newDBMockStateManager(t)
	defer done()

	contractAddress := tktypes.RandAddress()
	dc := ss.NewDomainContext(ctx, "domain1", *contractAddress).(*domainContext)
	defer dc.Close(ctx)

	jq := query.NewQueryBuilder().GreaterThan(".created", 1726545933211347000).Limit(10).Sort(".created").Query()

	schema, err := newABISchema(ctx, "domain1", testABIParam(t, fakeCoinABI))
	require.NoError(t, err)
	labelSet := dc.ss.labelSetFor(schema)

	ls := filters.PassthroughValueSet{}

	stateID := tktypes.MustParseHexBytes("2eaf4727b7c7e9b3728b1344ac38ea6d8698603dc3b41d9458d7c011c20ce672")

	// create time is equal - no match
	created := tktypes.TimestampFromUnix(1726545933211347000)
	addStateBaseLabels(ls, stateID, created)
	match, err := filters.EvalQuery(ctx, jq, labelSet, ls)
	assert.NoError(t, err)
	assert.False(t, match)

	// create time is greater - match
	created = tktypes.TimestampFromUnix(1726545933211347001)
	addStateBaseLabels(ls, stateID, created)
	match, err = filters.EvalQuery(ctx, jq, labelSet, ls)
	assert.NoError(t, err)
	assert.True(t, match)

	// create time is less - no match
	created = tktypes.TimestampFromUnix(1726545933211346999)
	addStateBaseLabels(ls, stateID, created)
	match, err = filters.EvalQuery(ctx, jq, labelSet, ls)
	assert.NoError(t, err)
	assert.False(t, match)

}
