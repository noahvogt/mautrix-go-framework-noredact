// Copyright (c) 2024 Tulir Asokan
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

package bridgev2

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/rs/zerolog"
	"go.mau.fi/util/dbutil"

	"maunium.net/go/mautrix/bridge/status"
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/id"
)

var ErrNotLoggedIn = errors.New("not logged in")

type Bridge struct {
	ID  networkid.BridgeID
	DB  *database.Database
	Log zerolog.Logger

	Matrix   MatrixConnector
	Bot      MatrixAPI
	Network  NetworkConnector
	Commands *CommandProcessor
	Config   *bridgeconfig.BridgeConfig

	usersByMXID    map[id.UserID]*User
	userLoginsByID map[networkid.UserLoginID]*UserLogin
	portalsByKey   map[networkid.PortalKey]*Portal
	portalsByMXID  map[id.RoomID]*Portal
	ghostsByID     map[networkid.UserID]*Ghost
	cacheLock      sync.Mutex
}

func NewBridge(bridgeID networkid.BridgeID, db *dbutil.Database, log zerolog.Logger, cfg *bridgeconfig.BridgeConfig, matrix MatrixConnector, network NetworkConnector) *Bridge {
	br := &Bridge{
		ID:  bridgeID,
		DB:  database.New(bridgeID, db),
		Log: log,

		Matrix:  matrix,
		Network: network,
		Config:  cfg,

		usersByMXID:    make(map[id.UserID]*User),
		userLoginsByID: make(map[networkid.UserLoginID]*UserLogin),
		portalsByKey:   make(map[networkid.PortalKey]*Portal),
		portalsByMXID:  make(map[id.RoomID]*Portal),
		ghostsByID:     make(map[networkid.UserID]*Ghost),
	}
	if br.Config == nil {
		br.Config = &bridgeconfig.BridgeConfig{CommandPrefix: "!bridge"}
	}
	br.Commands = NewProcessor(br)
	br.Matrix.Init(br)
	br.Bot = br.Matrix.BotIntent()
	br.Network.Init(br)
	return br
}

type DBUpgradeError struct {
	Err     error
	Section string
}

func (e DBUpgradeError) Error() string {
	return e.Err.Error()
}

func (e DBUpgradeError) Unwrap() error {
	return e.Err
}

func (br *Bridge) Start() error {
	br.Log.Info().Msg("Starting bridge")
	ctx := br.Log.WithContext(context.Background())

	err := br.DB.Upgrade(ctx)
	if err != nil {
		return DBUpgradeError{Err: err, Section: "main"}
	}
	br.Log.Info().Msg("Starting Matrix connector")
	err = br.Matrix.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start Matrix connector: %w", err)
	}
	br.Log.Info().Msg("Starting network connector")
	err = br.Network.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start network connector: %w", err)
	}

	logins, err := br.GetAllUserLogins(ctx)
	if err != nil {
		return fmt.Errorf("failed to get user logins: %w", err)
	}
	for _, login := range logins {
		br.Log.Info().Str("id", string(login.ID)).Msg("Starting user login")
		err = login.Client.Connect(login.Log.WithContext(ctx))
		if err != nil {
			br.Log.Err(err).Msg("Failed to connect existing client")
		}
	}
	if len(logins) == 0 {
		br.Log.Info().Msg("No user logins found")
		br.SendGlobalBridgeState(status.BridgeState{StateEvent: status.StateUnconfigured})
	}

	br.Log.Info().Msg("Bridge started")
	return nil
}
