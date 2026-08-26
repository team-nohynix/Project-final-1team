// job-trigger는 kubeadm 이전(2026-08-26) 이후, K8s Job 생성 방식을 원래
// 설계(infra/lambda/job-trigger/index.py가 AWS Lambda에서 하던 일)대로 되돌린
// 것입니다 — Docker Compose 시절의 docker-run 우회(homelab/job-trigger/main.go
// git 이력 참고)는 K8s 자체가 없어서 어쩔 수 없이 썼던 임시방편이었고, 진짜
// K8s가 생겼으니 client-go로 in-cluster API를 불러 진짜 batch/v1 Job을
// 만드는 원래 방식이 더 간단합니다 — completionMode: Indexed가
// JOB_COMPLETION_INDEX를 자동 주입해줘서, replay의 -shard-index를 직접
// 계산해 넘기던 반복문도 필요 없어졌습니다.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type jobRequest struct {
	JobType     string   `json:"jobType"`
	Date        string   `json:"date"`
	Speed       *float64 `json:"speed,omitempty"`
	OrderBucket string   `json:"orderBucket,omitempty"`
	ShardCount  *int     `json:"shardCount,omitempty"`
	FromTS      *int64   `json:"fromTs,omitempty"`
	ToTS        *int64   `json:"toTs,omitempty"`
}

// orderRecordsHostPath는 trader/replayengine이 orderapi와 같은 주문 기록을
// 공유하기 위한 hostPath — 단일 노드 클러스터라 PVC(RWX 미지원) 대신
// 이 경로를 그대로 마운트하면 여러 파드가 동시에 같은 디렉터리에 쓸 수
// 있다(homelab/k8s/apps/orderapi-deployment.yaml과 동일 경로).
const orderRecordsHostPath = "/var/lib/truss/order-records"

func main() {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.Fatalf("in-cluster config 획득 실패: %v", err)
	}
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		log.Fatalf("k8s 클라이언트 생성 실패: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req jobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"errorCode":"INVALID_BODY","message":"잘못된 요청 본문입니다."}`, http.StatusBadRequest)
			return
		}

		var err error
		switch req.JobType {
		case "ai-trader":
			err = runAITraderJob(r.Context(), clientset, req)
		case "replay":
			err = runReplayJob(r.Context(), clientset, req)
		default:
			http.Error(w, `{"errorCode":"INVALID_JOB_TYPE","message":"jobType은 ai-trader 또는 replay만 가능합니다."}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			log.Printf("Job 생성 실패 (jobType=%s): %v", req.JobType, err)
			http.Error(w, fmt.Sprintf(`{"errorCode":"INTERNAL_ERROR","message":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("job-trigger 시작 :9000 (K8s Job 생성 방식)")
	log.Fatal(http.ListenAndServe(":9000", mux))
}

func baseArgs(req jobRequest) []string {
	args := []string{"-date=" + req.Date}
	if req.Speed != nil {
		args = append(args, "-speed="+strconv.FormatFloat(*req.Speed, 'f', -1, 64))
	}
	if req.OrderBucket != "" {
		args = append(args, "-order-bucket="+req.OrderBucket)
	}
	return args
}

func orderRecordsVolume() (corev1.Volume, corev1.VolumeMount) {
	vol := corev1.Volume{
		Name: "order-records",
		VolumeSource: corev1.VolumeSource{
			HostPath: &corev1.HostPathVolumeSource{
				Path: orderRecordsHostPath,
				Type: func() *corev1.HostPathType { t := corev1.HostPathDirectoryOrCreate; return &t }(),
			},
		},
	}
	mount := corev1.VolumeMount{Name: "order-records", MountPath: "/app/orders"}
	return vol, mount
}

// runAITraderJob은 infra/lambda/job-trigger/index.py의 _build_ai_trader_job과
// 같은 구성(ai-trader-config ConfigMap, sa-ai-trader ServiceAccount, 리소스
// 한도)을 client-go로 재현합니다.
func runAITraderJob(ctx context.Context, clientset *kubernetes.Clientset, req jobRequest) error {
	name := "ai-trader-" + time.Now().Format("20060102-150405")
	vol, mount := orderRecordsVolume()

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ai-trader"},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptrInt32(0),
			TTLSecondsAfterFinished: ptrInt32(3600),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "sa-ai-trader",
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "ai-trader",
							Image:           "truss-trader:latest",
							ImagePullPolicy: corev1.PullNever,
							Args:            baseArgs(req),
							EnvFrom: []corev1.EnvFromSource{
								{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "ai-trader-config"}}},
								{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "bedrock-credentials"}, Optional: ptrBool(true)}},
							},
							VolumeMounts: []corev1.VolumeMount{mount},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("250m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{vol},
				},
			},
		},
	}

	_, err := clientset.BatchV1().Jobs("ai-trader").Create(ctx, job, metav1.CreateOptions{})
	return err
}

// runReplayJob은 _build_replay_job과 같은 구성 — completionMode: Indexed로
// K8s가 파드마다 JOB_COMPLETION_INDEX를 자동 주입하게 해서, replayengine의
// -shard-index를 그 값으로 그대로 채운다(별도 반복문 불필요, Compose 시절과
// 다른 점).
func runReplayJob(ctx context.Context, clientset *kubernetes.Clientset, req jobRequest) error {
	shardCount := int32(1)
	if req.ShardCount != nil && *req.ShardCount > 0 {
		shardCount = int32(*req.ShardCount)
	}
	name := "replay-" + time.Now().Format("20060102-150405")
	vol, mount := orderRecordsVolume()

	args := append(baseArgs(req),
		"-run-id="+name,
		"-shard-index=$(JOB_COMPLETION_INDEX)",
		"-shard-count="+strconv.Itoa(int(shardCount)),
	)
	if req.FromTS != nil {
		args = append(args, "-from-ts="+strconv.FormatInt(*req.FromTS, 10))
	}
	if req.ToTS != nil {
		args = append(args, "-to-ts="+strconv.FormatInt(*req.ToTS, 10))
	}

	completionMode := batchv1.IndexedCompletion
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "replay"},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptrInt32(0),
			TTLSecondsAfterFinished: ptrInt32(3600),
			Completions:             ptrInt32(shardCount),
			Parallelism:             ptrInt32(shardCount),
			CompletionMode:          &completionMode,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					ServiceAccountName: "sa-replay-engine",
					RestartPolicy:      corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name:            "replay-engine",
							Image:           "truss-replayengine:latest",
							ImagePullPolicy: corev1.PullNever,
							Args:            args,
							EnvFrom: []corev1.EnvFromSource{
								{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "replay-config"}}},
							},
							VolumeMounts: []corev1.VolumeMount{mount},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("250m"),
									corev1.ResourceMemory: resource.MustParse("512Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{vol},
				},
			},
		},
	}

	_, err := clientset.BatchV1().Jobs("replay").Create(ctx, job, metav1.CreateOptions{})
	return err
}

func ptrInt32(v int32) *int32 { return &v }
func ptrBool(v bool) *bool    { return &v }
