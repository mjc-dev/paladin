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

package io.kaleido.paladin.toolkit;

import io.kaleido.paladin.toolkit.Service;
import org.apache.logging.log4j.LogManager;
import org.apache.logging.log4j.Logger;

import java.util.HashMap;
import java.util.Map;

public abstract class DomainBase extends PluginBase<Service.DomainMessage> {
    private static final Logger LOGGER = LogManager.getLogger(DomainBase.class);

    protected abstract DomainInstance newDomainInstance(String grpcTarget, String instanceId);

    @Override
    final PluginInstance<Service.DomainMessage> newPluginInstance(String grpcTarget, String instanceId) {
        LOGGER.info("Starting new domain instance {} connecting to {}", instanceId, grpcTarget);
        return newDomainInstance(grpcTarget, instanceId);
    }
}