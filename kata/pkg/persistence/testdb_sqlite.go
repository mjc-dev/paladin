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

//go:build !testdbpostgres
// +build !testdbpostgres

package persistence

import (
	"context"

	"github.com/kaleido-io/paladin/kata/internal/confutil"
)

// Used for unit tests throughout the project that want to test against a real DB
// This version return an in-memory DB
func NewUnitTestPersistence(ctx context.Context) (Persistence, func(), error) {
	p, err := newSQLiteProvider(ctx, &Config{
		Type: "sqlite",
		SQLite: SQLiteConfig{
			SQLDBConfig: SQLDBConfig{
				URI:           ":memory:",
				AutoMigrate:   confutil.P(true),
				MigrationsDir: "../../db/migrations/sqlite",
				DebugQueries:  true,
			},
		},
	})
	return p, func() { p.Close() }, err
}