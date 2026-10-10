package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

const podsFixture = `{"items":[
	{"metadata":{"name":"a"},"status":{"phase":"Running"}},
	{"metadata":{"name":"b"},"status":{"phase":"Pending"}},
	{"metadata":{"name":"c"},"status":{"phase":"Running"}}
]}`

func fakeFS() fstest.MapFS {
	fsys := fstest.MapFS{podsFile: {Data: []byte(podsFixture)}}
	for path, file := range routes {
		fsys[file] = &fstest.MapFile{Data: []byte(`{"served":"` + path + `"}`)}
	}
	return fsys
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := newHandler(fakeFS())
	if err != nil {
		t.Fatalf("newHandler: %v", err)
	}
	return h
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func podNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	var list struct {
		Kind  string `json:"kind"`
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode pod list: %v (body %s)", err, rec.Body)
	}
	if list.Kind != "PodList" {
		t.Errorf("kind = %q, want PodList", list.Kind)
	}
	names := []string{}
	for _, item := range list.Items {
		names = append(names, item.Metadata.Name)
	}
	return names
}

func TestEveryRouteServesItsFixture(t *testing.T) {
	h := newTestHandler(t)
	for path := range routes {
		rec := get(t, h, path+"?resourceVersion=0")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", path, rec.Code)
			continue
		}
		if want := `{"served":"` + path + `"}`; rec.Body.String() != want {
			t.Errorf("%s: body %s, want %s", path, rec.Body, want)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s: content type %q", path, ct)
		}
	}
}

func TestPodsHonourPhaseSelector(t *testing.T) {
	h := newTestHandler(t)
	cases := []struct {
		name, query string
		want        []string
	}{
		{"no selector serves every pod", "", []string{"a", "b", "c"}},
		{"running", "?fieldSelector=status.phase%3DRunning&limit=200&resourceVersion=0", []string{"a", "c"}},
		{"pending", "?fieldSelector=status.phase%3DPending&resourceVersion=0", []string{"b"}},
		{"double equals", "?fieldSelector=status.phase%3D%3DPending", []string{"b"}},
		{"phase with no pods is an empty list", "?fieldSelector=status.phase%3DFailed", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := get(t, h, podsPath+tc.query)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200", rec.Code)
			}
			if got := podNames(t, rec); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("pods = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsRejectUnsupportedSelector(t *testing.T) {
	h := newTestHandler(t)
	for _, selector := range []string{"spec.nodeName%3Dn1", "status.phase%21%3DRunning", "status.phase%3D", "status.phase%3DRunning%2Cspec.nodeName%3Dn1"} {
		rec := get(t, h, podsPath+"?fieldSelector="+selector)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", selector, rec.Code)
		}
	}
}

func TestUnknownPathIsKubernetesNotFound(t *testing.T) {
	rec := get(t, newTestHandler(t), "/apis/security.istio.io/v1beta1")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	var status struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
		Code   int    `json:"code"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Kind != "Status" || status.Reason != "NotFound" || status.Code != http.StatusNotFound {
		t.Errorf("status body = %+v", status)
	}
}

func TestStartupFailsOnMissingOrInvalidFixture(t *testing.T) {
	missing := fakeFS()
	delete(missing, "nodes.json")
	if _, err := newHandler(missing); err == nil || !strings.Contains(err.Error(), "nodes.json") {
		t.Errorf("missing fixture: err = %v, want it to name nodes.json", err)
	}

	invalid := fakeFS()
	invalid["jobs.json"] = &fstest.MapFile{Data: []byte(`{"items":[`)}
	if _, err := newHandler(invalid); err == nil || !strings.Contains(err.Error(), "jobs.json") {
		t.Errorf("invalid fixture: err = %v, want it to name jobs.json", err)
	}
}

func TestRunFailsFastOnBadDirOrBusyAddress(t *testing.T) {
	if err := run("127.0.0.1:0", t.TempDir()); err == nil {
		t.Error("empty fixture dir: want an error, got nil")
	}

	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = busy.Close() }()
	if err := run(busy.Addr().String(), "../../testdata"); err == nil {
		t.Error("busy address: want an error, got nil")
	}
}

func TestRepositoryTestdataLoads(t *testing.T) {
	h, err := newHandler(os.DirFS("../../testdata"))
	if err != nil {
		t.Fatalf("testdata does not satisfy the fixture routes: %v", err)
	}
	running := podNames(t, get(t, h, podsPath+"?fieldSelector=status.phase%3DRunning"))
	pending := podNames(t, get(t, h, podsPath+"?fieldSelector=status.phase%3DPending"))
	all := podNames(t, get(t, h, podsPath))
	if len(running) == 0 || len(pending) == 0 || len(running)+len(pending) > len(all) {
		t.Errorf("running %d + pending %d pods, of %d total", len(running), len(pending), len(all))
	}
}
