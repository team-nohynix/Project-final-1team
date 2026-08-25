package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakePrometheus는 실제 Prometheus HTTP API의 /api/v1/query 응답 모양만
// 흉내냅니다 — expr별로 결과를 미리 등록해두고, 등록 안 된 expr이 오면
// 빈 벡터(성공, 결과 없음)를 돌려줍니다.
func fakePrometheus(t *testing.T, results map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := url.QueryUnescape(r.URL.Query().Get("query"))
		if err != nil {
			t.Fatalf("query unescape 실패: %v", err)
		}
		body, ok := results[q]
		if !ok {
			body = `{"status":"success","data":{"resultType":"vector","result":[]}}`
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
}

func vectorResult(value string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"` + value + `"]}]}}`
}

func vectorResultWithPod(pod, value string) string {
	return `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"` + pod + `"},"value":[0,"` + value + `"]}]}}`
}

func TestClusterMetricsHandlerHappyPath(t *testing.T) {
	results := map[string]string{
		`count(up{job="k8s-nodes-cadvisor"} == 1)`:                                         vectorResult("7"),
		`sum(kube_pod_status_phase{exported_namespace="backend", phase="Running"})`:         vectorResult("4"),
		`sum(matching_engine_book_size)`:                                                    vectorResult("0"),
		`sum by (pod) (kube_pod_container_status_restarts_total{exported_namespace="backend"})`: vectorResultWithPod("kube-state-metrics-559fcf9d4-2sb", "3"),
		`kube_deployment_status_replicas{exported_namespace="backend", deployment="matching-engine"}`: vectorResult("2"),
		`kube_deployment_status_replicas{exported_namespace="backend", deployment="recorder"}`:        vectorResult("1"),
		`count(kube_node_spec_taint{key="workload", value="backend"})`:                     vectorResult("3"),
	}
	srv := fakePrometheus(t, results)
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/cluster-metrics", nil)
	clusterMetricsHandler(newPromQuerier(srv.URL))(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}
	var got clusterMetricsResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("응답 디코딩 실패: %v", err)
	}

	if got.ActiveNodes != 7 {
		t.Errorf("ActiveNodes = %d, want 7", got.ActiveNodes)
	}
	if got.RunningPodsBackend != 4 {
		t.Errorf("RunningPodsBackend = %d, want 4", got.RunningPodsBackend)
	}
	if got.MatchingBookSize != 0 {
		t.Errorf("MatchingBookSize = %d, want 0", got.MatchingBookSize)
	}
	if len(got.PodRestarts) != 1 || got.PodRestarts[0].Pod != "kube-state-metrics-559fcf9d4-2sb" || got.PodRestarts[0].Restarts != 3 {
		t.Errorf("PodRestarts = %+v, want [{kube-state-metrics-559fcf9d4-2sb 3}]", got.PodRestarts)
	}
	if got.Autoscaling.Matching.Current != 2 || got.Autoscaling.Matching.Max != 10 {
		t.Errorf("Autoscaling.Matching = %+v, want {2 10}", got.Autoscaling.Matching)
	}
	if got.Autoscaling.Recorder.Current != 1 || got.Autoscaling.Recorder.Max != 10 {
		t.Errorf("Autoscaling.Recorder = %+v, want {1 10}", got.Autoscaling.Recorder)
	}
	if got.Autoscaling.KarpenterNodes != 3 {
		t.Errorf("Autoscaling.KarpenterNodes = %d, want 3", got.Autoscaling.KarpenterNodes)
	}
}

func TestClusterMetricsHandlerFiltersZeroRestartPods(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(q, "restarts_total") {
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[
				{"metric":{"pod":"a"},"value":[0,"0"]},
				{"metric":{"pod":"b"},"value":[0,"2"]}
			]}}`))
			return
		}
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[0,"1"]}]}}`))
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/cluster-metrics", nil)
	clusterMetricsHandler(newPromQuerier(srv.URL))(rec, req)

	var got clusterMetricsResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if len(got.PodRestarts) != 1 || got.PodRestarts[0].Pod != "b" {
		t.Errorf("PodRestarts = %+v, want only pod b (0-restart pod a는 제외돼야 함)", got.PodRestarts)
	}
}

func TestClusterMetricsHandlerPrometheusDownReturns502(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/cluster-metrics", nil)
	clusterMetricsHandler(newPromQuerier(srv.URL))(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	var got errorResponse
	json.NewDecoder(rec.Body).Decode(&got)
	if got.ErrorCode != "PROMETHEUS_UNAVAILABLE" {
		t.Errorf("errorCode = %q, want PROMETHEUS_UNAVAILABLE", got.ErrorCode)
	}
}
