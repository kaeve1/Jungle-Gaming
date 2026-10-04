//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"jungle-gaming-backend/internal/bootstrap"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type stage11Process struct {
	stderr  *os.File
	cmd     *exec.Cmd
	api     apiFixture
	dir     string
	stopped bool
}
type stage11Ready struct {
	Address string
	PID     int
}

func stage11Wait(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if fn() {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("distributed barrier timed out")
}
func stage11Start(t *testing.T, db, input, output, roles, mode, ttl string) *stage11Process {
	t.Helper()
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	name := filepath.Base(dir) + "-" + filepath.Base(filepath.Dir(dir))
	cmd := exec.Command(binary, "-test.run=^TestStage11ProcessHelper$", "-test.timeout=300s")
	for _, v := range os.Environ() {
		key := strings.SplitN(v, "=", 2)[0]
		if strings.HasPrefix(key, "KC_SECRET_") || strings.HasPrefix(key, "TEST_SQS_ADMIN_") || key == "TEST_PG_SUPERUSER_PASSWORD" || key == "TEST_PG_MIGRATOR_PASSWORD" || key == "TEST_SQS_CREDENTIALS_FILE" || strings.HasPrefix(key, "AWS_") {
			continue
		}
		cmd.Env = append(cmd.Env, v)
	}
	cmd.Env = append(cmd.Env, "STAGE11_DB="+db, "STAGE11_DIR="+dir, "STAGE11_INPUT="+input, "STAGE11_OUTPUT="+output, "STAGE11_ROLES="+roles, "STAGE11_MODE="+mode, "STAGE11_INSTANCE="+name, "STAGE11_TTL="+ttl)
	cmd.Stdout = io.Discard
	stderr, e := os.OpenFile(filepath.Join(dir, "stderr"), os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	cmd.Stderr = stderr
	p := &stage11Process{cmd: cmd, dir: dir, stderr: stderr}
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if !p.stopped {
			p.stop(t, false)
		}
	})
	var ready stage11Ready
	stage11Wait(t, 35*time.Second, func() bool {
		b, e := os.ReadFile(filepath.Join(dir, "ready"))
		return e == nil && json.Unmarshal(b, &ready) == nil
	})
	if ready.PID != cmd.Process.Pid {
		t.Fatal("readiness PID is not independent child")
	}
	p.api = apiFixture{url: "http://" + ready.Address, client: &http.Client{Timeout: 25 * time.Second}}
	t.Logf("independent Fx process PID=%d roles=%s mode=%s", ready.PID, roles, mode)
	return p
}
func (p *stage11Process) stop(t *testing.T, kill bool) {
	t.Helper()
	if p.stopped {
		return
	}
	if kill {
		_ = p.cmd.Process.Kill()
	} else {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan error, 1)
	go func() { done <- p.cmd.Wait() }()
	select {
	case e := <-done:
		if !kill && e != nil {
			t.Errorf("child did not shut down cleanly: %v", e)
		}
	case <-time.After(25 * time.Second):
		_ = p.cmd.Process.Kill()
		<-done
		t.Error("child shutdown timed out")
	}
	p.stopped = true
	_ = p.stderr.Close()
	if b, e := os.ReadFile(filepath.Join(p.dir, "stderr")); e == nil && bytes.Contains(b, []byte("WARNING: DATA RACE")) {
		t.Error("race detector reported a data race in child process")
	}
	if p.api.client != nil {
		p.api.client.CloseIdleConnections()
	}
}
func (p *stage11Process) barrier(t *testing.T) {
	t.Helper()
	stage11Wait(t, 20*time.Second, func() bool { _, e := os.Stat(filepath.Join(p.dir, "barrier")); return e == nil })
}
func stage11Marker(dir string) {
	_ = os.WriteFile(filepath.Join(dir, "barrier"), []byte("reached"), 0600)
}

// The child executes production Fx. Fault injection is exclusively in local
// proxies from this _test.go file, never in the application binary.
func TestStage11ProcessHelper(t *testing.T) {
	db := os.Getenv("STAGE11_DB")
	if db == "" {
		t.Skip("child only")
	}
	dir := os.Getenv("STAGE11_DIR")
	mode := os.Getenv("STAGE11_MODE")
	endpoint := env(t, "TEST_SQS_ENDPOINT")
	target, _ := url.Parse(endpoint)
	brokerProxy := httputil.NewSingleHostReverseProxy(target)
	original := brokerProxy.Director
	brokerProxy.Director = func(r *http.Request) { host := r.Host; original(r); r.Host = host }
	brokerProxy.ModifyResponse = func(r *http.Response) error {
		if strings.HasSuffix(r.Request.Header.Get("X-Amz-Target"), ".ReceiveMessage") && r.StatusCode == 200 {
			b, e := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if e != nil {
				return e
			}
			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(b))
			var out struct {
				Messages []struct {
					Attributes map[string]string `json:"Attributes"`
				} `json:"Messages"`
			}
			if json.Unmarshal(b, &out) == nil {
				for _, m := range out.Messages {
					if m.Attributes["ApproximateReceiveCount"] != "" {
						_ = os.WriteFile(filepath.Join(dir, "delivered"), []byte(m.Attributes["ApproximateReceiveCount"]), 0600)
					}
				}
			}
		}
		return nil
	}
	if mode == "publish-after" {
		brokerProxy.ModifyResponse = func(r *http.Response) error {
			if strings.HasSuffix(r.Request.Header.Get("X-Amz-Target"), ".SendMessage") && r.StatusCode == 200 {
				stage11Marker(dir)
				<-r.Request.Context().Done()
				return r.Request.Context().Err()
			}
			return nil
		}
	}
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.Header.Get("X-Amz-Target")
		if (mode == "consume-after" && strings.HasSuffix(action, ".DeleteMessage")) || (mode == "publish-before" && strings.HasSuffix(action, ".SendMessage")) {
			stage11Marker(dir)
			<-r.Context().Done()
			return
		}
		brokerProxy.ServeHTTP(w, r)
	}), ReadHeaderTimeout: 3 * time.Second}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	go broker.Serve(listener)
	endpoint = "http://" + listener.Addr().String()
	settings := map[string]string{"DATABASE_URL": fmt.Sprintf("postgres://wallet_app:%s@%s:5432/%s?sslmode=disable", url.QueryEscape(env(t, "TEST_PG_APP_PASSWORD")), env(t, "TEST_PG_HOST"), db), "APP_ROLES": os.Getenv("STAGE11_ROLES"), "HTTP_ADDRESS": "127.0.0.1:0", "ALLOW_INSECURE_LOCAL": "true", "OIDC_ISSUER": env(t, "TEST_OIDC_ISSUER"), "OIDC_JWKS_URL": env(t, "TEST_OIDC_JWKS_URL"), "SQS_ENDPOINT": endpoint, "SQS_CREDENTIALS_FILE": env(t, "TEST_SQS_SERVICE_CREDENTIALS_FILE"), "INSTANCE_ID": os.Getenv("STAGE11_INSTANCE"), "CONSUMER_QUEUE": os.Getenv("STAGE11_INPUT"), "PUBLISHER_QUEUE": os.Getenv("STAGE11_OUTPUT"), "CONSUMER_CONCURRENCY": "1", "PUBLISHER_CONCURRENCY": "1", "REFERENCE_BASE_DELAY": "100ms", "REFERENCE_MAX_DELAY": "500ms"}
	if ttl := os.Getenv("STAGE11_TTL"); ttl != "" {
		settings["REFERENCE_TTL"] = ttl
	}
	log, e := os.OpenFile(filepath.Join(dir, "runtime.jsonl"), os.O_CREATE|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer log.Close()
	app, e := bootstrap.NewInstance(func(k string) string { return settings[k] }, log)
	if e != nil {
		t.Fatal(e)
	}
	ctx, c := context.WithTimeout(context.Background(), 30*time.Second)
	e = app.Start(ctx)
	c()
	if e != nil {
		t.Fatal(e)
	}
	address := app.Address()
	var front *http.Server
	if mode == "http-after" {
		target, _ := url.Parse("http://" + address)
		proxy := httputil.NewSingleHostReverseProxy(target)
		proxy.ModifyResponse = func(r *http.Response) error {
			if r.Request.Method == "POST" && r.Request.URL.Path == "/wagering/transactions" && r.StatusCode == 201 {
				stage11Marker(dir)
				<-r.Request.Context().Done()
				return r.Request.Context().Err()
			}
			return nil
		}
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		front = &http.Server{Handler: proxy, ReadHeaderTimeout: 3 * time.Second}
		go front.Serve(l)
		address = l.Addr().String()
	}
	ready, _ := json.Marshal(stage11Ready{address, os.Getpid()})
	if e = os.WriteFile(filepath.Join(dir, "ready"), ready, 0600); e != nil {
		t.Fatal(e)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	<-signals
	signal.Stop(signals)
	ctx, c = context.WithTimeout(context.Background(), 20*time.Second)
	defer c()
	if e = app.Stop(ctx); e != nil {
		t.Error(e)
	}
	_ = broker.Close()
	if front != nil {
		_ = front.Close()
	}
}

func stage11Call(t *testing.T, p *stage11Process, token, key, body, correlation string) (int, map[string]any, error) {
	req, e := http.NewRequestWithContext(t.Context(), "POST", p.api.url+"/wagering/transactions", strings.NewReader(body))
	if e != nil {
		return 0, nil, e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("X-Correlation-Id", correlation)
	resp, e := p.api.client.Do(req)
	if e != nil {
		return 0, nil, e
	}
	defer resp.Body.Close()
	var result map[string]any
	d := json.NewDecoder(resp.Body)
	d.UseNumber()
	e = d.Decode(&result)
	return resp.StatusCode, result, e
}
func stage11AssertLog(t *testing.T, p *stage11Process, correlation string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(p.dir, "runtime.jsonl"))
	if e != nil || !bytes.Contains(b, []byte(`"correlationId":"`+correlation+`"`)) {
		t.Fatal("request was not traced in designated process")
	}
}
