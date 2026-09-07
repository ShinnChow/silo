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

package logger

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	types "github.com/minio/minio/internal/logger/target/loggertypes"
)

// fakeTarget is a minimal Target used to exercise the registry.
type fakeTarget struct {
	name  string
	kind  types.TargetType
	inits atomic.Int32
}

func (f *fakeTarget) String() string                  { return f.name }
func (f *fakeTarget) Endpoint() string                { return "" }
func (f *fakeTarget) Stats() types.TargetStats        { return types.TargetStats{} }
func (f *fakeTarget) Init(context.Context) error      { f.inits.Add(1); return nil }
func (f *fakeTarget) IsOnline(context.Context) bool   { return true }
func (f *fakeTarget) Cancel()                         {}
func (f *fakeTarget) Send(context.Context, any) error { return nil }
func (f *fakeTarget) Type() types.TargetType          { return f.kind }

// swapSystemTargets isolates the package-level registry for a test.
func swapSystemTargets(t *testing.T) {
	t.Helper()
	prevTargets, prevConsole := systemTargets, consoleTgt
	systemTargets, consoleTgt = newTargetsList(), nil
	t.Cleanup(func() {
		systemTargets, consoleTgt = prevTargets, prevConsole
	})
}

// AddSystemTarget must not register the same target twice: the console
// logger is re-added on every console-log subscription, and duplicates
// surface as identical series in the /logger/webhook metrics collector.
func TestAddSystemTargetIdempotent(t *testing.T) {
	swapSystemTargets(t)
	ctx := context.Background()

	console := &fakeTarget{name: "console+http", kind: types.TargetConsole}
	for range 3 {
		if err := AddSystemTarget(ctx, console); err != nil {
			t.Fatalf("AddSystemTarget: %v", err)
		}
	}
	if got := len(SystemTargets()); got != 1 {
		t.Fatalf("expected 1 system target after repeated add, got %d", got)
	}
	if got := console.inits.Load(); got != 1 {
		t.Fatalf("expected Init to run once, ran %d times", got)
	}
	if consoleTgt != console {
		t.Fatalf("consoleTgt not set to the console target")
	}

	other := &fakeTarget{name: "other", kind: types.TargetHTTP}
	if err := AddSystemTarget(ctx, other); err != nil {
		t.Fatalf("AddSystemTarget: %v", err)
	}
	if got := len(SystemTargets()); got != 2 {
		t.Fatalf("expected 2 distinct system targets, got %d", got)
	}
}

func TestAddSystemTargetConcurrent(t *testing.T) {
	swapSystemTargets(t)
	ctx := context.Background()

	console := &fakeTarget{name: "console+http", kind: types.TargetConsole}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = AddSystemTarget(ctx, console)
		}()
	}
	wg.Wait()

	if got := len(SystemTargets()); got != 1 {
		t.Fatalf("expected 1 system target after concurrent adds, got %d", got)
	}
}
