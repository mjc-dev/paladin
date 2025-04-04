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

package keymanager

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/kaleido-io/paladin/config/pkg/pldconf"
	"github.com/kaleido-io/paladin/core/internal/components"
	"github.com/kaleido-io/paladin/toolkit/pkg/log"
	"github.com/kaleido-io/paladin/toolkit/pkg/prototk"
	"github.com/kaleido-io/paladin/toolkit/pkg/retry"
	"github.com/kaleido-io/paladin/toolkit/pkg/signer"
	"github.com/kaleido-io/paladin/toolkit/pkg/signerapi"
)

// Plugin signing module
type signingModule struct {
	ctx       context.Context
	cancelCtx context.CancelFunc

	conf *pldconf.SigningModuleConfig
	km   *keyManager
	id   uuid.UUID
	name string
	api  components.KeyManagerToSigningModule

	initialized atomic.Bool
	initRetry   *retry.Retry

	initError atomic.Pointer[error]
	initDone  chan struct{}
}

func (km *keyManager) newSigningModule(id uuid.UUID, name string, conf *pldconf.SigningModuleConfig, toSigningModule components.KeyManagerToSigningModule) signer.SigningModule {
	sm := &signingModule{
		km:        km,
		conf:      conf,
		initRetry: retry.NewRetryIndefinite(&conf.Init.Retry),
		name:      name,
		id:        id,
		api:       toSigningModule,
		initDone:  make(chan struct{}),
	}
	sm.ctx, sm.cancelCtx = context.WithCancel(log.WithLogField(km.bgCtx, "signingModule", sm.name))
	return sm
}

func (sm *signingModule) init() {
	defer close(sm.initDone)

	// We block retrying each part of init until we succeed, or are cancelled
	// (which the plugin manager will do if the signing module disconnects)
	err := sm.initRetry.Do(sm.ctx, func(attempt int) (bool, error) {
		// Send the configuration to the signing module for processing
		confJSON, _ := json.Marshal(&sm.conf.Config)
		_, err := sm.api.ConfigureSigningModule(sm.ctx, &prototk.ConfigureSigningModuleRequest{
			Name:       sm.name,
			ConfigJson: string(confJSON),
		})
		return true, err
	})
	if err != nil {
		log.L(sm.ctx).Debugf("signing module initialization cancelled before completion: %s", err)
		sm.initError.Store(&err)
	} else {
		log.L(sm.ctx).Debugf("signing module initialization complete %s", sm.name)
		sm.initialized.Store(true)
		// Inform the plugin manager callback
		sm.api.Initialized()

		// Now that the signing module plugin has been loaded, go and add any wallets that use it
		for _, walletConf := range sm.km.conf.Wallets {
			if walletConf.SignerType == pldconf.WalletSignerTypePlugin && walletConf.SignerPluginName == sm.name {
				sm.km.addWallet(walletConf)
			}
		}
	}
}

func (sm *signingModule) Resolve(ctx context.Context, req *prototk.ResolveKeyRequest) (res *prototk.ResolveKeyResponse, err error) {
	return sm.api.ResolveKey(ctx, req)
}

func (sm *signingModule) Sign(ctx context.Context, req *prototk.SignWithKeyRequest) (res *prototk.SignWithKeyResponse, err error) {
	return sm.api.Sign(ctx, req)
}

func (sm *signingModule) List(ctx context.Context, req *prototk.ListKeysRequest) (res *prototk.ListKeysResponse, err error) {
	return sm.api.ListKeys(ctx, req)
}

func (sm *signingModule) AddInMemorySigner(prefix string, signer signerapi.InMemorySigner) {
	_, _ = sm.api.AddInMemorySigner(sm.ctx, &prototk.AddInMemorySignerRequest{
		Prefix: prefix,
		//Signer: &signer, // TODO protobuf representation of a signer implementation
	})
}

func (sm *signingModule) Close() {
	sm.api.Close(sm.ctx, &prototk.CloseRequest{})
}

func (sm *signingModule) close() {
	sm.cancelCtx()
	<-sm.initDone
}
