/*
 * Copyright © 2024 Kaleido, Inc.
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
package transaction

import (
	"context"

	"github.com/hyperledger/firefly-common/pkg/i18n"
	"github.com/hyperledger/firefly-common/pkg/log"
	"github.com/kaleido-io/paladin/kata/internal/msgs"
	"github.com/kaleido-io/paladin/kata/pkg/proto"
)

func (s *PaladinTransactionService) submit(ctx context.Context, req *proto.SubmitTransactionRequest) (*proto.SubmitTransactionResponse, error) {
	// TODO: Implement the logic to submit a transaction
	// You can access the request fields using req.contractAddress, req.from, req.idempotencyKey, and req.payload
	// You can create a new transaction ID using a UUID library or any other method you prefer
	// You can return the transaction ID in the response using &SubmitTransactionResponse{transactionId: "your-transaction-id"}

	log.L(ctx).Infof("Received SubmitTransactionRequest: contractAddress=%s, from=%s, idempotencyKey=%s, payload=%s", req.ContractAddress, req.From, req.IdempotencyKey, req.Payload)

	// High level checks goes into here:

	payload := req.GetPayloadJSON()
	if payload == "" {
		payload = req.GetPayloadRLP()
	}

	if payload == "" || req.From == "" || req.ContractAddress == "" {
		missingFields := make([]string, 4)
		if payload == "" {
			missingFields = append(missingFields, "PayloadJSON", "PayloadRLP")
		}
		if req.From == "" {
			missingFields = append(missingFields, "From")
		}
		if req.ContractAddress == "" {
			missingFields = append(missingFields, "payload")
		}
		return nil, i18n.NewError(ctx, msgs.MsgTransactionMissingField, missingFields)
	}

	// What happens next

	return &proto.SubmitTransactionResponse{
		TransactionId: "your-transaction-id",
	}, nil
}
