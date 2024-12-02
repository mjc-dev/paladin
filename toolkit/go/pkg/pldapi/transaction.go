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

package pldapi

import (
	"github.com/google/uuid"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
)

type TransactionType string

const (
	TransactionTypePrivate TransactionType = "private"
	TransactionTypePublic  TransactionType = "public"
)

func (tt TransactionType) Enum() tktypes.Enum[TransactionType] {
	return tktypes.Enum[TransactionType](tt)
}

func (tt TransactionType) Options() []string {
	return []string{
		string(TransactionTypePrivate),
		string(TransactionTypePublic),
	}
}

type SubmitMode string

const (
	SubmitModeAuto     SubmitMode = "auto"     // automatically submitted transaction by paladin
	SubmitModeExternal SubmitMode = "external" // the transaction will result in a prepared transaction, that can be downloaded and externally obtained
	SubmitModeCall     SubmitMode = "call"     // just a call (never persisted to a transaction in the DB)
	SubmitModePrepare  SubmitMode = "prepare"  // occurs when writing the prepared TXN back to the DB - does not get persisted
)

func (tt SubmitMode) Enum() tktypes.Enum[SubmitMode] {
	return tktypes.Enum[SubmitMode](tt)
}

func (tt SubmitMode) Options() []string {
	return []string{
		string(SubmitModeAuto),
		string(SubmitModeExternal),
		string(SubmitModeCall),
	}
}

func (tt SubmitMode) Default() string {
	return string(SubmitModeAuto)
}

// The Base fields that are input and output fields
type TransactionBase struct {
	IdempotencyKey string                        `docstruct:"Transaction" json:"idempotencyKey,omitempty"` // externally supplied unique identifier for this transaction. 409 Conflict will be returned on attempt to re-submit
	Type           tktypes.Enum[TransactionType] `docstruct:"Transaction" json:"type,omitempty"`           // public transactions go straight to a base ledger EVM smart contract. Private transactions use a Paladin domain to mask the on-chain data
	Domain         string                        `docstruct:"Transaction" json:"domain,omitempty"`         // name of a domain - only required on input for private deploy transactions (n/a for public, and inferred from "to" for invoke)
	Function       string                        `docstruct:"Transaction" json:"function,omitempty"`       // inferred from definition if not supplied. Resolved to full signature and stored. Required with abiReference on input if not constructor
	ABIReference   *tktypes.Bytes32              `docstruct:"Transaction" json:"abiReference,omitempty"`   // calculated if not supplied (ABI will be stored for you)
	From           string                        `docstruct:"Transaction" json:"from,omitempty"`           // locator for a local signing identity to use for submission of this transaction
	To             *tktypes.EthAddress           `docstruct:"Transaction" json:"to,omitempty"`             // the target contract, or null for a deploy
	Data           tktypes.RawJSON               `docstruct:"Transaction" json:"data,omitempty"`           // pre-encoded array with/without function selector, array, or object input
	PublicTxOptions
	// TODO: PrivateTransactions string list
	// TODO: PublicTransactions string list
}

// The full transaction you query, with input and output
type Transaction struct {
	ID         *uuid.UUID               `docstruct:"Transaction" json:"id,omitempty"`         // server generated UUID for this transaction (query only)
	Created    tktypes.Timestamp        `docstruct:"Transaction" json:"created,omitempty"`    // server generated creation timestamp for this transaction (query only)
	SubmitMode tktypes.Enum[SubmitMode] `docstruct:"Transaction" json:"submitMode,omitempty"` // empty unless submitted via PrepareTransaction route
	TransactionBase
}

// The input structure, containing the base input/output fields, along with some convenience fields resolved on input
type TransactionInput struct {
	TransactionBase
	DependsOn []uuid.UUID      `docstruct:"TransactionInput" json:"dependsOn,omitempty"` // these transactions must be mined on the blockchain successfully (or deleted) before this transaction submits. Failure of pre-reqs results in failure of this TX
	ABI       abi.ABI          `docstruct:"TransactionInput" json:"abi,omitempty"`       // required if abiReference not supplied
	Bytecode  tktypes.HexBytes `docstruct:"TransactionInput" json:"bytecode,omitempty"`  // for deploy this is prepended to the encoded data inputs
}

// Call also provides some options on how to execute the call
type TransactionCall struct {
	TransactionInput
	PublicCallOptions
	DataFormat tktypes.JSONFormatOptions `docstruct:"TransactionCall" json:"dataFormat"` // formatting options for the result data
}

// Additional fields returned on output when "full" specified
type TransactionFull struct {
	*Transaction
	DependsOn []uuid.UUID             `docstruct:"TransactionFull" json:"dependsOn,omitempty"` // transactions registered as dependencies when the transaction was created
	Receipt   *TransactionReceiptData `docstruct:"TransactionFull" json:"receipt"`             // available if the transaction has reached a final state
	Public    []*PublicTx             `docstruct:"TransactionFull" json:"public"`              // list of public transactions associated
	// TODO: PrivateTransactions object list
}

type ABIDecodedData struct {
	Data       tktypes.RawJSON `docstruct:"ABIDecodedData" json:"data"`
	Summary    string          `docstruct:"ABIDecodedData" json:"summary,omitempty"` // errors only
	Definition *abi.Entry      `docstruct:"ABIDecodedData" json:"definition"`
	Signature  string          `docstruct:"ABIDecodedData" json:"signature"`
}

type TransactionReceipt struct {
	ID uuid.UUID `docstruct:"TransactionReceipt" json:"id,omitempty"` // transaction ID
	TransactionReceiptData
}

type TransactionReceiptFull struct {
	*TransactionReceipt
	States             *TransactionStates `docstruct:"TransactionReceiptFull" json:"states,omitempty"`
	DomainReceipt      tktypes.RawJSON    `docstruct:"TransactionReceiptFull" json:"domainReceipt,omitempty"`
	DomainReceiptError string             `docstruct:"TransactionReceiptFull" json:"domainReceiptError,omitempty"`
}

type TransactionReceiptDataOnchain struct {
	TransactionHash  *tktypes.Bytes32 `docstruct:"TransactionReceiptDataOnchain" json:"transactionHash,omitempty"`
	BlockNumber      int64            `docstruct:"TransactionReceiptDataOnchain" json:"blockNumber,omitempty"`
	TransactionIndex int64            `docstruct:"TransactionReceiptDataOnchain" json:"transactionIndex,omitempty"`
}

type TransactionReceiptDataOnchainEvent struct {
	LogIndex int64              `docstruct:"TransactionReceiptDataOnchainEvent" json:"logIndex,omitempty"`
	Source   tktypes.EthAddress `docstruct:"TransactionReceiptDataOnchainEvent" json:"source,omitempty"`
}

type TransactionReceiptData struct {
	Indexed                             tktypes.Timestamp   `docstruct:"TransactionReceiptData" json:"indexed,omitempty"` // the time when this receipt was indexed
	Domain                              string              `docstruct:"TransactionReceiptData" json:"domain,omitempty"`  // only set on private transaction receipts
	Success                             bool                `docstruct:"TransactionReceiptData" json:"success,omitempty"` // true for success (note "status" is reserved for future use)
	*TransactionReceiptDataOnchain      `json:",inline"`    // if the result was finalized by the blockchain (note quirk of omitempty that we can't put zero-valid int pointers on main struct)
	*TransactionReceiptDataOnchainEvent `json:",inline"`    // if the result was finalized by the blockchain by an event
	FailureMessage                      string              `docstruct:"TransactionReceiptData" json:"failureMessage,omitempty"`  // always set to a non-empty string if the transaction reverted, with as much detail as could be extracted
	RevertData                          tktypes.HexBytes    `docstruct:"TransactionReceiptData" json:"revertData,omitempty"`      // encoded revert data if available
	ContractAddress                     *tktypes.EthAddress `docstruct:"TransactionReceiptData" json:"contractAddress,omitempty"` // address of the new contract address, to be used in the `To` field for subsequent invoke transactions.  Nil if this transaction itself was an invoke
}

type TransactionActivityRecord struct {
	Time    tktypes.Timestamp `docstruct:"TransactionActivityRecord" json:"time"`    // time the record occurred
	Message string            `docstruct:"TransactionActivityRecord" json:"message"` // a message
}

type TransactionDependencies struct {
	DependsOn []uuid.UUID `docstruct:"TransactionDependencies" json:"dependsOn"`
	PrereqOf  []uuid.UUID `docstruct:"TransactionDependencies" json:"prereqOf"`
}

type PreparedTransaction struct {
	ID          uuid.UUID           `docstruct:"PreparedTransaction" json:"id"`
	Domain      string              `docstruct:"PreparedTransaction" json:"domain"`
	To          *tktypes.EthAddress `docstruct:"PreparedTransaction" json:"to"`
	Transaction TransactionInput    `docstruct:"PreparedTransaction" json:"transaction"`
	Metadata    tktypes.RawJSON     `docstruct:"PreparedTransaction" json:"metadata,omitempty"`
	States      TransactionStates   `docstruct:"PreparedTransaction" json:"states"`
}