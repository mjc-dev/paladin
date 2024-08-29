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

package zeto

type ZetoDomainConfig struct {
	DomainContracts ZetoDomainContracts `yaml:"contracts"`
}

type ZetoDomainContracts struct {
	Factory ZetoDomainContractFactory `yaml:"factory"`
}

type ZetoDomainContract struct {
	Name            string         `yaml:"name"`
	ContractAddress string         `yaml:"address"`
	AbiAndBytecode  AbiAndBytecode `yaml:"abiAndBytecode"`
	Libraries       []string       `yaml:"libraries"`
}

type ZetoDomainContractFactory struct {
	ContractAddress string               `yaml:"address"`
	AbiAndBytecode  AbiAndBytecode       `yaml:"abiAndBytecode"`
	Libraries       []string             `yaml:"libraries"`
	Implementations []ZetoDomainContract `yaml:"implementations"`
}

type AbiAndBytecode struct {
	Path string             `yaml:"path"`
	Json AbiAndBytecodeJSON `yaml:"json"`
}

type AbiAndBytecodeJSON struct {
	Abi      map[string]interface{} `yaml:"abi"`
	Bytecode string                 `yaml:"bytecode"`
}
