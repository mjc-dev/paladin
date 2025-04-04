/*
 * Copyright © 2025 Kaleido, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with
 * the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on
 * an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 */

package ethclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/hyperledger/firefly-common/pkg/fftypes"
	"github.com/hyperledger/firefly-signer/pkg/abi"
	"github.com/hyperledger/firefly-signer/pkg/ethsigner"
	"github.com/hyperledger/firefly-signer/pkg/ethtypes"
	"github.com/hyperledger/firefly-signer/pkg/secp256k1"
	"github.com/kaleido-io/paladin/config/pkg/confutil"
	"github.com/kaleido-io/paladin/config/pkg/pldconf"
	"github.com/kaleido-io/paladin/core/internal/msgs"
	"github.com/kaleido-io/paladin/toolkit/pkg/algorithms"
	"github.com/kaleido-io/paladin/toolkit/pkg/i18n"
	"github.com/kaleido-io/paladin/toolkit/pkg/log"
	"github.com/kaleido-io/paladin/toolkit/pkg/prototk"
	"github.com/kaleido-io/paladin/toolkit/pkg/rpcclient"
	"github.com/kaleido-io/paladin/toolkit/pkg/signerapi"
	"github.com/kaleido-io/paladin/toolkit/pkg/signpayloads"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
	"github.com/kaleido-io/paladin/toolkit/pkg/verifiers"
	"golang.org/x/crypto/sha3"
)

// Low level EthClient helpers for submission
type EthClient interface {
	Close()
	ChainID() int64

	GasPrice(ctx context.Context) (gasPrice *tktypes.HexUint256, err error)
	GetBalance(ctx context.Context, address tktypes.EthAddress, block string) (balance *tktypes.HexUint256, err error)
	GetTransactionReceipt(ctx context.Context, txHash string) (*TransactionReceiptResponse, error)

	EstimateGasNoResolve(ctx context.Context, tx *ethsigner.Transaction, opts ...CallOption) (res EstimateGasResult, err error)
	CallContractNoResolve(ctx context.Context, tx *ethsigner.Transaction, block string, opts ...CallOption) (res CallResult, err error)
	GetTransactionCount(ctx context.Context, fromAddr tktypes.EthAddress) (transactionCount *tktypes.HexUint64, err error)
	SendRawTransaction(ctx context.Context, rawTX tktypes.HexBytes) (*tktypes.Bytes32, error)
}

// Higher level client interface to the base Ethereum ledger for TX submission.
// Not used by Paladin as we have the publicTxMgr
type EthClientWithKeyManager interface {
	EthClient

	ABI(ctx context.Context, a abi.ABI) (ABIClient, error)
	ABIJSON(ctx context.Context, abiJson []byte) (ABIClient, error)
	ABIFunction(ctx context.Context, functionABI *abi.Entry) (_ ABIFunctionClient, err error)
	ABIConstructor(ctx context.Context, constructorABI *abi.Entry, bytecode tktypes.HexBytes) (_ ABIFunctionClient, err error)
	MustABIJSON(abiJson []byte) ABIClient

	// Below are raw functions that the ABI() above provides wrappers for
	CallContract(ctx context.Context, from *string, tx *ethsigner.Transaction, block string, opts ...CallOption) (res CallResult, err error)
	EstimateGas(ctx context.Context, from *string, tx *ethsigner.Transaction, opts ...CallOption) (res EstimateGasResult, err error)
	BuildRawTransaction(ctx context.Context, txVersion EthTXVersion, from string, tx *ethsigner.Transaction, opts ...CallOption) (tktypes.HexBytes, error)
}

// Call options affect the behavior of gas estimate and call functions, such as by allowing you to supply
// an ABI for the client to use to decode the error data.
type CallOption interface {
	isCallOptions()
}

type callOptions struct {
	errABI     abi.ABI
	outputs    abi.TypeComponent
	serializer *abi.Serializer
}

func (co *callOptions) isCallOptions() {}

// The supplied ABI will be used when attempting to process revert data (if available)
func WithErrorsFrom(a abi.ABI) CallOption {
	return &callOptions{
		errABI: a,
	}
}

// The supplied function definition will be used to decode return data
func WithOutputs(outputs abi.TypeComponent) CallOption {
	return &callOptions{
		outputs: outputs,
	}
}

// The supplied function definition will be used to decode return data
func WithSerializer(serializer *abi.Serializer) CallOption {
	return &callOptions{
		serializer: serializer,
	}
}

type EstimateGasResult struct {
	GasLimit   tktypes.HexUint64
	RevertData tktypes.HexBytes
}

type CallResult struct {
	serializer    *abi.Serializer
	Data          tktypes.HexBytes
	DecodedResult *abi.ComponentValue
	RevertData    tktypes.HexBytes
}

// Convenience func that bypasses errors and uses the serializer provided
func (cr CallResult) JSON() (s string) {
	if cr.DecodedResult != nil {
		serializer := cr.serializer
		if serializer == nil {
			serializer = tktypes.StandardABISerializer()
		}
		b, _ := serializer.SerializeJSON(cr.DecodedResult)
		if b != nil {
			s = string(b)
		}
	}
	return s
}

type KeyManager interface {
	AddInMemorySigner(prefix string, signer signerapi.InMemorySigner) // should only be called on initialization routine
	ResolveKey(ctx context.Context, identifier, algorithm, verifierType string) (keyHandle, verifier string, err error)
	Sign(ctx context.Context, req *prototk.SignWithKeyRequest) (*prototk.SignWithKeyResponse, error)
	Close()
}

type ethClient struct {
	chainID           int64
	gasEstimateFactor float64
	rpc               rpcclient.Client
	keymgr            KeyManager
}

// A direct creation of a dedicated RPC client for things like unit tests outside of Paladin.
// Within Paladin, use the EthClientFactory instead as passed to your component/manager/engine via the initialization
func WrapRPCClient(ctx context.Context, keymgr KeyManager, rpc rpcclient.Client, conf *pldconf.EthClientConfig) (EthClient, error) {
	ec := &ethClient{
		keymgr:            keymgr,
		rpc:               rpc,
		gasEstimateFactor: confutil.Float64Min(conf.EstimateGasFactor, 1.0, *pldconf.EthClientDefaults.EstimateGasFactor),
	}
	if err := ec.setupChainID(ctx); err != nil {
		return nil, err
	}
	return ec, nil
}

// This is useful in cases where the RPC client is used only for ABI formatting.
// All JSON/RPC requests will fail, and there is no chain ID available
func NewUnconnectedRPCClient(ctx context.Context, conf *pldconf.EthClientConfig, chainID int64) EthClient {
	return &ethClient{
		rpc:               &unconnectedRPC{},
		gasEstimateFactor: confutil.Float64Min(conf.EstimateGasFactor, 1.0, *pldconf.EthClientDefaults.EstimateGasFactor),
		chainID:           chainID,
	}
}

type unconnectedRPC struct{}

func (u *unconnectedRPC) CallRPC(ctx context.Context, result interface{}, method string, params ...interface{}) rpcclient.ErrorRPC {
	return rpcclient.NewRPCError(ctx, rpcclient.RPCCodeInternalError, msgs.MsgEthClientNoConnection)
}

func (ec *ethClient) Close() {
	wsRPC, isWS := ec.rpc.(rpcclient.WSClient)
	if isWS {
		wsRPC.Close()
	}
}

func (ec *ethClient) ChainID() int64 {
	return ec.chainID
}

func (ec *ethClient) setupChainID(ctx context.Context) error {
	var chainID ethtypes.HexUint64
	if rpcErr := ec.rpc.CallRPC(ctx, &chainID, "eth_chainId"); rpcErr != nil {
		log.L(ctx).Errorf("eth_chainId failed: %+v", rpcErr)
		return i18n.WrapError(ctx, rpcErr, msgs.MsgEthClientChainIDFailed)
	}
	ec.chainID = int64(chainID.Uint64())
	return nil
}

func (ec *ethClient) resolveFrom(ctx context.Context, from *string, tx *ethsigner.Transaction) (string, *tktypes.EthAddress, error) {
	if from != nil && *from != "" {
		var fromAddr *tktypes.EthAddress
		keyHandle, fromVerifier, err := ec.keymgr.ResolveKey(ctx, *from, algorithms.ECDSA_SECP256K1, verifiers.ETH_ADDRESS)
		if err == nil {
			fromAddr, err = tktypes.ParseEthAddress(fromVerifier)
		}
		if err != nil {
			return "", nil, err
		}
		tx.From = json.RawMessage(tktypes.JSONString(fromAddr))
		return keyHandle, fromAddr, nil
	}
	return "", nil, nil
}

func (ec *ethClient) CallContract(ctx context.Context, from *string, tx *ethsigner.Transaction, block string, opts ...CallOption) (res CallResult, err error) {
	if ec.keymgr == nil && from != nil && *from != "" {
		ethAddr, err := tktypes.ParseEthAddress(*from)
		if err != nil {
			return res, err
		}
		tx.From = json.RawMessage(tktypes.JSONString(ethAddr))
	} else {
		if _, _, err := ec.resolveFrom(ctx, from, tx); err != nil {
			return res, err
		}
	}
	return ec.CallContractNoResolve(ctx, tx, block, opts...)
}

func (ec *ethClient) CallContractNoResolve(ctx context.Context, tx *ethsigner.Transaction, block string, opts ...CallOption) (res CallResult, err error) {

	var outputs abi.TypeComponent
	errABI := abi.ABI{}
	for _, o := range opts {
		co := o.(*callOptions)
		if co.errABI != nil {
			errABI = co.errABI
		}
		if co.outputs != nil {
			outputs = co.outputs
		}
		if co.serializer != nil {
			res.serializer = co.serializer
		}
	}
	if err := ec.rpc.CallRPC(ctx, &res.Data, "eth_call", tx, block); err != nil {
		rpcErr := err.RPCError()
		log.L(ctx).Errorf("eth_call failed: %+v", rpcErr)
		if len(rpcErr.Data) != 0 {
			log.L(ctx).Debugf("Received error data in revert: %s", rpcErr.Data)
			_ = json.Unmarshal(rpcErr.Data.Bytes(), &res.RevertData)
			if len(res.RevertData) > 0 {
				errString, _ := errABI.ErrorStringCtx(ctx, res.RevertData)
				if errString == "" {
					errString = tktypes.HexBytes(res.RevertData).String()
				}
				return res, i18n.NewError(ctx, msgs.MsgEthClientCallReverted, errString)
			}
		}
		// Or fallback to whatever the error we got was
		return res, rpcErr
	}

	// See if we can decode the result
	if outputs != nil {
		res.DecodedResult, err = outputs.DecodeABIDataCtx(ctx, res.Data, 0)
	}
	return res, err

}

func (ec *ethClient) GetBalance(ctx context.Context, address tktypes.EthAddress, block string) (*tktypes.HexUint256, error) {
	var addressBalance tktypes.HexUint256

	if rpcErr := ec.rpc.CallRPC(ctx, &addressBalance, "eth_getBalance", address, block); rpcErr != nil {
		log.L(ctx).Errorf("eth_getBalance failed: %+v", rpcErr)
		return nil, rpcErr
	}
	return &addressBalance, nil
}

func (ec *ethClient) GasPrice(ctx context.Context) (*tktypes.HexUint256, error) {
	// currently only support London style gas price
	// For EIP1559, will need to add support for `eth_maxPriorityFeePerGas`
	var gasPrice tktypes.HexUint256

	if rpcErr := ec.rpc.CallRPC(ctx, &gasPrice, "eth_gasPrice"); rpcErr != nil {
		log.L(ctx).Errorf("eth_gasPrice failed: %+v", rpcErr)
		return nil, rpcErr
	}
	return &gasPrice, nil
}

func (ec *ethClient) GetTransactionReceipt(ctx context.Context, txHash string) (*TransactionReceiptResponse, error) {

	// Get the receipt in the back-end JSON/RPC format
	var ethReceipt *txReceiptJSONRPC
	rpcErr := ec.rpc.CallRPC(ctx, &ethReceipt, "eth_getTransactionReceipt", txHash)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if ethReceipt == nil {
		return nil, i18n.NewError(ctx, msgs.MsgEthClientReceiptNotAvailable, txHash)
	}
	isSuccess := (ethReceipt.Status != nil && ethReceipt.Status.BigInt().Int64() > 0)

	var returnDataString *string
	var transactionErrorMessage *string

	if !isSuccess {
		returnDataString, transactionErrorMessage = ec.getErrorInfo(ctx, ethReceipt.RevertReason)
	}

	fullReceipt, _ := json.Marshal(&receiptExtraInfo{
		ContractAddress:   ethReceipt.ContractAddress,
		CumulativeGasUsed: (*fftypes.FFBigInt)(ethReceipt.CumulativeGasUsed),
		From:              ethReceipt.From,
		To:                ethReceipt.To,
		GasUsed:           (*fftypes.FFBigInt)(ethReceipt.GasUsed),
		Status:            (*fftypes.FFBigInt)(ethReceipt.Status),
		ReturnValue:       returnDataString,
		ErrorMessage:      transactionErrorMessage,
	})

	var txIndex int64
	if ethReceipt.TransactionIndex != nil {
		txIndex = ethReceipt.TransactionIndex.BigInt().Int64()
	}
	receiptResponse := &TransactionReceiptResponse{
		BlockNumber:      (*fftypes.FFBigInt)(ethReceipt.BlockNumber),
		TransactionIndex: fftypes.NewFFBigInt(txIndex),
		BlockHash:        ethReceipt.BlockHash.String(),
		Success:          isSuccess,
		ProtocolID:       ProtocolIDForReceipt((*fftypes.FFBigInt)(ethReceipt.BlockNumber), fftypes.NewFFBigInt(txIndex)),
		ExtraInfo:        fftypes.JSONAnyPtrBytes(fullReceipt),
	}

	if ethReceipt.ContractAddress != nil {
		location, _ := json.Marshal(map[string]string{
			"address": ethReceipt.ContractAddress.String(),
		})
		receiptResponse.ContractLocation = fftypes.JSONAnyPtrBytes(location)
	}
	return receiptResponse, nil
}

func (ec *ethClient) EstimateGas(ctx context.Context, from *string, tx *ethsigner.Transaction, opts ...CallOption) (res EstimateGasResult, err error) {
	if _, _, err := ec.resolveFrom(ctx, from, tx); err != nil {
		return res, err
	}
	return ec.EstimateGasNoResolve(ctx, tx, opts...)
}

func (ec *ethClient) EstimateGasNoResolve(ctx context.Context, tx *ethsigner.Transaction, opts ...CallOption) (res EstimateGasResult, err error) {
	if err = ec.rpc.CallRPC(ctx, &res.GasLimit, "eth_estimateGas", tx); err != nil {
		log.L(ctx).Errorf("eth_estimateGas failed: %+v", err)
		// Fall back to a call, to see if we can get an error
		callRes, callErr := ec.CallContractNoResolve(ctx, tx, "latest", opts...)
		if callErr != nil {
			err = callErr
		}
		res.RevertData = callRes.RevertData
		return res, err
	}
	return res, nil
}

func (ec *ethClient) GetTransactionCount(ctx context.Context, fromAddr tktypes.EthAddress) (*tktypes.HexUint64, error) {
	var transactionCount tktypes.HexUint64
	if rpcErr := ec.rpc.CallRPC(ctx, &transactionCount, "eth_getTransactionCount", fromAddr, "latest"); rpcErr != nil {
		log.L(ctx).Errorf("eth_getTransactionCount(%s) failed: %+v", fromAddr, rpcErr)
		return nil, rpcErr
	}
	return &transactionCount, nil
}

func (ec *ethClient) BuildRawTransaction(ctx context.Context, txVersion EthTXVersion, from string, tx *ethsigner.Transaction, opts ...CallOption) (tktypes.HexBytes, error) {
	keyHandle, fromAddr, err := ec.resolveFrom(ctx, &from, tx)
	if err != nil {
		return nil, err
	}

	// Trivial nonce management in the client - just get the current nonce for this key, from the local node mempool, for each TX
	if tx.Nonce == nil {
		txNonce, err := ec.GetTransactionCount(ctx, *fromAddr)
		if err != nil {
			log.L(ctx).Errorf("eth_getTransactionCount(%s) failed: %+v", keyHandle, err)
			return nil, err
		}
		tx.Nonce = ethtypes.NewHexInteger(big.NewInt(int64(txNonce.Uint64())))
	}

	if tx.GasLimit == nil {
		// Estimate gas before submission
		gasEstimate, err := ec.EstimateGasNoResolve(ctx, tx, opts...)
		if err != nil {
			log.L(ctx).Errorf("eth_estimateGas failed: %+v", err)
			return nil, err
		}
		// If that went well, so submission with a bump on the estimation
		factoredGasLimit := int64((float64)(gasEstimate.GasLimit) * ec.gasEstimateFactor)
		tx.GasLimit = (*ethtypes.HexInteger)(big.NewInt(factoredGasLimit))
	}

	// Sign
	var sigPayload *ethsigner.TransactionSignaturePayload
	switch txVersion {
	case EIP1559:
		sigPayload = tx.SignaturePayloadEIP1559(ec.chainID)
	case LEGACY_EIP155:
		sigPayload = tx.SignaturePayloadLegacyEIP155(ec.chainID)
	case LEGACY_ORIGINAL:
		sigPayload = tx.SignaturePayloadLegacyOriginal()
	default:
		return nil, i18n.NewError(ctx, msgs.MsgEthClientInvalidTXVersion, txVersion)
	}
	hash := sha3.NewLegacyKeccak256()
	_, _ = hash.Write(sigPayload.Bytes())
	signature, err := ec.keymgr.Sign(ctx, &prototk.SignWithKeyRequest{
		Algorithm:   algorithms.ECDSA_SECP256K1,
		PayloadType: signpayloads.OPAQUE_TO_RSV,
		KeyHandle:   keyHandle,
		Payload:     tktypes.HexBytes(hash.Sum(nil)),
	})
	var sig *secp256k1.SignatureData
	if err == nil {
		sig, err = secp256k1.DecodeCompactRSV(ctx, signature.Payload)
	}
	var rawTX []byte
	if err == nil {
		switch txVersion {
		case EIP1559:
			rawTX, err = tx.FinalizeEIP1559WithSignature(sigPayload, sig)
		case LEGACY_EIP155:
			rawTX, err = tx.FinalizeLegacyEIP155WithSignature(sigPayload, sig, ec.chainID)
		case LEGACY_ORIGINAL:
			rawTX, err = tx.FinalizeLegacyOriginalWithSignature(sigPayload, sig)
		}
	}
	if err != nil {
		log.L(ctx).Errorf("signing failed with keyHandle %s (addr=%s): %s", keyHandle, fromAddr, err)
		return nil, err
	}
	return rawTX, nil
}

func (ec *ethClient) SendRawTransaction(ctx context.Context, rawTX tktypes.HexBytes) (*tktypes.Bytes32, error) {

	// Submit
	var txHash tktypes.Bytes32
	if rpcErr := ec.rpc.CallRPC(ctx, &txHash, "eth_sendRawTransaction", tktypes.HexBytes(rawTX)); rpcErr != nil {
		addr, decodedTX, err := ethsigner.RecoverRawTransaction(ctx, ethtypes.HexBytes0xPrefix(rawTX), ec.chainID)
		if err != nil {
			log.L(ctx).Errorf("Invalid transaction build during signing: %s", err)
		} else {
			log.L(ctx).Errorf("Rejected TX (from=%s): %+v", addr, logJSON(decodedTX.Transaction))
		}
		return nil, fmt.Errorf("eth_sendRawTransaction failed: %+v", rpcErr)
	}

	// We just return the hash here - see blockindexer.BlockIndexer
	// - to wait for completion see BlockIndexer.WaitForTransaction()
	// - to query events for that completed transaction see BlockIndexer.ListTransactionEvents()
	// - to stream events in order (whether you submitted them or not) see BlockIndexer.TODO()
	return &txHash, nil
}

func logJSON(v interface{}) string {
	ret := ""
	b, _ := json.Marshal(v)
	if len(b) > 0 {
		ret = (string)(b)
	}
	return ret
}

func (ec *ethClient) getErrorInfo(ctx context.Context, revertFromReceipt *ethtypes.HexBytes0xPrefix) (pReturnValue *string, pErrorMessage *string) {

	var revertReason string
	if revertFromReceipt != nil {
		revertReason = revertFromReceipt.String()
	}

	// See if the return value is using the default error you get from "revert"
	var errorMessage string
	returnDataBytes, _ := hex.DecodeString(strings.TrimPrefix(revertReason, "0x"))
	if len(returnDataBytes) > 4 && bytes.Equal(returnDataBytes[0:4], defaultErrorID) {
		value, err := defaultError.DecodeCallDataCtx(ctx, returnDataBytes)
		if err == nil {
			errorMessage = value.Children[0].Value.(string)
		}
	}

	// Otherwise we can't decode it, so put it directly in the error
	if errorMessage == "" {
		if len(returnDataBytes) > 0 {
			errorMessage = i18n.NewError(ctx, msgs.MsgEthClientReturnValueNotDecoded, revertReason).Error()
		} else {
			errorMessage = i18n.NewError(ctx, msgs.MsgEthClientReturnValueNotAvailable).Error()
		}
	}
	return &revertReason, &errorMessage
}
