// Standalone acceptance fixture for the complete installer and full server UI.
// Run through crewship-heavy from a product module checkout. It honors the
// managed DOCKER_HOST, uses one fresh container, and never accesses the Vault.
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func main() {
	if err := qualify(); err != nil {
		fmt.Fprintln(os.Stderr, "installer qualification FAIL:", err)
		os.Exit(1)
	}
}
func qualify() (retErr error) {
	var artifactDir, installer, revision string
	flag.StringVar(&artifactDir, "artifact-dir", "", "Verified full release directory, including embedded UI")
	flag.StringVar(&installer, "installer", "", "Exact candidate scripts/install.sh")
	flag.StringVar(&revision, "revision", "", "Full verified clean source SHA")
	flag.Parse()
	if len(revision) != 40 || strings.Trim(revision, "0123456789abcdef") != "" || artifactDir == "" || installer == "" {
		return fmt.Errorf("explicit release artifacts, installer and full revision are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	stage, err := os.MkdirTemp("", "crewship-installer-qualification-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tag := "v0.0.0-native-" + revision[:12]
	archiveName := "crewship_" + strings.TrimPrefix(tag, "v") + "_linux_" + runtime.GOARCH + ".tar.gz"
	archivePath := filepath.Join(stage, archiveName)
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	artifacts := map[string]string{}
	for _, name := range []string{"crewship", "crewship-sidecar", "crewship-agent-cli", "entrypoint.sh"} {
		path := filepath.Join(artifactDir, name)
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("missing release companion %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular release companion %s", name)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: info.Size()}); err != nil {
			f.Close()
			return err
		}
		digest := sha256.New()
		_, err = io.Copy(io.MultiWriter(tw, digest), f)
		f.Close()
		if err != nil {
			return err
		}
		artifacts[name] = hex.EncodeToString(digest.Sum(nil))
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	archiveHash, err := fileHash(archivePath)
	if err != nil {
		return err
	}
	installBytes, err := os.ReadFile(installer)
	if err != nil {
		return err
	}
	trackedInstaller, err := exec.CommandContext(ctx, "git", "show", revision+":scripts/install.sh").Output()
	if err != nil || string(trackedInstaller) != string(installBytes) {
		return fmt.Errorf("installer bytes do not match the verified source revision")
	}
	installerHash := sha256.Sum256(installBytes)
	caPEM, tlsCert, err := fixtureCertificate()
	if err != nil {
		return err
	}
	caHash := sha256.Sum256(caPEM)
	var mu sync.Mutex
	downloads := map[string]int{}
	tlsListener, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{tlsCert}, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	releaseServer := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix := "/crewship-ai/crewship/releases/download/" + tag + "/"
		if r.Method != "GET" || r.Host != "github.com" || !strings.HasPrefix(r.URL.Path, prefix) {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		switch name {
		case archiveName:
			mu.Lock()
			downloads[name]++
			mu.Unlock()
			http.ServeFile(w, r, archivePath)
		case "checksums.txt":
			mu.Lock()
			downloads[name]++
			mu.Unlock()
			fmt.Fprintf(w, "%s  %s\n", archiveHash, archiveName)
		default:
			http.NotFound(w, r)
		}
	})}
	go releaseServer.Serve(tlsListener)
	defer releaseServer.Close()
	proxyListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return err
	}
	proxy := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" || r.Host != "github.com:443" {
			http.Error(w, "fixture target refused", 403)
			return
		}
		upstream, err := net.DialTimeout("tcp", tlsListener.Addr().String(), 5*time.Second)
		if err != nil {
			http.Error(w, "fixture unavailable", 502)
			return
		}
		downstream, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		defer downstream.Close()
		defer upstream.Close()
		if _, err := rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
		finished := make(chan struct{})
		go func() { io.Copy(upstream, io.MultiReader(rw.Reader, downstream)); upstream.Close(); close(finished) }()
		io.Copy(downstream, upstream)
		downstream.Close()
		<-finished
	})}
	go proxy.Serve(proxyListener)
	defer proxy.Close()
	bootstrapListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		return err
	}
	bootstrap := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/installer.sh":
			w.Write(installBytes)
		case "/ca.pem":
			w.Write(caPEM)
		default:
			http.NotFound(w, r)
		}
	})}
	go bootstrap.Serve(bootstrapListener)
	defer bootstrap.Close()
	daemon, err := client.New(client.FromEnv)
	if err != nil {
		return err
	}
	defer daemon.Close()
	fixtureID := strings.ToLower(rand.Text())
	artifactJSON, err := json.Marshal(artifacts)
	if err != nil {
		return err
	}
	env := []string{fmt.Sprintf("BOOTSTRAP_PORT=%d", bootstrapListener.Addr().(*net.TCPAddr).Port), fmt.Sprintf("PROXY_PORT=%d", proxyListener.Addr().(*net.TCPAddr).Port), fmt.Sprintf("INSTALLER_SHA=%x", installerHash), fmt.Sprintf("CA_SHA=%x", caHash), "CREWSHIP_VERSION=" + tag, "EXPECTED_REVISION=" + revision, "EXPECTED_BINARY_SHA=" + artifacts["crewship"], "EXPECTED_ARTIFACTS=" + string(artifactJSON), "EXPECTED_ARCH=" + runtime.GOARCH}
	created, err := daemon.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: "node:24-bookworm-slim@sha256:ba849c60be29959425b8734d57b8b4b7d56f98edd9504c9af091d5281095a71e", User: "0", Cmd: []string{"bash", "-c", containerScript}, Env: env, Labels: map[string]string{"native.installer.fixture": fixtureID}}, HostConfig: &container.HostConfig{NetworkMode: "bridge"}})
	if err != nil {
		return fmt.Errorf("fresh container creation: %w", err)
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		if err := removeOwnedFixture(cleanupCtx, daemon, created.ID, fixtureID); err != nil {
			return err
		}
		cleaned = true
		return nil
	}
	defer func() {
		if err := cleanup(); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}()

	if _, err := daemon.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	waited := daemon.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result, ok := <-waited.Result:
		if !ok {
			return fmt.Errorf("fixture wait closed without a terminal result")
		}
		if result.Error != nil || result.StatusCode != 0 {
			return fmt.Errorf("fresh installer/server fixture exit %d (container %s; no server log disclosed)", result.StatusCode, created.ID)
		}
	case err := <-waited.Error:
		return fmt.Errorf("fixture completion: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	}
	mu.Lock()
	complete := downloads[archiveName] > 0 && downloads["checksums.txt"] > 0
	mu.Unlock()
	if !complete {
		return fmt.Errorf("installer did not download and verify the candidate release")
	}
	if err := cleanup(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"state": "PASS", "source_revision": revision, "container_id": created.ID, "cleanup_confirmed": true, "archive_sha256": archiveHash, "installer_sha256": fmt.Sprintf("%x", installerHash), "artifacts": artifacts, "scope": "root explicit-tag installation to custom prefix, unchanged source installer, full server --no-docker, fresh DB, executing provenance, readyz and root embedded UI smoke; public distribution, signatures, latest resolver, service/provisioning and model/credential scenarios NOT RUN"})
}

// A completed container is not successful qualification until scoped removal
// and its absence are both authenticated. A gateway refusal is not NotFound.
func removeOwnedFixture(ctx context.Context, daemon *client.Client, id, fixtureID string) error {
	actual, err := daemon.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil || actual.Container.ID != id || actual.Container.Config == nil || actual.Container.Config.Labels["native.installer.fixture"] != fixtureID {
		return fmt.Errorf("fixture ownership/cleanup unconfirmed for %s", id)
	}
	if _, err := daemon.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		return fmt.Errorf("fixture remove unconfirmed: %w", err)
	}
	if _, err := daemon.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("fixture absence unconfirmed after remove: %v", err)
	}
	return nil
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
func fixtureCertificate() ([]byte, tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "native installer test only"}, DNSNames: []string{"github.com"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(ca, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}))
	return ca, pair, err
}

const containerScript = `set -eu
export DEBIAN_FRONTEND=noninteractive
apt-get update >/tmp/dependencies.log 2>&1
apt-get install -y --no-install-recommends curl ca-certificates >>/tmp/dependencies.log 2>&1
node <<'NODE'
const fs=require('fs'),crypto=require('crypto');
const row=fs.readFileSync('/proc/net/route','utf8').trim().split('\n').slice(1).map(x=>x.trim().split(/\s+/)).find(x=>x[1]==='00000000');
if(!row)throw Error('no fixture gateway');
const b=Buffer.alloc(4);b.writeUInt32LE(parseInt(row[2],16));const gateway=[...b].join('.');fs.writeFileSync('/tmp/fixture-gateway',gateway);
(async()=>{for(const [file,hash] of [['installer.sh',process.env.INSTALLER_SHA],['ca.pem',process.env.CA_SHA]]){const response=await fetch('http://'+gateway+':'+process.env.BOOTSTRAP_PORT+'/'+file);if(!response.ok)throw Error('bootstrap download');const bytes=Buffer.from(await response.arrayBuffer());if(crypto.createHash('sha256').update(bytes).digest('hex')!==hash)throw Error('bootstrap integrity');fs.writeFileSync('/tmp/'+file,bytes,{mode:0o600});}})().catch(e=>{console.error(e.message);process.exit(1)});
NODE
export HTTPS_PROXY="http://$(cat /tmp/fixture-gateway):$PROXY_PORT"
export https_proxy="$HTTPS_PROXY"
export NO_PROXY=127.0.0.1,localhost
export CURL_CA_BUNDLE=/tmp/ca.pem
export CREWSHIP_INSTALL_DIR=/tmp/native-install/bin
unset CREWSHIP_SKIP_VERIFY
bash /tmp/installer.sh >/tmp/installer.log 2>&1
test "$(sha256sum "$CREWSHIP_INSTALL_DIR/crewship" | cut -d ' ' -f1)" = "$EXPECTED_BINARY_SHA"
"$CREWSHIP_INSTALL_DIR/crewship-agent-cli" agent-runtime-probe >/tmp/sdk-probe.json
node <<'NODE'
const fs=require('fs'),crypto=require('crypto'),path=require('path');
const hashes=JSON.parse(process.env.EXPECTED_ARTIFACTS);
for(const [name,hash] of Object.entries(hashes)){const file=path.join(process.env.CREWSHIP_INSTALL_DIR,name);if(!fs.statSync(file).isFile()||!(fs.statSync(file).mode&0o111)||crypto.createHash('sha256').update(fs.readFileSync(file)).digest('hex')!==hash)process.exit(1)}
const p=require('/tmp/sdk-probe.json');
if(p.protocol!==1||p.commit!==process.env.EXPECTED_REVISION||p.dirty!==false||p.os!=='linux'||p.arch!==process.env.EXPECTED_ARCH||p.sha256!==hashes['crewship-agent-cli'])process.exit(1);
for(const command of ['version','commands'])if(!p.commands.includes(command))process.exit(1);
NODE
"$CREWSHIP_INSTALL_DIR/crewship" version -f json >/tmp/installed-version.json
node -e 'const p=require("/tmp/installed-version.json").client;if(p.commit!==process.env.EXPECTED_REVISION||p.dirty!==false||p.os!=="linux"||p.arch!==process.env.EXPECTED_ARCH)process.exit(1)'
test ! -e /tmp/native-install/data/crewship.db
mkdir -p /tmp/native-install/data
export CREWSHIP_HOME=/tmp/native-install/home
export CREWSHIP_DATA_DIR=/tmp/native-install/data
export CREWSHIP_PORT=18080
export KEEPER_ENABLED=false
"$CREWSHIP_INSTALL_DIR/crewship" start --no-docker --data-dir /tmp/native-install/data --db /tmp/native-install/data/crewship.db >/tmp/server.log 2>&1 &
server_pid=$!
export NATIVE_SERVER_PID="$server_pid"
trap 'kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true' EXIT
node <<'NODE'
(async()=>{for(let i=0;i<120;i++){try{const ready=await fetch('http://127.0.0.1:18080/readyz');if(ready.ok){const ui=await fetch('http://127.0.0.1:18080/');const text=await ui.text();if(!ui.ok||!text.includes('<html'))throw Error('embedded UI unavailable');const fs=require('fs'),crypto=require('crypto');if(fs.statSync('/tmp/native-install/data/crewship.db').size<=0)throw Error('fresh DB absent');if(crypto.createHash('sha256').update(fs.readFileSync('/proc/'+process.env.NATIVE_SERVER_PID+'/exe')).digest('hex')!==process.env.EXPECTED_BINARY_SHA)throw Error('running server identity differs');process.exit(0)}}catch(e){}await new Promise(r=>setTimeout(r,500))}throw Error('fresh server readiness/UI failed')})().catch(e=>{console.error(e.message);process.exit(1)});
NODE
`
