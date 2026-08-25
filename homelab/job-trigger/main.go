// job-trigger는 홈서버(2026-08-25 AWS→Proxmox 이전)에서 SQS+Lambda 경로
// (infra/lambda/job-trigger/index.py)를 대체합니다 — orderapi가 SQS 대신
// 이 서비스에 POST /v1/jobs로 직접 요청을 보내고(orderapi/jobtrigger.HTTPPublisher),
// 여기서는 K8s Job을 만드는 대신 `docker run`으로 trader/replayengine
// 컨테이너를 직접 실행합니다. Docker 소켓을 마운트해서 "형제 컨테이너"를
// 띄우는 방식(Docker-outside-of-Docker)이라, job-trigger 자신도 컨테이너로
// 돌지만 호스트의 Docker 데몬에 직접 명령합니다.
//
// Lambda 원본의 _build_ai_trader_job/_build_replay_job과 최대한 같은 인자
// 구성을 유지합니다 — 리소스 제한(OOM 방지, Lambda 주석 참고)은 docker run의
// --memory/--cpus로 그대로 옮깁니다.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"time"
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

func main() {
	dockerNetwork := getenv("DOCKER_NETWORK", "homelab_default")
	backendURL := getenv("BACKEND_URL", "http://backend:8080")
	orderapiURL := getenv("ORDERAPI_URL", "http://orderapi:8081")
	bedrockRegion := getenv("BEDROCK_REGION", "ap-northeast-2")
	bedrockModelID := getenv("BEDROCK_MODEL_ID", "apac.anthropic.claude-3-haiku-20240307-v1:0")
	// Bedrock(AI 트레이더 LLM)은 홈서버로 이전할 수 없는 유일한 관리형 서비스라
	// (계획 문서 참고) 인터넷으로 계속 호출합니다 — IRSA가 없으므로 대신
	// Bedrock InvokeModel 전용 최소권한 액세스 키를 씁니다.
	awsAccessKeyID := os.Getenv("BEDROCK_AWS_ACCESS_KEY_ID")
	awsSecretAccessKey := os.Getenv("BEDROCK_AWS_SECRET_ACCESS_KEY")

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
			err = runAITrader(req, dockerNetwork, backendURL, orderapiURL, bedrockRegion, bedrockModelID, awsAccessKeyID, awsSecretAccessKey)
		case "replay":
			err = runReplay(req, dockerNetwork, orderapiURL)
		default:
			http.Error(w, `{"errorCode":"INVALID_JOB_TYPE","message":"jobType은 ai-trader 또는 replay만 가능합니다."}`, http.StatusBadRequest)
			return
		}
		if err != nil {
			log.Printf("Job 실행 실패 (jobType=%s): %v", req.JobType, err)
			http.Error(w, fmt.Sprintf(`{"errorCode":"INTERNAL_ERROR","message":%q}`, err.Error()), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("job-trigger 시작 :9000 (network=%s, backend=%s, orderapi=%s)", dockerNetwork, backendURL, orderapiURL)
	log.Fatal(http.ListenAndServe(":9000", mux))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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

// runAITrader는 job-trigger Lambda의 _build_ai_trader_job과 같은 리소스
// 한도(1Gi 요청/2Gi 한도, 2026-08-25 OOM 사고 대응)를 docker run으로 재현합니다.
func runAITrader(req jobRequest, network, backendURL, orderapiURL, bedrockRegion, bedrockModelID, accessKeyID, secretAccessKey string) error {
	name := "ai-trader-" + time.Now().Format("20060102-150405")
	args := []string{
		"run", "-d", "--rm",
		"--name", name,
		"--network", network,
		"--memory=2g", "--cpus=1",
		"-e", "BACKEND_URL=" + backendURL,
		"-e", "ORDERAPI_URL=" + orderapiURL,
		"-e", "BEDROCK_REGION=" + bedrockRegion,
		"-e", "BEDROCK_MODEL_ID=" + bedrockModelID,
	}
	if accessKeyID != "" {
		args = append(args, "-e", "AWS_ACCESS_KEY_ID="+accessKeyID, "-e", "AWS_SECRET_ACCESS_KEY="+secretAccessKey, "-e", "AWS_REGION="+bedrockRegion)
	}
	args = append(args, "truss-trader:latest")
	args = append(args, baseArgs(req)...)
	return runDocker(args)
}

// runReplay는 K8s Indexed Job(completions=shardCount, JOB_COMPLETION_INDEX
// 자동 주입)을 shardCount번의 개별 docker run으로 재현합니다 — 각 컨테이너에
// -shard-index를 직접 넘겨서 K8s가 자동으로 채워주던 것을 대신합니다.
func runReplay(req jobRequest, network, orderapiURL string) error {
	shardCount := 1
	if req.ShardCount != nil && *req.ShardCount > 0 {
		shardCount = *req.ShardCount
	}
	runID := "replay-" + time.Now().Format("20060102-150405")

	for i := 0; i < shardCount; i++ {
		name := fmt.Sprintf("%s-%d", runID, i)
		args := []string{
			"run", "-d", "--rm",
			"--name", name,
			"--network", network,
			"--memory=2g", "--cpus=1",
			"-e", "ORDERAPI_URL=" + orderapiURL,
			"truss-replayengine:latest",
		}
		args = append(args, baseArgs(req)...)
		args = append(args,
			"-run-id="+runID,
			"-shard-index="+strconv.Itoa(i),
			"-shard-count="+strconv.Itoa(shardCount),
		)
		if req.FromTS != nil {
			args = append(args, "-from-ts="+strconv.FormatInt(*req.FromTS, 10))
		}
		if req.ToTS != nil {
			args = append(args, "-to-ts="+strconv.FormatInt(*req.ToTS, 10))
		}
		if err := runDocker(args); err != nil {
			return fmt.Errorf("샤드 %d/%d 실행 실패: %w", i, shardCount, err)
		}
	}
	return nil
}

func runDocker(args []string) error {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %v 실패: %w (output=%s)", args, err, out)
	}
	log.Printf("컨테이너 시작: %s", out)
	return nil
}
