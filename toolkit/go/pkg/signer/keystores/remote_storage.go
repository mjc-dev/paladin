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

package keystores

import (
	"context"
	"fmt"

	"github.com/kaleido-io/paladin/config/pkg/pldconf"
	"github.com/kaleido-io/paladin/toolkit/pkg/log"
	"github.com/kaleido-io/paladin/toolkit/pkg/pldclient"
	"github.com/kaleido-io/paladin/toolkit/pkg/signerapi"
)

type remoteStoreFactory struct{}

type remoteStore struct {
	config     *pldconf.RemoteSignerConfig
	ctx        context.Context
	httpClient pldclient.PaladinClient
	wsClient   pldclient.PaladinWSClient
}

func NewRemoteStoreFactory() signerapi.KeyStoreFactory[*pldconf.RemoteSignerConfig] {
	return &remoteStoreFactory{}
}

func (rsf *remoteStoreFactory) NewKeyStore(ctx context.Context, eConf *pldconf.RemoteSignerConfig) (signerapi.KeyStore, error) {
	store := &remoteStore{
		config: eConf,
		ctx:    ctx,
	}

	if eConf.HTTPConfig != nil {
		httpClient, err := pldclient.New().HTTP(ctx, eConf.HTTPConfig)
		// TODO better error
		if err != nil {
			return nil, err
		}

		// rpc client and websockets separate?
		store.httpClient = httpClient
	}

	if eConf.WSConfig != nil {
		wsClient, err := store.httpClient.WebSocket(ctx, eConf.WSConfig)
		// TODO better error
		if err != nil {
			return nil, err
		}
		// rpc client and websockets separate?
		store.wsClient = wsClient
	}

	return store, nil
}

func (rks *remoteStore) FindOrCreateLoadableKey(ctx context.Context, req *signerapi.ResolveKeyRequest, newKeyMaterial func() ([]byte, error)) (keyMaterial []byte, keyHandle string, err error) {
	// TODO unsupported
	return nil, "", nil

}

func (rks *remoteStore) LoadKeyMaterial(ctx context.Context, keyHandle string) ([]byte, error) {
	// TODO unsupported
	return nil, nil
}

// Remote keystore
func (rks *remoteStore) FindOrCreateInStoreSigningKey(ctx context.Context, req *signerapi.ResolveKeyRequest) (res *signerapi.ResolveKeyResponse, err error) {
	remoteResolveResponse := &signerapi.ResolveKeyResponse{}

	if rpcErr := rks.httpClient.CallRPC(ctx, remoteResolveResponse, "rks_resolve", req); rpcErr != nil {
		log.L(ctx).Errorf("Remote key store JSON-RPC 'rks_resolve' error: %s", rpcErr)
		return nil, fmt.Errorf("Remote module JSON-RPC 'rsm_resolve' error: %s", rpcErr)
	}

	// TODO ws

	return remoteResolveResponse, nil
}

func (rks *remoteStore) SignWithinKeystore(ctx context.Context, req *signerapi.SignRequest) (res *signerapi.SignResponse, err error) {
	remoteSignResponse := &signerapi.SignResponse{}

	if rpcErr := rks.httpClient.CallRPC(ctx, remoteSignResponse, "rks_sign", req); rpcErr != nil {
		log.L(ctx).Errorf("Remote key store JSON-RPC 'rks_sign' error: %s", rpcErr)
		return nil, fmt.Errorf("Remote module JSON-RPC 'rks_sign' error: %s", rpcErr)
	}

	// TODO ws

	return remoteSignResponse, nil
}

func (rks *remoteStore) Close() {
	// TODO tear down clients
}
