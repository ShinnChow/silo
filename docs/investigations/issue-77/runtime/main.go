//go:build ignore

// Standalone disposable loopback-lab driver; see ../../issue-77.md.
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/minio/madmin-go/v3"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/tags"
)

const bucket = "issue77-runtime"
const user = "issue77-lab-admin"
const password = "issue77-disposable-lab-secret"

var ctx = context.Background()
var root string

type site struct {
	name, address, binary, gate string
	cmd                         *exec.Cmd
	log                         *os.File
	proxy                       *httptest.Server
	block                       atomic.Bool
	calls                       atomic.Int64
	dropped                     atomic.Int64
	admin                       *madmin.AdminClient
	s3                          *minio.Client
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
func stamp(s string) { fmt.Println(time.Now().UTC().Format(time.RFC3339), s) }
func port() string {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	check(e)
	s := l.Addr().String()
	check(l.Close())
	return s
}
func newSite(name, binary string) *site {
	s := &site{name: name, address: port(), binary: binary, gate: "on"}
	target, e := url.Parse("http://" + s.address)
	check(e)
	reverse := httputil.NewSingleHostReverseProxy(target)
	reverse.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { w.WriteHeader(503) }
	s.proxy = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/site-replication/peer/bucket-meta") {
			data, e := io.ReadAll(r.Body)
			check(e)
			r.Body = io.NopCloser(bytes.NewReader(data))
			var event madmin.SRBucketMeta
			check(json.Unmarshal(data, &event))
			if event.Bucket == bucket {
				s.calls.Add(1)
				if s.block.Load() {
					s.dropped.Add(1)
					w.WriteHeader(503)
					return
				}
			}
		}
		reverse.ServeHTTP(w, r)
	}))
	s.admin, e = madmin.New(s.address, user, password, false)
	check(e)
	s.s3, e = minio.New(s.address, &minio.Options{Creds: credentials.NewStaticV4(user, password, ""), Secure: false})
	check(e)
	return s
}
func (s *site) start() {
	var e error
	s.log, e = os.OpenFile(filepath.Join(root, s.name+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	check(e)
	args := []string{"--json", "server", "--quiet", "--address", s.address, "--console-address", port()}
	for i := 0; i < 4; i++ {
		dir := filepath.Join(root, s.name, fmt.Sprintf("disk%d", i))
		check(os.MkdirAll(dir, 0700))
		args = append(args, dir)
	}
	s.cmd = exec.Command(s.binary, args...)
	s.cmd.Env = append(os.Environ(), "MINIO_ROOT_USER="+user, "MINIO_ROOT_PASSWORD="+password, "MINIO_BROWSER=off", "MINIO_CI_CD=1", "MINIO_SITE_REPLICATION_METADATA_TOMBSTONES="+s.gate)
	s.cmd.Stdout = s.log
	s.cmd.Stderr = s.log
	check(s.cmd.Start())
	client := http.Client{Timeout: time.Second}
	deadline := time.Now().Add(50 * time.Second)
	for time.Now().Before(deadline) {
		r, e := client.Get("http://" + s.address + "/minio/health/ready")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				stamp(s.name + " ready gate=" + s.gate)
				return
			}
		}
		time.Sleep(time.Second)
	}
	panic(s.name + " startup timed out; inspect " + s.log.Name())
}
func (s *site) stop() {
	if s.cmd == nil {
		return
	}
	_ = s.cmd.Process.Signal(os.Interrupt)
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.log.Close()
	s.cmd = nil
}
func (s *site) info() madmin.SRBucketInfo {
	i, e := s.admin.SRMetaInfo(ctx, madmin.SRStatusOptions{Buckets: true})
	check(e)
	return i.Buckets[bucket]
}
func (s *site) apply(e madmin.SRBucketMeta) {
	e.Bucket = bucket
	check(s.admin.SRPeerReplicateBucketMeta(ctx, e))
}
func enc(s string) *string { r := base64.StdEncoding.EncodeToString([]byte(s)); return &r }
func events(at time.Time, newer bool) []madmin.SRBucketMeta {
	value, days := "old", 10
	if newer {
		value, days = "new", 30
	}
	return []madmin.SRBucketMeta{
		{Type: madmin.SRBucketMetaTypePolicy, UpdatedAt: at, Policy: []byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Sid":%q,"Effect":"Allow","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::%s/*"}]}`, value, bucket))},
		{Type: madmin.SRBucketMetaTypeTags, UpdatedAt: at, Tags: enc(`<Tagging><TagSet><Tag><Key>key</Key><Value>` + value + `</Value></Tag></TagSet></Tagging>`)},
		{Type: madmin.SRBucketMetaTypeSSEConfig, UpdatedAt: at, SSEConfig: enc(`<ServerSideEncryptionConfiguration><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>AES256</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`)},
		{Type: madmin.SRBucketMetaTypeQuotaConfig, UpdatedAt: at, Quota: []byte(`{"quota":1024,"quotatype":"hard"}`)},
		{Type: madmin.SRBucketMetaTypeVersionConfig, UpdatedAt: at, Versioning: enc(`<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`)},
		{Type: madmin.SRBucketMetaTypeObjectLockConfig, UpdatedAt: at, ObjectLockConfig: enc(fmt.Sprintf(`<ObjectLockConfiguration><ObjectLockEnabled>Enabled</ObjectLockEnabled><Rule><DefaultRetention><Mode>GOVERNANCE</Mode><Days>%d</Days></DefaultRetention></Rule></ObjectLockConfiguration>`, days))},
	}
}
func states(m madmin.SRBucketInfo) []byte {
	b, e := json.Marshal([]any{m.Policy, m.PolicyUpdatedAt, m.Tags, m.TagConfigUpdatedAt, m.SSEConfig, m.SSEConfigUpdatedAt, m.QuotaConfig, m.QuotaConfigUpdatedAt, m.Versioning, m.VersioningConfigUpdatedAt, m.ObjectLockConfig, m.ObjectLockConfigUpdatedAt})
	check(e)
	return b
}
func times(m madmin.SRBucketInfo) []time.Time {
	return []time.Time{m.PolicyUpdatedAt, m.TagConfigUpdatedAt, m.SSEConfigUpdatedAt, m.QuotaConfigUpdatedAt, m.VersioningConfigUpdatedAt, m.ObjectLockConfigUpdatedAt}
}
func converge(a, b *site, label string) {
	stamp(label + ": waiting for ordinary 30-second heal")
	deadline := time.Now().Add(130 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Equal(states(a.info()), states(b.info())) {
			stamp(label + ": six field states equal")
			return
		}
		time.Sleep(2 * time.Second)
	}
	check(os.WriteFile(filepath.Join(root, label+"-a.json"), states(a.info()), 0600))
	check(os.WriteFile(filepath.Join(root, label+"-b.json"), states(b.info()), 0600))
	panic(label + " convergence timed out")
}
func quiet(a, b *site, label string) {
	beforeA, beforeB := a.calls.Load(), b.calls.Load()
	stamp(label + ": observing two heal intervals")
	time.Sleep(65 * time.Second)
	if a.calls.Load() != beforeA || b.calls.Load() != beforeB {
		panic(fmt.Sprintf("%s not quiet: a %d -> %d, b %d -> %d", label, beforeA, a.calls.Load(), beforeB, b.calls.Load()))
	}
	stamp(label + ": zero metadata RPCs across 65 seconds")
}
func main() {
	root = os.Args[1]
	binary, e := filepath.Abs(os.Args[2])
	check(e)
	check(os.MkdirAll(root, 0700))
	a, b := newSite("site-a", binary), newSite("site-b", binary)
	defer a.proxy.Close()
	defer b.proxy.Close()
	defer a.stop()
	defer b.stop()
	a.start()
	b.start()
	created := time.Now().UTC().Add(-time.Hour)
	check(a.admin.SRPeerBucketOps(ctx, bucket, madmin.MakeWithVersioningBktOp, map[string]string{"createdAt": created.Format(time.RFC3339Nano), "lockEnabled": "true", "versioningEnabled": "true"}))
	for _, event := range events(created, false) {
		a.apply(event)
	}
	stamp("historical bucket prepared: six live fields at Created")
	status, e := a.admin.SiteReplicationAdd(ctx, []madmin.PeerSite{{Name: a.name, Endpoint: a.proxy.URL, AccessKey: user, SecretKey: password}, {Name: b.name, Endpoint: b.proxy.URL, AccessKey: user, SecretKey: password}}, madmin.SRAddOptions{})
	check(e)
	if !status.Success || status.InitialSyncErrorMessage != "" {
		panic(fmt.Sprintf("add sites: %+v", status))
	}
	converge(a, b, "baseline-initial-sync")
	for _, tm := range times(a.info()) {
		if !tm.Equal(created) {
			panic("initial sync reclocked historical baseline: " + tm.String())
		}
	}
	stamp("initial sync preserved six historical Created timestamps")
	a.block.Store(true)
	b.block.Store(true)
	faultTags, e := tags.NewTags(map[string]string{"fault": "outgoing-message-dropped"}, true)
	check(e)
	check(a.s3.SetBucketTagging(ctx, bucket, faultTags))
	if b.dropped.Load() == 0 {
		panic("outbound fault injection did not drop a real metadata RPC")
	}
	stamp("real local PUT outgoing replication RPC dropped by partition")
	at := time.Now().UTC().Add(time.Minute)
	newer, older := events(at, true), events(at.Add(-time.Second), false)
	for i := range newer {
		a.apply(newer[i])
		a.apply(newer[i])
		b.apply(older[i])
		a.apply(older[i])
	}
	for _, tm := range times(a.info()) {
		if !tm.Equal(at) {
			panic("duplicate/out-of-order source time changed: " + tm.String())
		}
	}
	if bytes.Equal(states(a.info()), states(b.info())) {
		panic("fault setup did not create divergence")
	}
	a.block.Store(false)
	b.block.Store(false)
	converge(a, b, "live-reconnect")
	quiet(a, b, "live-steady")
	for _, event := range older {
		b.apply(event)
	}
	if !bytes.Equal(states(a.info()), states(b.info())) {
		panic("stale event after convergence rolled back state")
	}
	stamp("six duplicate/out-of-order events rejected without clock changes")
	a.block.Store(true)
	b.block.Store(true)
	dropped := b.dropped.Load()
	check(a.s3.RemoveBucketTagging(ctx, bucket))
	if b.dropped.Load() <= dropped {
		panic("ordinary delete RPC was not dropped")
	}
	stamp("real local DELETE outgoing replication RPC dropped by partition")
	deleteAt := at.Add(time.Minute)
	for _, event := range newer[:4] {
		a.apply(madmin.SRBucketMeta{Type: event.Type, UpdatedAt: deleteAt})
	}
	beforeRestart := states(a.info())
	a.stop()
	a.start()
	if !bytes.Equal(beforeRestart, states(a.info())) {
		panic("restart lost deletion/source state")
	}
	stamp("four tombstones survived process restart")
	a.block.Store(false)
	b.block.Store(false)
	converge(a, b, "delete-reconnect")
	quiet(a, b, "delete-steady")
	final := a.info()
	if len(final.Policy) != 0 || final.Tags != nil || final.SSEConfig != nil || final.QuotaConfig != nil {
		panic("deletion did not converge")
	}
	check(os.WriteFile(filepath.Join(root, "converged.json"), states(final), 0600))
	// Deliberate legacy and generation exceptions are outside convergence proof.
	for i := 0; i < 20; i++ {
		event := newer[1]
		event.UpdatedAt = time.Time{}
		a.apply(event)
		event.UpdatedAt = final.CreatedAt.Add(-time.Duration(i+1) * time.Second)
		a.apply(event)
	}
	data, e := os.ReadFile(filepath.Join(root, "site-a.jsonl"))
	check(e)
	for _, reason := range []string{"legacy-zero", "before-created"} {
		n := strings.Count(string(data), "bucket metadata replication: "+reason)
		if n != 1 {
			panic(fmt.Sprintf("%s diagnostics count %d, want 1", reason, n))
		}
	}
	stamp("40 exceptional events produced exactly one log per reason")
	if len(os.Args) > 3 {
		stamp("starting fixed/previous-server rolling-upgrade smoke with gate off")
		a.stop()
		b.stop()
		a.gate = "off"
		b.gate = "off"
		b.binary, e = filepath.Abs(os.Args[3])
		check(e)
		a.start()
		b.start()
		tag, e := tags.NewTags(map[string]string{"upgrade": "works"}, true)
		check(e)
		check(a.s3.SetBucketTagging(ctx, bucket, tag))
		remote, e := b.s3.GetBucketTagging(ctx, bucket)
		check(e)
		if remote.ToMap()["upgrade"] != "works" {
			panic("mixed-version live event lost")
		}
		check(a.s3.RemoveBucketTagging(ctx, bucket))
		if a.info().Tags != nil || b.info().Tags != nil {
			panic("mixed-version ordinary tag delete lost")
		}
		if !a.info().TagConfigUpdatedAt.IsZero() {
			panic("off exporter exposed new tombstone")
		}
		stamp("mixed-version ordinary PUT/DELETE and off tombstone visibility passed")
	}
	stamp("PASS: isolated two-site implementation acceptance")
}
