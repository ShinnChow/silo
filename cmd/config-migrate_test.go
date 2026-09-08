// Copyright (c) 2015-2026 MinIO, Inc.
//
// This file is part of MinIO Object Storage stack
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/minio/minio/internal/config"
	"github.com/minio/minio/internal/config/notify"
	"github.com/minio/minio/internal/event/target"
)

func installLegacyConfigFile(t *testing.T, configure func(*serverConfigV33)) (string, []byte) {
	t.Helper()

	cfg := &serverConfigV33{
		Version: "33",
		Notify:  notify.NewConfig(),
	}
	configure(cfg)

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	oldConfigDir := globalConfigDir
	globalConfigDir = &ConfigDir{path: t.TempDir()}
	t.Cleanup(func() { globalConfigDir = oldConfigDir })

	configFile := getConfigFile()
	if err = os.WriteFile(configFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return configFile, data
}

func assertLegacyMigrationError(t *testing.T, err error, subsystem, name, key, secret string) {
	t.Helper()
	var targetErr *notify.LegacyDatabaseTargetError
	if !errors.As(err, &targetErr) {
		t.Fatalf("error = %v, want *notify.LegacyDatabaseTargetError", err)
	}
	msg := err.Error()
	for _, want := range []string{subsystem + config.SubSystemSeparator + name, key} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, secret) {
		t.Errorf("migration error leaks database password %q: %s", secret, msg)
	}
}

func TestReadConfigWithoutMigrateRejectsLegacyDatabaseTargets(t *testing.T) {
	tests := []struct {
		name      string
		subsystem string
		key       string
		secret    string
		configure func(*serverConfigV33)
	}{
		{
			name:      "postgres",
			subsystem: config.NotifyPostgresSubSys,
			key:       target.PostgresConnectionString,
			secret:    "postgres-migration-secret",
			configure: func(cfg *serverConfigV33) {
				cfg.Notify.PostgreSQL["archive"] = target.PostgreSQLArgs{
					Enable:   true,
					Port:     "5432",
					Username: "legacy-user",
					Password: "postgres-migration-secret",
					Database: "events",
				}
			},
		},
		{
			name:      "mysql",
			subsystem: config.NotifyMySQLSubSys,
			key:       target.MySQLDSNString,
			secret:    "mysql-migration-secret",
			configure: func(cfg *serverConfigV33) {
				cfg.Notify.MySQL["archive"] = target.MySQLArgs{
					Enable:   true,
					Port:     "3306",
					User:     "legacy-user",
					Password: "mysql-migration-secret",
					Database: "events",
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configFile, original := installLegacyConfigFile(t, test.configure)
			got, err := readConfigWithoutMigrate(t.Context(), nil)
			if got != nil {
				t.Fatalf("config = %v, want nil on failed migration", got)
			}
			assertLegacyMigrationError(t, err, test.subsystem, "archive", test.key, test.secret)

			after, readErr := os.ReadFile(configFile)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(after, original) {
				t.Fatal("failed migration rewrote the legacy source config")
			}
			if _, statErr := os.Stat(configFile + ".old"); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed migration created a backup/persistence artifact: %v", statErr)
			}
		})
	}
}

func TestReadConfigWithoutMigrateMigratesCanonicalDatabaseTargets(t *testing.T) {
	const (
		postgresConnection   = "host=postgres.example port=5432 dbname=events user=app password=secret sslmode=disable"
		mysqlDSN             = "app:secret@tcp(mysql.example:3306)/events?parseTime=true"
		discardedLegacyValue = "discarded-legacy-value"
	)
	installLegacyConfigFile(t, func(cfg *serverConfigV33) {
		cfg.Notify.PostgreSQL["archive"] = target.PostgreSQLArgs{
			Enable:           true,
			Format:           "namespace",
			ConnectionString: postgresConnection,
			Table:            "events",
			Port:             discardedLegacyValue,
			Username:         discardedLegacyValue,
			Password:         discardedLegacyValue,
			Database:         discardedLegacyValue,
		}
		cfg.Notify.MySQL["archive"] = target.MySQLArgs{
			Enable:   true,
			Format:   "namespace",
			DSN:      mysqlDSN,
			Table:    "events",
			Port:     discardedLegacyValue,
			User:     discardedLegacyValue,
			Password: discardedLegacyValue,
			Database: discardedLegacyValue,
		}
	})

	got, err := readConfigWithoutMigrate(t.Context(), nil)
	if err != nil {
		t.Fatalf("readConfigWithoutMigrate: %v", err)
	}
	tests := []struct {
		subsystem string
		key       string
		want      string
		discarded string
	}{
		{config.NotifyPostgresSubSys, target.PostgresConnectionString, postgresConnection, discardedLegacyValue},
		{config.NotifyMySQLSubSys, target.MySQLDSNString, mysqlDSN, discardedLegacyValue},
	}
	for _, test := range tests {
		kvs := got[test.subsystem]["archive"]
		if value := kvs.Get(test.key); value != test.want {
			t.Errorf("%s %s = %q, want %q", test.subsystem, test.key, value, test.want)
		}
		if err := config.CheckValidKeys(test.subsystem+config.SubSystemSeparator+"archive", kvs, notify.DefaultNotificationKVS[test.subsystem]); err != nil {
			t.Errorf("migrated %s target failed key validation: %v", test.subsystem, err)
		}
		for _, key := range []string{"host", "port", "username", "password", "database"} {
			if _, ok := kvs.Lookup(key); ok {
				t.Errorf("migrated %s target contains legacy key %q", test.subsystem, key)
			}
		}
		for _, kv := range kvs {
			if strings.Contains(kv.Value, test.discarded) {
				t.Errorf("migrated %s target contains discarded legacy value in %q", test.subsystem, kv.Key)
			}
		}
	}

	postgresTargets, err := notify.GetNotifyPostgres(got[config.NotifyPostgresSubSys])
	if err != nil {
		t.Fatalf("GetNotifyPostgres: %v", err)
	}
	if value := postgresTargets["archive"].ConnectionString; value != postgresConnection {
		t.Errorf("Postgres connection string = %q, want %q", value, postgresConnection)
	}
	mysqlTargets, err := notify.GetNotifyMySQL(got[config.NotifyMySQLSubSys])
	if err != nil {
		t.Fatalf("GetNotifyMySQL: %v", err)
	}
	if value := mysqlTargets["archive"].DSN; value != mysqlDSN {
		t.Errorf("MySQL DSN = %q, want %q", value, mysqlDSN)
	}
}

func TestInitConfigSubsystemReturnsLegacyDatabaseTargetError(t *testing.T) {
	obj, fsDir, err := prepareFS(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = obj.Shutdown(context.Background())
		_ = os.RemoveAll(fsDir)
	})

	const secret = "startup-migration-secret"
	installLegacyConfigFile(t, func(cfg *serverConfigV33) {
		cfg.Notify.PostgreSQL["archive"] = target.PostgreSQLArgs{
			Enable:   true,
			Port:     "5432",
			Username: "legacy-user",
			Password: secret,
			Database: "events",
		}
	})

	globalServerConfigMu.RLock()
	var before config.Config
	if globalServerConfig != nil {
		before = globalServerConfig.Clone()
	}
	globalServerConfigMu.RUnlock()

	err = initConfigSubsystem(t.Context(), obj)
	assertLegacyMigrationError(t, err, config.NotifyPostgresSubSys, "archive", target.PostgresConnectionString, secret)
	if configRetriableErrors(err) {
		t.Fatal("legacy database migration error must be startup-fatal, not retriable")
	}
	if !fatalServerConfigError(err) {
		t.Fatal("legacy database migration error must abort server startup")
	}

	globalServerConfigMu.RLock()
	var after config.Config
	if globalServerConfig != nil {
		after = globalServerConfig.Clone()
	}
	globalServerConfigMu.RUnlock()
	if !reflect.DeepEqual(after, before) {
		t.Fatal("failed migration activated a partial server configuration")
	}
}
