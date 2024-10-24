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

package privatetxnmgr

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/hyperledger/firefly-common/pkg/i18n"
	"github.com/kaleido-io/paladin/core/internal/msgs"
	"github.com/kaleido-io/paladin/toolkit/pkg/log"
	"github.com/kaleido-io/paladin/toolkit/pkg/pldapi"
	"github.com/kaleido-io/paladin/toolkit/pkg/prototk"
	"github.com/kaleido-io/paladin/toolkit/pkg/tktypes"
)

func (tf *transactionFlow) Action(ctx context.Context) {
	log.L(ctx).Debug("transactionFlow:Action")
	if tf.complete {
		log.L(ctx).Infof("Transaction %s is complete", tf.transaction.ID.String())
		return
	}

	if tf.dispatched {
		log.L(ctx).Infof("Transaction %s is dispatched", tf.transaction.ID.String())
		return
	}

	// Lets get the nasty stuff out of the way first
	// if the event handler has marked the transaction as failed, then we initiate the finalize sync point
	if tf.finalizeRequired {
		if tf.finalizePending {
			log.L(ctx).Infof("Transaction %s finalize already pending", tf.transaction.ID.String())
			return
		}
		//we know we need to finalize but we are not currently waiting for a finalize to complete
		// most likely a previous attempt to finalize has failed
		tf.finalize(ctx)
	}

	if tf.transaction.PreAssembly == nil {
		panic("PreAssembly is nil.")
		//This should never happen unless there is a serious programming error or the memory has been corrupted
		// PreAssembly is checked for nil after InitTransaction which is during the synchronous transaction request
		// and before it is added to the transaction processor / dispatched to the event loop
	}

	if tf.status == "delegating" {
		log.L(ctx).Infof("Transaction %s is delegating", tf.transaction.ID.String())
		return
	}

	if tf.status == "delegated" {
		// probably should not get here because the sequencer should have removed the transaction processor
		log.L(ctx).Infof("Transaction %s has been delegated", tf.transaction.ID.String())
		return
	}
	tf.delegateIfRequired(ctx)
	if tf.status == "delegating" {
		log.L(ctx).Infof("Transaction %s is delegating", tf.transaction.ID.String())
		return
	}

	if tf.status == "delegated" {
		// probably should not get here because the sequencer should have removed the transaction processor
		log.L(ctx).Infof("Transaction %s has been delegated", tf.transaction.ID.String())
		return
	}

	if tf.transaction.PostAssembly == nil {
		log.L(ctx).Debug("not assembled yet - or was assembled and reverted")

		//if we have not sent a request, or if the request has timed out or been invalidated by a re-assembly, then send the request
		tf.requestVerifierResolution(ctx)
		if tf.hasOutstandingVerifierRequests(ctx) {
			log.L(ctx).Infof("Transaction %s not ready to assemble. Waiting for verifiers to be resolved", tf.transaction.ID.String())
			return
		}

		tf.requestAssemble(ctx)
		if tf.transaction.PostAssembly == nil {
			log.L(ctx).Infof("Transaction %s not assembled. Waiting for assembler to return", tf.transaction.ID.String())
			return
		}
	}

	if tf.transaction.PostAssembly.OutputStatesPotential != nil && tf.transaction.PostAssembly.OutputStates == nil {
		// We need to write the potential states to the domain before we can sign or endorse the transaction
		// but there is no point in doing that until we are sure that the transaction is going to be coordinated locally
		// so this is the earliest, and latest, point in the flow that we can do this
		readTX := tf.components.Persistence().DB() // no DB transaction required here for the reads from the DB (writes happen on syncpoint flusher)
		err := tf.domainAPI.WritePotentialStates(tf.endorsementGatherer.DomainContext(), readTX, tf.transaction)
		if err != nil {
			//Any error from WritePotentialStates is likely to be caused by an invalid init or assemble of the transaction
			// which is most likely a programming error in the domain or the domain manager or privateTxManager
			// not much we can do other than revert the transaction with an internal error
			errorMessage := fmt.Sprintf("Failed to write potential states: %s", err)
			log.L(ctx).Error(errorMessage)
			//TODO publish an event that will cause the transaction to be reverted
			//tf.revertTransaction(ctx, i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerInternalError), errorMessage))
			return
		}
	}
	log.L(ctx).Debugf("Transaction %s is ready (outputStatesPotential=%d outputStates=%d)",
		tf.transaction.ID.String(), len(tf.transaction.PostAssembly.OutputStatesPotential), len(tf.transaction.PostAssembly.OutputStates))
	tf.readyForSequencing = true

	//If we get here, we have an assembled transaction and have no intention of delegating it
	// so we are responsible for coordinating the endorsement flow

	// either because it was submitted locally and we decided not to delegate or because it was delegated to us
	// start with fulfilling any outstanding signature requests
	tf.requestSignatures(ctx)
	if tf.hasOutstandingSignatureRequests() {
		return
	}
	tf.status = "signed"

	tf.requestEndorsements(ctx)
	if tf.hasOutstandingEndorsementRequests(ctx) {
		return
	}
	tf.status = "endorsed"

	// TODO is this too late to be resolving the dispatch key?
	// Can we do it any earlier or do we need to wait until we have all endorsements ( i.e. so that the endorser can declare ENDORSER_MUST_SUBMIT)
	// We would need to do it earlier if we want to avoid transactions for different dispatch keys ending up in the same dependency graph
	if tf.transaction.Signer == "" {
		err := tf.domainAPI.ResolveDispatch(ctx, tf.transaction)
		if err != nil {

			log.L(ctx).Errorf("Failed to resolve dispatch for transaction %s: %s", tf.transaction.ID.String(), err)
			tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerResolveDispatchError), err.Error())

			//TODO as it stands, we will just enter a retry loop of trying to resolve the dispatcher next time the event loop triggers an action
			// if we are lucky, that will be triggered by an event that somehow changes the in memory state in a way that the dispatcher can be
			// resolved but that is unlikely
			// would it be more appropriate to re-assemble ( or even revert ) the transaction here?
			return
		}
	}
}

func (tf *transactionFlow) revertTransaction(ctx context.Context, revertReason string) {
	log.L(ctx).Errorf("Reverting transaction %s: %s", tf.transaction.ID.String(), revertReason)
	//trigger a finalize and update the transaction state so that finalize can be retried if it fails
	tf.finalizeRequired = true
	tf.finalizePending = true
	tf.finalizeReason = revertReason
	tf.finalize(ctx)

}

func (tf *transactionFlow) finalize(ctx context.Context) {
	log.L(ctx).Errorf("finalize transaction %s: %s", tf.transaction.ID.String(), tf.finalizeReason)
	//flush that to the txmgr database
	// so that the user can see that it is reverted and so that we stop retrying to assemble and endorse it

	tf.syncPoints.QueueTransactionFinalize(
		ctx,
		tf.domainAPI.Address(),
		tf.transaction.ID,
		tf.finalizeReason,
		func(ctx context.Context) {
			//we are not on the main event loop thread so can't update in memory state here.
			// need to go back into the event loop
			log.L(ctx).Infof("Transaction %s finalize committed", tf.transaction.ID.String())
			go tf.publisher.PublishTransactionFinalizedEvent(ctx, tf.transaction.ID.String())
		},
		func(ctx context.Context, rollbackErr error) {
			//we are not on the main event loop thread so can't update in memory state here.
			// need to go back into the event loop
			log.L(ctx).Errorf("Transaction %s finalize rolled back: %s", tf.transaction.ID.String(), rollbackErr)
			go tf.publisher.PublishTransactionFinalizeError(ctx, tf.transaction.ID.String(), tf.finalizeReason, rollbackErr)
		},
	)
}

func (tf *transactionFlow) delegateIfRequired(ctx context.Context) {
	//TODO there may be a potential optimization we can add where, in certain domain configurations, we can optimistically proceed without delegation and only delegate once we detect
	// other active nodes.  For now, we keep it simple and strictly abide by the configuration of the domain
	log.L(ctx).Debug("transactionFlow:delegateIfRequired")
	coordinatorNode := tf.selectCoordinator(ctx)

	tf.localCoordinator = false
	// TODO persist the delegation and send the request on the callback
	if coordinatorNode == tf.nodeName || coordinatorNode == "" {
		return
	}
	//TODO if already `delegating` check how long we have been waiting for the ack and send again.
	//Should probably do that earlier in the flow because if we have just decided not to delegate or if we have just selected a different delegate, \
	//then we need to either claw back that delegation or wait until the delegate has realized that they are no longer the coordinator and returns / forwards the responsibility for this transaction
	tf.status = "delegating"

	// TODO update to "delegated" once the ack has been received
	err := tf.transportWriter.SendDelegationRequest(
		ctx,
		uuid.New().String(),
		coordinatorNode,
		tf.transaction,
	)
	if err != nil {
		tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerInternalError), err.Error())
	}

}

func (tf *transactionFlow) requestAssemble(ctx context.Context) {
	//Assemble may require a call to another node ( in the case we have been delegated to coordinate transaction for other nodes)
	//Usually, they will get sent to us already assembled but there may be cases where we need to re-assemble
	// so this needs to be an async step
	// however, there must be only one assemble in progress at a time or else there is a risk that 2 transactions could chose to spend the same state
	//   (TODO - maybe in future, we could further optimise this and allow multiple assembles to be in progress if we can assert that they are not presented with the same available states)
	//   However, before we do that, we really need to sort out the separation of concerns between the domain manager, state store and private transaction manager and where the responsibility to single thread the assembly stream(s) lies

	log.L(ctx).Debug("transactionFlow:requestAssemble")

	if tf.transaction.PostAssembly != nil {
		log.L(ctx).Debug("already assembled")
		return
	}

	assemblingNode, err := tktypes.PrivateIdentityLocator(tf.transaction.Inputs.From).Node(ctx, true)
	if err != nil {

		log.L(ctx).Errorf("Failed to get node name from locator %s: %s", tf.transaction.Inputs.From, err)
		tf.publisher.PublishTransactionAssembleFailedEvent(
			ctx,
			tf.transaction.ID.String(),
			i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerInternalError), "Failed to get node name from locator"),
		)
		return
	}

	//TODO call tf.assembleRequester.RequestAssemble(ctx, assemblingNode ....// figure out what to pass )

}

func (tf *transactionFlow) requestSignature(ctx context.Context, attRequest *prototk.AttestationRequest, partyName string) {

	keyMgr := tf.components.KeyManager()
	unqualifiedLookup, err := tktypes.PrivateIdentityLocator(partyName).Identity(ctx)
	var resolvedKey *pldapi.KeyMappingAndVerifier
	if err == nil {
		resolvedKey, err = keyMgr.ResolveKeyNewDatabaseTX(ctx, unqualifiedLookup, attRequest.Algorithm, attRequest.VerifierType)
	}
	if err != nil {
		log.L(ctx).Errorf("Failed to resolve local signer for %s (algorithm=%s): %s", partyName, attRequest.Algorithm, err)
		tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerResolveError), partyName, attRequest.Algorithm, err.Error())
		return
	}
	// TODO this could be calling out to a remote signer, should we be doing these in parallel?
	signaturePayload, err := keyMgr.Sign(ctx, resolvedKey, attRequest.PayloadType, attRequest.Payload)
	if err != nil {
		log.L(ctx).Errorf("failed to sign for party %s (verifier=%s,algorithm=%s): %s", partyName, resolvedKey.Verifier.Verifier, attRequest.Algorithm, err)
		tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerSignError), partyName, resolvedKey.Verifier.Verifier, attRequest.Algorithm, err.Error())
		return
	}
	log.L(ctx).Debugf("payload: %x signed %x by %s (%s)", attRequest.Payload, signaturePayload, partyName, resolvedKey.Verifier.Verifier)

	tf.publisher.PublishTransactionSignedEvent(ctx,
		tf.transaction.ID.String(),
		&prototk.AttestationResult{
			Name:            attRequest.Name,
			AttestationType: attRequest.AttestationType,
			Verifier: &prototk.ResolvedVerifier{
				Lookup:       partyName,
				Algorithm:    attRequest.Algorithm,
				Verifier:     resolvedKey.Verifier.Verifier,
				VerifierType: attRequest.VerifierType,
			},
			Payload:     signaturePayload,
			PayloadType: &attRequest.PayloadType,
		},
	)
}

func (tf *transactionFlow) requestSignatures(ctx context.Context) {

	if tf.requestedSignatures {
		return
	}
	if tf.transaction.PostAssembly.Signatures == nil {
		tf.transaction.PostAssembly.Signatures = make([]*prototk.AttestationResult, 0)
	}
	attPlan := tf.transaction.PostAssembly.AttestationPlan
	attResults := tf.transaction.PostAssembly.Endorsements

	for _, attRequest := range attPlan {
		switch attRequest.AttestationType {
		case prototk.AttestationType_SIGN:
			toBeComplete := true
			for _, ar := range attResults {
				if ar.GetAttestationType().Type() == attRequest.GetAttestationType().Type() {
					toBeComplete = false
					break
				}
			}
			if toBeComplete {

				for _, partyName := range attRequest.Parties {
					go tf.requestSignature(ctx, attRequest, partyName)
				}
			}
		}
	}
	tf.requestedSignatures = true
}

func (tf *transactionFlow) requestEndorsement(ctx context.Context, party string, attRequest *prototk.AttestationRequest) {

	partyLocator := tktypes.PrivateIdentityLocator(party)
	partyNode, err := partyLocator.Node(ctx, true)
	if err != nil {
		log.L(ctx).Errorf("Failed to get node name from locator %s: %s", party, err)
		tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerInternalError), err.Error())
		return
	}

	if partyNode == tf.nodeName || partyNode == "" {
		// This is a local party, so we can endorse it directly
		endorsement, revertReason, err := tf.endorsementGatherer.GatherEndorsement(
			ctx,
			tf.transaction.PreAssembly.TransactionSpecification,
			tf.transaction.PreAssembly.Verifiers,
			tf.transaction.PostAssembly.Signatures,
			toEndorsableList(tf.transaction.PostAssembly.InputStates),
			toEndorsableList(tf.transaction.PostAssembly.ReadStates),
			toEndorsableList(tf.transaction.PostAssembly.OutputStates),
			party,
			attRequest)
		if err != nil {
			log.L(ctx).Errorf("Failed to gather endorsement for party %s: %s", party, err)
			tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerInternalError), err.Error())
			return

		}
		tf.publisher.PublishTransactionEndorsedEvent(ctx,
			tf.transaction.ID.String(),
			endorsement,
			revertReason,
		)

	} else {
		// This is a remote party, so we need to send an endorsement request to the remote node

		err = tf.transportWriter.SendEndorsementRequest(
			ctx,
			party,
			partyNode,
			tf.transaction.Inputs.To.String(),
			tf.transaction.ID.String(),
			attRequest,
			tf.transaction.PreAssembly.TransactionSpecification,
			tf.transaction.PreAssembly.Verifiers,
			tf.transaction.PostAssembly.Signatures,
			tf.transaction.PostAssembly.InputStates,
			tf.transaction.PostAssembly.OutputStates,
		)
		if err != nil {
			log.L(ctx).Errorf("Failed to send endorsement request to party %s: %s", party, err)
			tf.latestError = i18n.ExpandWithCode(ctx, i18n.MessageKey(msgs.MsgPrivateTxManagerEndorsementRequestError), party, err.Error())
		}
	}
}

func (tf *transactionFlow) requestEndorsements(ctx context.Context) {
	for _, outstandingEndorsementRequest := range tf.outstandingEndorsementRequests(ctx) {
		// there is a request in the attestation plan and we do not have a response to match it
		// first lets see if we have recently sent a request for this endorsement and just need to be patient
		previousRequestTime := time.Time{}
		if timesForAttRequest, ok := tf.requestedEndorsementTimes[outstandingEndorsementRequest.attRequest.Name]; ok {
			if t, ok := timesForAttRequest[outstandingEndorsementRequest.party]; ok {
				previousRequestTime = t
			}
		} else {
			tf.requestedEndorsementTimes[outstandingEndorsementRequest.attRequest.Name] = make(map[string]time.Time)
		}

		if !previousRequestTime.IsZero() && tf.clock.Now().Before(previousRequestTime.Add(tf.requestTimeout)) {
			//We have already sent a message for this request and the deadline has not passed
			log.L(ctx).Debugf("Transaction %s endorsement already requested %v", tf.transaction.ID.String(), previousRequestTime)
			return
		}
		if previousRequestTime.IsZero() {
			log.L(ctx).Infof("Transaction %s endorsement has never been requested for attestation request:%s, party:%s", tf.transaction.ID.String(), outstandingEndorsementRequest.attRequest.Name, outstandingEndorsementRequest.party)
		} else {
			log.L(ctx).Infof("Previous endorsement request for transaction:%s, attestation request:%s, party:%s sent at %v has timed out", tf.transaction.ID.String(), outstandingEndorsementRequest.attRequest.Name, outstandingEndorsementRequest.party, previousRequestTime)
		}
		tf.requestEndorsement(ctx, outstandingEndorsementRequest.party, outstandingEndorsementRequest.attRequest)
		tf.requestedEndorsementTimes[outstandingEndorsementRequest.attRequest.Name][outstandingEndorsementRequest.party] = tf.clock.Now()

	}
}

func (tf *transactionFlow) requestVerifierResolution(ctx context.Context) {

	if tf.requestedVerifierResolution {
		log.L(ctx).Infof("Transaction %s verifier resolution already requested", tf.transaction.ID.String())
		return
	}

	//TODO keep track of previous requests and send out new requests if previous ones have timed out
	if tf.transaction.PreAssembly.Verifiers == nil {
		tf.transaction.PreAssembly.Verifiers = make([]*prototk.ResolvedVerifier, 0, len(tf.transaction.PreAssembly.RequiredVerifiers))
	}
	for _, v := range tf.transaction.PreAssembly.RequiredVerifiers {
		tf.identityResolver.ResolveVerifierAsync(
			ctx,
			v.Lookup,
			v.Algorithm,
			v.VerifierType,
			func(ctx context.Context, verifier string) {
				//response event needs to be handled by the sequencer so that the dispatch to a handling thread is done in fairness to all other in flight transactions
				tf.publisher.PublishResolveVerifierResponseEvent(ctx, tf.transaction.ID.String(), v.Lookup, v.Algorithm, verifier, v.VerifierType)
			},
			func(ctx context.Context, err error) {
				tf.publisher.PublishResolveVerifierErrorEvent(ctx, tf.transaction.ID.String(), v.Lookup, v.Algorithm, err.Error())
			},
		)
	}
	//TODO this needs to be more precise (like which verifiers have been sent / pending / stale  etc)
	tf.requestedVerifierResolution = true
}
